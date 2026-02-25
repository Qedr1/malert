package engine

import (
	"testing"
	"time"

	"alerting/internal/config"
	"alerting/internal/domain"
)

func TestEnginePendingToFiringAndResolveBySilence(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	rule := config.RuleConfig{
		Name:      "ct",
		AlertType: "count_total",
		Match: config.RuleMatch{
			Type: []string{"event"},
			Var:  []string{"errors"},
			Tags: map[string]config.StringList{"dc": {"dc1"}},
		},
		Pending: config.RulePending{Enabled: true, DelaySec: 10},
		Raise:   config.RuleRaise{N: 2},
		Resolve: config.RuleResolve{SilenceSec: 5},
	}
	e := New()

	numberValue := 1.0
	event := domain.Event{
		Type:   domain.EventTypeEvent,
		AggCnt: 2,
		Var:    "errors",
		Tags:   map[string]string{"dc": "dc1"},
		Value:  domain.TypedValue{Type: "n", N: &numberValue},
	}
	decision := e.ProcessEvent(rule, event, "rule/ct/errors/hash", now)
	if !decision.StateChanged || decision.State != domain.AlertStatePending {
		t.Fatalf("expected pending transition, got %+v", decision)
	}

	decision = e.ProcessEvent(rule, event, "rule/ct/errors/hash", now.Add(12*time.Second))
	if !decision.StateChanged || decision.State != domain.AlertStateFiring {
		t.Fatalf("expected firing transition, got %+v", decision)
	}

	ticks := e.TickRule(rule, now.Add(20*time.Second))
	if len(ticks) != 1 {
		t.Fatalf("expected one resolve decision, got %d", len(ticks))
	}
	if ticks[0].State != domain.AlertStateResolved {
		t.Fatalf("expected resolved state, got %+v", ticks[0])
	}
}

func TestEngineMissingHeartbeatFireAndResolve(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	rule := config.RuleConfig{
		Name:      "hb",
		AlertType: "missing_heartbeat",
		Match: config.RuleMatch{
			Type: []string{"event"},
			Var:  []string{"heartbeat"},
			Tags: map[string]config.StringList{"dc": {"dc1"}},
		},
		Pending: config.RulePending{Enabled: false},
		Raise:   config.RuleRaise{MissingSec: 5},
	}
	e := New()

	numberValue := 1.0
	heartbeat := domain.Event{
		Type:   domain.EventTypeEvent,
		AggCnt: 1,
		Var:    "heartbeat",
		Tags:   map[string]string{"dc": "dc1"},
		Value:  domain.TypedValue{Type: "n", N: &numberValue},
	}
	decision := e.ProcessEvent(rule, heartbeat, "rule/hb/heartbeat/hash", now)
	if !decision.Matched {
		t.Fatalf("expected initial heartbeat matched")
	}

	ticks := e.TickRule(rule, now.Add(6*time.Second))
	if len(ticks) != 1 || ticks[0].State != domain.AlertStateFiring {
		t.Fatalf("expected firing from missing heartbeat, got %+v", ticks)
	}

	decision = e.ProcessEvent(rule, heartbeat, "rule/hb/heartbeat/hash", now.Add(7*time.Second))
	if !decision.StateChanged || decision.State != domain.AlertStateResolved {
		t.Fatalf("expected resolved on new heartbeat, got %+v", decision)
	}
}

