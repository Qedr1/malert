package app

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"alerting/internal/config"
	"alerting/internal/domain"
	"alerting/internal/engine"
	"alerting/internal/notify"
	"alerting/internal/state"
)

type tickCountStore struct {
	state.Store

	mu    sync.Mutex
	calls map[string]int
}

func newTickCountStore() *tickCountStore {
	return &tickCountStore{
		Store: state.NewMemoryStore(time.Now),
		calls: make(map[string]int),
	}
}

func (s *tickCountStore) RefreshTick(ctx context.Context, alertID string, lastSeen time.Time, ttl time.Duration) error {
	s.mu.Lock()
	s.calls[alertID]++
	s.mu.Unlock()
	return s.Store.RefreshTick(ctx, alertID, lastSeen, ttl)
}

func (s *tickCountStore) totalCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0
	for _, count := range s.calls {
		total += count
	}
	return total
}

func (s *tickCountStore) callsByAlert(alertID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[alertID]
}

type stepClock struct {
	mu   sync.Mutex
	now  time.Time
	step time.Duration
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	current := c.now
	c.now = c.now.Add(c.step)
	return current
}

func TestPushBatchCoalescesTickRefreshByAlertID(t *testing.T) {
	t.Parallel()

	store := newTickCountStore()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dispatcher := notify.NewDispatcher(config.NotifyConfig{}, logger)
	manager := NewManager(
		config.Config{Rule: []config.RuleConfig{batchRule("rule_coalesce")}},
		logger,
		store,
		dispatcher,
		&stepClock{now: time.Unix(1_739_000_000, 0).UTC(), step: time.Millisecond},
	)

	rule := batchRule("rule_coalesce")
	events := []domain.Event{
		batchEvent("host-1", 1_739_000_000_000),
		batchEvent("host-1", 1_739_000_000_100),
	}
	if err := manager.PushBatch(events); err != nil {
		t.Fatalf("push batch: %v", err)
	}

	alertID, err := engine.BuildAlertKey(rule, events[0])
	if err != nil {
		t.Fatalf("build alert id: %v", err)
	}
	if got := store.callsByAlert(alertID); got != 1 {
		t.Fatalf("expected one tick refresh for alert %q, got %d", alertID, got)
	}
	if got := store.totalCalls(); got != 1 {
		t.Fatalf("expected one tick refresh in batch, got %d", got)
	}
}

func TestPushBatchRefreshesTickPerDistinctAlertID(t *testing.T) {
	t.Parallel()

	store := newTickCountStore()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dispatcher := notify.NewDispatcher(config.NotifyConfig{}, logger)
	manager := NewManager(
		config.Config{Rule: []config.RuleConfig{batchRule("rule_distinct")}},
		logger,
		store,
		dispatcher,
		&stepClock{now: time.Unix(1_739_000_100, 0).UTC(), step: time.Millisecond},
	)

	events := []domain.Event{
		batchEvent("host-a", 1_739_000_100_000),
		batchEvent("host-b", 1_739_000_100_100),
	}
	if err := manager.PushBatch(events); err != nil {
		t.Fatalf("push batch: %v", err)
	}
	if got := store.totalCalls(); got != 2 {
		t.Fatalf("expected two tick refreshes for two alerts, got %d", got)
	}
}

func batchRule(name string) config.RuleConfig {
	return config.RuleConfig{
		Name:      name,
		AlertType: "count_total",
		Match: config.RuleMatch{
			Type: []string{"event"},
			Var:  []string{"errors"},
			Tags: map[string]config.StringList{
				"dc":      config.StringList{"dc1"},
				"service": config.StringList{"api"},
			},
		},
		Key: config.RuleKey{
			FromTags: []string{"dc", "host", "service"},
		},
		Raise: config.RuleRaise{
			N: 1,
		},
		Resolve: config.RuleResolve{
			SilenceSec: 30,
		},
	}
}

func batchEvent(host string, dt int64) domain.Event {
	value := 1.0
	return domain.Event{
		DT:   dt,
		Type: domain.EventTypeEvent,
		Tags: map[string]string{
			"dc":      "dc1",
			"service": "api",
			"host":    host,
		},
		Var: "errors",
		Value: domain.TypedValue{
			Type: "n",
			N:    &value,
		},
		AggCnt: 1,
		Win:    0,
	}
}
