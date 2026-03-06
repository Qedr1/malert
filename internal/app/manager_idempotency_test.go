package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"alerting/internal/config"
	"alerting/internal/domain"
	"alerting/internal/engine"
	"alerting/internal/notify"
	"alerting/internal/state"
)

type createConflictStore struct {
	*state.MemoryStore
	inject atomic.Bool
}

func (s *createConflictStore) CreateCard(ctx context.Context, alertID string, card domain.AlertCard) (uint64, error) {
	if s.inject.CompareAndSwap(true, false) {
		if _, err := s.MemoryStore.CreateCard(ctx, alertID, card); err != nil {
			return 0, err
		}
		return 0, state.ErrConflict
	}
	return s.MemoryStore.CreateCard(ctx, alertID, card)
}

type updateConflictStore struct {
	*state.MemoryStore
	inject atomic.Bool
}

func (s *updateConflictStore) UpdateCard(ctx context.Context, alertID string, expectedRevision uint64, card domain.AlertCard) (uint64, error) {
	if s.inject.CompareAndSwap(true, false) {
		if _, err := s.MemoryStore.UpdateCard(ctx, alertID, expectedRevision, card); err != nil {
			return 0, err
		}
		return 0, state.ErrConflict
	}
	return s.MemoryStore.UpdateCard(ctx, alertID, expectedRevision, card)
}

func TestApplyDecisionSkipsNotifyOnCreateConflict(t *testing.T) {
	t.Parallel()

	rule, cfg, requests := newHTTPNotifyRuleConfig(t, false)
	store := &createConflictStore{MemoryStore: state.NewMemoryStore(time.Now)}
	store.inject.Store(true)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := NewManager(cfg, logger, store, notify.NewDispatcher(cfg.Notify, logger), fixedClock{now: time.Unix(1_740_000_000, 0).UTC()})

	event := idempotencyEvent(1_740_000_000_000)
	alertID, err := engine.BuildAlertKey(rule, event)
	if err != nil {
		t.Fatalf("build alert id: %v", err)
	}
	now := time.Unix(1_740_000_000, 0).UTC()
	decision := manager.engine.ProcessEvent(rule, event, alertID, now)
	if err := manager.applyDecision(context.Background(), rule, alertID, decision, now, nil); err != nil {
		t.Fatalf("applyDecision: %v", err)
	}
	if got := atomic.LoadInt32(requests); got != 0 {
		t.Fatalf("expected no notification on create conflict, got %d", got)
	}
}

func TestApplyDecisionSkipsNotifyOnUpdateConflict(t *testing.T) {
	t.Parallel()

	rule, cfg, requests := newHTTPNotifyRuleConfig(t, true)
	store := &updateConflictStore{MemoryStore: state.NewMemoryStore(time.Now)}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := NewManager(cfg, logger, store, notify.NewDispatcher(cfg.Notify, logger), fixedClock{now: time.Unix(1_740_000_100, 0).UTC()})

	event := idempotencyEvent(1_740_000_100_000)
	alertID, err := engine.BuildAlertKey(rule, event)
	if err != nil {
		t.Fatalf("build alert id: %v", err)
	}
	start := time.Unix(1_740_000_100, 0).UTC()
	decision := manager.engine.ProcessEvent(rule, event, alertID, start)
	if err := manager.applyDecision(context.Background(), rule, alertID, decision, start, nil); err != nil {
		t.Fatalf("applyDecision pending: %v", err)
	}

	store.inject.Store(true)
	fireAt := start.Add(2 * time.Second)
	decision = manager.engine.ProcessEvent(rule, event, alertID, fireAt)
	if err := manager.applyDecision(context.Background(), rule, alertID, decision, fireAt, nil); err != nil {
		t.Fatalf("applyDecision firing: %v", err)
	}
	if got := atomic.LoadInt32(requests); got != 0 {
		t.Fatalf("expected no notification on update conflict, got %d", got)
	}
}

func newHTTPNotifyRuleConfig(t *testing.T, pending bool) (config.RuleConfig, config.Config, *int32) {
	t.Helper()

	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		atomic.AddInt32(&requests, 1)
		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	rule := config.RuleConfig{
		Name:      "rule_idempotency",
		AlertType: "count_total",
		Match: config.RuleMatch{
			Type: []string{"event"},
			Var:  []string{"errors"},
		},
		Key: config.RuleKey{
			FromTags: []string{"host"},
		},
		Raise: config.RuleRaise{N: 1},
		Resolve: config.RuleResolve{
			SilenceSec: 30,
		},
		Pending: config.RulePending{
			Enabled:  pending,
			DelaySec: 1,
		},
		Notify: config.RuleNotify{
			Route: []config.RuleNotifyRoute{{
				Name:     "http_main",
				Channel:  config.NotifyChannelHTTP,
				Template: "http_default",
				Mode:     config.NotifyRouteModeHistory,
			}},
		},
	}
	cfg := config.Config{
		Notify: config.NotifyConfig{
			HTTP: config.HTTPNotifier{
				Enabled:    true,
				URL:        server.URL,
				Method:     "POST",
				TimeoutSec: 2,
				NameTemplate: []config.NamedTemplateConfig{{
					Name:    "http_default",
					Message: "{{ .Message }}",
				}},
			},
		},
		Rule: []config.RuleConfig{rule},
	}
	return rule, cfg, &requests
}

func idempotencyEvent(dt int64) domain.Event {
	value := 1.0
	return domain.Event{
		DT:   dt,
		Type: domain.EventTypeEvent,
		Tags: map[string]string{
			"host": "app-1",
		},
		Var: "errors",
		Value: domain.TypedValue{
			Type: "n",
			N:    &value,
		},
		AggCnt: 1,
	}
}