func TestEngineMissingHeartbeatResolveHysteresisHold(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	rule := config.RuleConfig{
		Name:      "hb",
		AlertType: "missing_heartbeat",
		Match: config.RuleMatch{
			Type: []string{"event"},
			Var:  []string{"heartbeat"},
			Tags: map[string]config.StringList{"dc": {"dc1"}},
		},
		Pending: config.RulePending{Enabled: false},
		Raise:   config.RuleRaise{MissingSec: 5},
		Resolve: config.RuleResolve{HysteresisSec: 4},
	}
	e := New()

	numberValue := 1.0
	heartbeat := domain.Event{
		Type:   domain.EventTypeEvent,
		AggCnt: 1,
		Var:    "heartbeat",
		Tags:   map[string]string{"dc": "dc1"},
		Value:  domain.TypedValue{Type: "n", N: &numberValue},
	}
	_ = e.ProcessEvent(rule, heartbeat, "rule/hb/heartbeat/hash", now)

	ticks := e.TickRule(rule, now.Add(6*time.Second))
	if len(ticks) != 1 || ticks[0].State != domain.AlertStateFiring {
		t.Fatalf("expected firing from missing heartbeat, got %+v", ticks)
	}

	decision := e.ProcessEvent(rule, heartbeat, "rule/hb/heartbeat/hash", now.Add(7*time.Second))
	if decision.StateChanged || decision.State != domain.AlertStateFiring {
		t.Fatalf("expected firing hold during recovery window, got %+v", decision)
	}
	if !decision.ShouldStore || decision.ShouldNotify {
		t.Fatalf("expected store-only recovery hold decision, got %+v", decision)
	}

	ticks = e.TickRule(rule, now.Add(10*time.Second))
	if len(ticks) != 1 || ticks[0].State != domain.AlertStateFiring || ticks[0].StateChanged {
		t.Fatalf("expected firing hold tick before recovery timeout, got %+v", ticks)
	}

	ticks = e.TickRule(rule, now.Add(11*time.Second))
	if len(ticks) != 1 || ticks[0].State != domain.AlertStateResolved {
		t.Fatalf("expected resolved after recovery timeout, got %+v", ticks)
	}
}

func TestEngineMissingHeartbeatHysteresisResetsOnFlap(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	rule := config.RuleConfig{
		Name:      "hb",
		AlertType: "missing_heartbeat",
		Match: config.RuleMatch{
			Type: []string{"event"},
			Var:  []string{"heartbeat"},
			Tags: map[string]config.StringList{"dc": {"dc1"}},
		},
		Pending: config.RulePending{Enabled: false},
		Raise:   config.RuleRaise{MissingSec: 5},
		Resolve: config.RuleResolve{HysteresisSec: 10},
	}
	e := New()

	numberValue := 1.0
	heartbeat := domain.Event{
		Type:   domain.EventTypeEvent,
		AggCnt: 1,
		Var:    "heartbeat",
		Tags:   map[string]string{"dc": "dc1"},
		Value:  domain.TypedValue{Type: "n", N: &numberValue},
	}
	_ = e.ProcessEvent(rule, heartbeat, "rule/hb/heartbeat/hash", now)

	ticks := e.TickRule(rule, now.Add(6*time.Second))
	if len(ticks) != 1 || ticks[0].State != domain.AlertStateFiring {
		t.Fatalf("expected firing from missing heartbeat, got %+v", ticks)
	}

	decision := e.ProcessEvent(rule, heartbeat, "rule/hb/heartbeat/hash", now.Add(7*time.Second))
	if decision.StateChanged || decision.State != domain.AlertStateFiring {
		t.Fatalf("expected firing hold during recovery window, got %+v", decision)
	}

	ticks = e.TickRule(rule, now.Add(13*time.Second))
	if len(ticks) != 1 || ticks[0].State != domain.AlertStateFiring || ticks[0].StateChanged {
		t.Fatalf("expected firing to continue after missing recurs, got %+v", ticks)
	}

	ticks = e.TickRule(rule, now.Add(18*time.Second))
	if len(ticks) != 1 || ticks[0].State != domain.AlertStateFiring || ticks[0].StateChanged {
		t.Fatalf("expected no resolved after flap reset, got %+v", ticks)
	}
}

func TestEngineCountTotalResolveHysteresis(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	rule := config.RuleConfig{
		Name:      "ct_h",
		AlertType: "count_total",
		Match: config.RuleMatch{
			Type: []string{"event"},
			Var:  []string{"errors"},
			Tags: map[string]config.StringList{"dc": {"dc1"}},
		},
		Pending: config.RulePending{Enabled: false},
		Raise:   config.RuleRaise{N: 1},
		Resolve: config.RuleResolve{SilenceSec: 5, HysteresisSec: 3},
	}
	e := New()

	numberValue := 1.0
	event := domain.Event{
		Type:   domain.EventTypeEvent,
		AggCnt: 1,
		Var:    "errors",
		Tags:   map[string]string{"dc": "dc1"},
		Value:  domain.TypedValue{Type: "n", N: &numberValue},
	}
	decision := e.ProcessEvent(rule, event, "rule/ct_h/errors/hash", now)
	if !decision.StateChanged || decision.State != domain.AlertStateFiring {
		t.Fatalf("expected firing transition, got %+v", decision)
	}

	ticks := e.TickRule(rule, now.Add(7*time.Second))
	if len(ticks) != 0 {
		t.Fatalf("expected no resolve before silence+hysteresis, got %+v", ticks)
	}

	ticks = e.TickRule(rule, now.Add(8*time.Second))
	if len(ticks) != 1 || ticks[0].State != domain.AlertStateResolved {
		t.Fatalf("expected resolved after silence+hysteresis, got %+v", ticks)
	}
}

