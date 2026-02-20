package app

import (
	"testing"
	"time"

	"alerting/internal/config"
	"alerting/internal/domain"
)

func TestShouldDropOutOfOrderByMaxLate(t *testing.T) {
	t.Parallel()

	now := time.UnixMilli(1_739_876_543_210)
	rule := config.RuleConfig{
		OutOfOrder: config.RuleOutOfOrder{MaxLateMS: 1_000},
	}
	manager := &Manager{}

	if !manager.shouldDropOutOfOrder(rule, domain.Event{DT: now.UnixMilli() - 1_001}, now) {
		t.Fatalf("expected drop for too-late event")
	}
	if manager.shouldDropOutOfOrder(rule, domain.Event{DT: now.UnixMilli() - 1_000}, now) {
		t.Fatalf("did not expect drop at max_late_ms boundary")
	}
}

func TestShouldDropOutOfOrderByMaxFutureSkew(t *testing.T) {
	t.Parallel()

	now := time.UnixMilli(1_739_876_543_210)
	rule := config.RuleConfig{
		OutOfOrder: config.RuleOutOfOrder{MaxFutureSkewMS: 500},
	}
	manager := &Manager{}

	if !manager.shouldDropOutOfOrder(rule, domain.Event{DT: now.UnixMilli() + 501}, now) {
		t.Fatalf("expected drop for too-future event")
	}
	if manager.shouldDropOutOfOrder(rule, domain.Event{DT: now.UnixMilli() + 500}, now) {
		t.Fatalf("did not expect drop at max_future_skew_ms boundary")
	}
}

func TestShouldDropOutOfOrderDisabledByZeroLimits(t *testing.T) {
	t.Parallel()

	now := time.UnixMilli(1_739_876_543_210)
	rule := config.RuleConfig{
		OutOfOrder: config.RuleOutOfOrder{},
	}
	manager := &Manager{}

	if manager.shouldDropOutOfOrder(rule, domain.Event{DT: now.UnixMilli() - 10_000}, now) {
		t.Fatalf("did not expect drop when out_of_order limits are disabled")
	}
	if manager.shouldDropOutOfOrder(rule, domain.Event{DT: now.UnixMilli() + 10_000}, now) {
		t.Fatalf("did not expect drop when out_of_order limits are disabled")
	}
}
