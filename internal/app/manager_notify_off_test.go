package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"alerting/internal/config"
	"alerting/internal/domain"
	"alerting/internal/notify"
	"alerting/internal/notifyqueue"
	"alerting/internal/state"
)

type fixedClock struct {
	now time.Time
}

func (c fixedClock) Now() time.Time {
	return c.now
}

type captureProducer struct {
	mu   sync.Mutex
	jobs []notifyqueue.Job
}

func (p *captureProducer) Enqueue(_ context.Context, job notifyqueue.Job) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.jobs = append(p.jobs, job)
	return nil
}

func (p *captureProducer) Close() error {
	return nil
}

func (p *captureProducer) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.jobs)
}

func TestDispatchNotificationSkipsInOffWindowDirect(t *testing.T) {
	now := time.Now().In(time.Local)
	weekday := weekdayToken(now.Weekday())
	rule := notifyOffRule("rule_off_direct", []config.RuleNotifyOff{{
		Days: []string{weekday},
		From: "00:00",
		To:   "24:00",
	}})

	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		atomic.AddInt32(&requests, 1)
		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	cfg := notifyOffConfig(server.URL, rule)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := NewManager(cfg, logger, state.NewMemoryStore(time.Now), notify.NewDispatcher(cfg.Notify, logger), fixedClock{now: now})

	notification := domain.Notification{
		AlertID:   "rule/test/errors/hash",
		RuleName:  rule.Name,
		State:     domain.AlertStateFiring,
		Message:   "alert is firing",
		Timestamp: now,
	}
	if err := manager.dispatchNotification(context.Background(), rule, notification); err != nil {
		t.Fatalf("dispatchNotification: %v", err)
	}
	if got := atomic.LoadInt32(&requests); got != 0 {
		t.Fatalf("expected no direct send inside off window, got %d requests", got)
	}
}

func TestDispatchNotificationSendsOutsideOffWindowDirect(t *testing.T) {
	now := time.Now().In(time.Local)
	nextWeekday := weekdayToken((now.Weekday() + 1) % 7)
	rule := notifyOffRule("rule_no_off_now", []config.RuleNotifyOff{{
		Days: []string{nextWeekday},
		From: "00:00",
		To:   "24:00",
	}})

	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		atomic.AddInt32(&requests, 1)
		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	cfg := notifyOffConfig(server.URL, rule)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := NewManager(cfg, logger, state.NewMemoryStore(time.Now), notify.NewDispatcher(cfg.Notify, logger), fixedClock{now: now})

	notification := domain.Notification{
		AlertID:   "rule/test/errors/hash",
		RuleName:  rule.Name,
		State:     domain.AlertStateFiring,
		Message:   "alert is firing",
		Timestamp: now,
	}
	if err := manager.dispatchNotification(context.Background(), rule, notification); err != nil {
		t.Fatalf("dispatchNotification: %v", err)
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Fatalf("expected one direct send outside off window, got %d requests", got)
	}
}

func TestDispatchNotificationSkipsInOffWindowQueue(t *testing.T) {
	now := time.Now().In(time.Local)
	weekday := weekdayToken(now.Weekday())
	rule := notifyOffRule("rule_off_queue", []config.RuleNotifyOff{{
		Days: []string{weekday},
		From: "00:00",
		To:   "24:00",
	}})

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := NewManager(
		notifyOffConfig("http://127.0.0.1:1", rule),
		logger,
		state.NewMemoryStore(time.Now),
		nil,
		fixedClock{now: now},
	)
	producer := &captureProducer{}
	manager.SetQueueProducer(producer)

	notification := domain.Notification{
		AlertID:   "rule/test/errors/hash",
		RuleName:  rule.Name,
		State:     domain.AlertStateFiring,
		Message:   "alert is firing",
		Timestamp: now,
	}
	if err := manager.dispatchNotification(context.Background(), rule, notification); err != nil {
		t.Fatalf("dispatchNotification: %v", err)
	}
	if got := producer.count(); got != 0 {
		t.Fatalf("expected no queue jobs inside off window, got %d", got)
	}
}

func TestProcessQueuedNotificationSkipsInOffWindow(t *testing.T) {
	now := time.Now().In(time.Local)
	weekday := weekdayToken(now.Weekday())
	rule := notifyOffRule("rule_off_worker", []config.RuleNotifyOff{{
		Days: []string{weekday},
		From: "00:00",
		To:   "24:00",
	}})

	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		atomic.AddInt32(&requests, 1)
		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	cfg := notifyOffConfig(server.URL, rule)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := NewManager(cfg, logger, state.NewMemoryStore(time.Now), notify.NewDispatcher(cfg.Notify, logger), fixedClock{now: now})

	job := notifyqueue.Job{
		ID:        "job-1",
		RouteKey:  "http_main",
		RouteMode: config.NotifyRouteModeHistory,
		Channel:   config.NotifyChannelHTTP,
		Template:  "http_default",
		Notification: domain.Notification{
			AlertID:   "rule/test/errors/hash",
			RuleName:  rule.Name,
			State:     domain.AlertStateFiring,
			Message:   "alert is firing",
			Timestamp: now,
		},
		CreatedAt: now,
	}

	if err := manager.ProcessQueuedNotification(context.Background(), job); err != nil {
		t.Fatalf("ProcessQueuedNotification: %v", err)
	}
	if got := atomic.LoadInt32(&requests); got != 0 {
		t.Fatalf("expected no worker send inside off window, got %d requests", got)
	}
}

func notifyOffConfig(httpURL string, rule config.RuleConfig) config.Config {
	return config.Config{
		Notify: config.NotifyConfig{
			HTTP: config.HTTPNotifier{
				Enabled:    true,
				URL:        httpURL,
				Method:     "POST",
				TimeoutSec: 2,
				NameTemplate: []config.NamedTemplateConfig{
					{
						Name:    "http_default",
						Message: "{{ .Message }}",
					},
				},
			},
		},
		Rule: []config.RuleConfig{rule},
	}
}

func notifyOffRule(name string, off []config.RuleNotifyOff) config.RuleConfig {
	return config.RuleConfig{
		Name:      name,
		AlertType: "count_total",
		Match: config.RuleMatch{
			Type: []string{"event"},
			Var:  []string{"errors"},
			Tags: map[string]config.StringList{
				"dc": {"dc1"},
			},
		},
		Key: config.RuleKey{
			FromTags: []string{"dc"},
		},
		Raise: config.RuleRaise{
			N: 1,
		},
		Resolve: config.RuleResolve{
			SilenceSec: 5,
		},
		Notify: config.RuleNotify{
			Off: off,
			Route: []config.RuleNotifyRoute{
				{
					Name:     "http_main",
					Channel:  config.NotifyChannelHTTP,
					Template: "http_default",
					Mode:     config.NotifyRouteModeHistory,
				},
			},
		},
	}
}

func weekdayToken(day time.Weekday) string {
	switch day {
	case time.Sunday:
		return "sun"
	case time.Monday:
		return "mon"
	case time.Tuesday:
		return "tue"
	case time.Wednesday:
		return "wed"
	case time.Thursday:
		return "thu"
	case time.Friday:
		return "fri"
	default:
		return "sat"
	}
}