func TestEngineCountWindowFireAndResolveBySilence(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	rule := config.RuleConfig{
		Name:      "cw",
		AlertType: "count_window",
		Match: config.RuleMatch{
			Type: []string{"event"},
			Var:  []string{"errors"},
			Tags: map[string]config.StringList{"dc": {"dc1"}},
		},
		Pending: config.RulePending{Enabled: false},
		Raise: config.RuleRaise{
			N:       3,
			TaggSec: 5,
		},
		Resolve: config.RuleResolve{SilenceSec: 4},
	}
	e := New()

	numberValue := 1.0
	event := domain.Event{
		Type:   domain.EventTypeEvent,
		AggCnt: 2,
		Var:    "errors",
		Tags:   map[string]string{"dc": "dc1"},
		Value:  domain.TypedValue{Type: "n", N: &numberValue},
	}

	decision := e.ProcessEvent(rule, event, "rule/cw/errors/hash", now)
	if !decision.Matched || decision.StateChanged {
		t.Fatalf("expected matched non-transition decision, got %+v", decision)
	}

	event.AggCnt = 1
	decision = e.ProcessEvent(rule, event, "rule/cw/errors/hash", now.Add(time.Second))
	if !decision.StateChanged || decision.State != domain.AlertStateFiring {
		t.Fatalf("expected firing transition, got %+v", decision)
	}

	ticks := e.TickRule(rule, now.Add(2*time.Second))
	if len(ticks) != 0 {
		t.Fatalf("expected no resolve before silence timeout, got %+v", ticks)
	}

	ticks = e.TickRule(rule, now.Add(6*time.Second))
	if len(ticks) != 1 || ticks[0].State != domain.AlertStateResolved {
		t.Fatalf("expected resolved by silence timeout, got %+v", ticks)
	}
}

func TestEngineCountWindowResolveHysteresis(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	rule := config.RuleConfig{
		Name:      "cw_h",
		AlertType: "count_window",
		Match: config.RuleMatch{
			Type: []string{"event"},
			Var:  []string{"errors"},
			Tags: map[string]config.StringList{"dc": {"dc1"}},
		},
		Pending: config.RulePending{Enabled: false},
		Raise: config.RuleRaise{
			N:       3,
			TaggSec: 5,
		},
		Resolve: config.RuleResolve{SilenceSec: 4, HysteresisSec: 3},
	}
	e := New()

	numberValue := 1.0
	event := domain.Event{
		Type:   domain.EventTypeEvent,
		AggCnt: 2,
		Var:    "errors",
		Tags:   map[string]string{"dc": "dc1"},
		Value:  domain.TypedValue{Type: "n", N: &numberValue},
	}

	decision := e.ProcessEvent(rule, event, "rule/cw_h/errors/hash", now)
	if !decision.Matched || decision.StateChanged {
		t.Fatalf("expected matched non-transition decision, got %+v", decision)
	}

	event.AggCnt = 1
	decision = e.ProcessEvent(rule, event, "rule/cw_h/errors/hash", now.Add(time.Second))
	if !decision.StateChanged || decision.State != domain.AlertStateFiring {
		t.Fatalf("expected firing transition, got %+v", decision)
	}

	ticks := e.TickRule(rule, now.Add(7*time.Second))
	if len(ticks) != 0 {
		t.Fatalf("expected no resolve before silence+hysteresis, got %+v", ticks)
	}

	ticks = e.TickRule(rule, now.Add(8*time.Second))
	if len(ticks) != 1 || ticks[0].State != domain.AlertStateResolved {
		t.Fatalf("expected resolved after silence+hysteresis, got %+v", ticks)
	}
}

func TestTickRuleWithFiringReturnsActiveFiringIDs(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	rule := config.RuleConfig{
		Name:      "ct_firing_ids",
		AlertType: "count_total",
		Match: config.RuleMatch{
			Type: []string{"event"},
			Var:  []string{"errors"},
			Tags: map[string]config.StringList{"dc": {"dc1"}},
		},
		Raise:   config.RuleRaise{N: 1},
		Resolve: config.RuleResolve{SilenceSec: 5},
	}
	e := New()

	numberValue := 1.0
	event := domain.Event{
		Type:   domain.EventTypeEvent,
		AggCnt: 1,
		Var:    "errors",
		Tags:   map[string]string{"dc": "dc1"},
		Value:  domain.TypedValue{Type: "n", N: &numberValue},
	}

	decision := e.ProcessEvent(rule, event, "rule/ct_firing_ids/errors/hash", now)
	if !decision.StateChanged || decision.State != domain.AlertStateFiring {
		t.Fatalf("expected firing transition, got %+v", decision)
	}

	ticks, firingIDs := e.TickRuleWithFiring(rule, now.Add(2*time.Second))
	if len(ticks) != 0 {
		t.Fatalf("expected no tick decisions before silence timeout, got %d", len(ticks))
	}
	if len(firingIDs) != 1 || firingIDs[0] != "rule/ct_firing_ids/errors/hash" {
		t.Fatalf("expected one firing alert id, got %+v", firingIDs)
	}

	ticks, firingIDs = e.TickRuleWithFiring(rule, now.Add(8*time.Second))
	if len(ticks) != 1 || ticks[0].State != domain.AlertStateResolved {
		t.Fatalf("expected one resolved decision, got %+v", ticks)
	}
	if len(firingIDs) != 0 {
		t.Fatalf("expected no firing ids after resolve, got %+v", firingIDs)
	}
}

func TestEngineCompactStatesByIdleTTL(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	rule := config.RuleConfig{
		Name:      "ct",
		AlertType: "count_total",
		Match: config.RuleMatch{
			Type: []string{"event"},
			Var:  []string{"errors"},
			Tags: map[string]config.StringList{"dc": {"dc1"}},
		},
		Raise: config.RuleRaise{N: 100},
	}
	e := New()

	numberValue := 1.0
	event := domain.Event{
		Type:   domain.EventTypeEvent,
		AggCnt: 1,
		Var:    "errors",
		Tags:   map[string]string{"dc": "dc1"},
		Value:  domain.TypedValue{Type: "n", N: &numberValue},
	}

	_ = e.ProcessEvent(rule, event, "rule/ct/errors/old", now)
	_ = e.ProcessEvent(rule, event, "rule/ct/errors/new", now.Add(9*time.Second))

	evicted := e.CompactStates(now.Add(10*time.Second), 5*time.Second, 0)
	if evicted != 1 {
		t.Fatalf("expected 1 evicted state, got %d", evicted)
	}
	if _, ok := e.GetStateSnapshot("rule/ct/errors/old"); ok {
		t.Fatalf("expected old state to be evicted")
	}
	if _, ok := e.GetStateSnapshot("rule/ct/errors/new"); !ok {
		t.Fatalf("expected recent state to stay")
	}
}

func TestEngineCompactStatesByMaxCap(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	rule := config.RuleConfig{
		Name:      "ct",
		AlertType: "count_total",
		Match: config.RuleMatch{
			Type: []string{"event"},
			Var:  []string{"errors"},
			Tags: map[string]config.StringList{"dc": {"dc1"}},
		},
		Raise: config.RuleRaise{N: 100},
	}
	e := New()

	numberValue := 1.0
	event := domain.Event{
		Type:   domain.EventTypeEvent,
		AggCnt: 1,
		Var:    "errors",
		Tags:   map[string]string{"dc": "dc1"},
		Value:  domain.TypedValue{Type: "n", N: &numberValue},
	}

	_ = e.ProcessEvent(rule, event, "rule/ct/errors/a", now)
	_ = e.ProcessEvent(rule, event, "rule/ct/errors/b", now.Add(time.Second))
	_ = e.ProcessEvent(rule, event, "rule/ct/errors/c", now.Add(2*time.Second))

	evicted := e.CompactStates(now.Add(3*time.Second), 0, 2)
	if evicted != 1 {
		t.Fatalf("expected 1 evicted state, got %d", evicted)
	}
	if _, ok := e.GetStateSnapshot("rule/ct/errors/a"); ok {
		t.Fatalf("expected oldest state to be evicted by max cap")
	}
	if _, ok := e.GetStateSnapshot("rule/ct/errors/b"); !ok {
		t.Fatalf("expected state b to stay")
	}
	if _, ok := e.GetStateSnapshot("rule/ct/errors/c"); !ok {
		t.Fatalf("expected state c to stay")
	}
}
