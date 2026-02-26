package config

import (
	"testing"
	"time"
)

func TestValidateRuleCountWindowValid(t *testing.T) {
	t.Parallel()

	rule := baseRuleForTypeTests()
	rule.AlertType = "count_window"
	rule.Raise.N = 3
	rule.Raise.WindowSec = 10
	rule.Resolve.SilenceSec = 30

	if err := validateRule(rule); err != nil {
		t.Fatalf("validateRule(count_window) failed: %v", err)
	}
}

func TestValidateRuleMissingHeartbeatValid(t *testing.T) {
	t.Parallel()

	rule := baseRuleForTypeTests()
	rule.AlertType = "missing_heartbeat"
	rule.Raise.MissingSec = 5
	rule.Resolve.SilenceSec = 0
	rule.Resolve.HysteresisSec = 30
	rule.Raise.N = 0
	rule.Raise.WindowSec = 0

	if err := validateRule(rule); err != nil {
		t.Fatalf("validateRule(missing_heartbeat) failed: %v", err)
	}
}

func TestValidateRuleCountWindowRejectsMissingSec(t *testing.T) {
	t.Parallel()

	rule := baseRuleForTypeTests()
	rule.AlertType = "count_window"
	rule.Raise.N = 1
	rule.Raise.WindowSec = 5
	rule.Raise.MissingSec = 1

	if err := validateRule(rule); err == nil {
		t.Fatalf("expected error for count_window with raise.missing_sec")
	}
}

func TestValidateRuleCountTotalAcceptsHysteresisSec(t *testing.T) {
	t.Parallel()

	rule := baseRuleForTypeTests()
	rule.AlertType = "count_total"
	rule.Raise.N = 1
	rule.Resolve.SilenceSec = 5
	rule.Resolve.HysteresisSec = 1

	if err := validateRule(rule); err != nil {
		t.Fatalf("expected valid count_total with resolve.hysteresis_sec, got: %v", err)
	}
}

func TestValidateRuleMissingHeartbeatRejectsResolveAndRaiseN(t *testing.T) {
	t.Parallel()

	rule := baseRuleForTypeTests()
	rule.AlertType = "missing_heartbeat"
	rule.Raise.MissingSec = 5
	rule.Raise.N = 1
	rule.Resolve.SilenceSec = 1

	if err := validateRule(rule); err == nil {
		t.Fatalf("expected error for missing_heartbeat with raise.n/resolve.silence_sec")
	}
}

func TestValidateRuleMissingHeartbeatRejectsNegativeHysteresisSec(t *testing.T) {
	t.Parallel()

	rule := baseRuleForTypeTests()
	rule.AlertType = "missing_heartbeat"
	rule.Raise.MissingSec = 5
	rule.Resolve.HysteresisSec = -1

	if err := validateRule(rule); err == nil {
		t.Fatalf("expected error for missing_heartbeat with negative resolve.hysteresis_sec")
	}
}

func TestResolveTimeoutMissingHeartbeatIncludesHysteresisSec(t *testing.T) {
	t.Parallel()

	rule := baseRuleForTypeTests()
	rule.AlertType = "missing_heartbeat"
	rule.Raise.MissingSec = 5
	rule.Resolve.HysteresisSec = 7

	timeout := ResolveTimeout(rule)
	if timeout.Seconds() != 12 {
		t.Fatalf("expected timeout=12s, got %s", timeout)
	}
}

func TestResolveTimeoutCountTotalIncludesHysteresisSec(t *testing.T) {
	t.Parallel()

	rule := baseRuleForTypeTests()
	rule.AlertType = "count_total"
	rule.Raise.N = 1
	rule.Resolve.SilenceSec = 5
	rule.Resolve.HysteresisSec = 7

	timeout := ResolveTimeout(rule)
	if timeout.Seconds() != 12 {
		t.Fatalf("expected timeout=12s, got %s", timeout)
	}
}

func TestValidateRuleNotifyOffValid(t *testing.T) {
	t.Parallel()

	rule := baseRuleForTypeTests()
	rule.AlertType = "count_total"
	rule.Raise.N = 1
	rule.Resolve.SilenceSec = 5
	rule.Notify.Off = []RuleNotifyOff{
		{
			Days: []string{"mon", "tue", "wed", "thu", "fri"},
			From: "22:00",
			To:   "08:00",
		},
	}

	if err := validateRule(rule); err != nil {
		t.Fatalf("expected valid notify.off, got: %v", err)
	}
}

func TestValidateRuleNotifyOffRejectsInvalidDay(t *testing.T) {
	t.Parallel()

	rule := baseRuleForTypeTests()
	rule.AlertType = "count_total"
	rule.Raise.N = 1
	rule.Resolve.SilenceSec = 5
	rule.Notify.Off = []RuleNotifyOff{
		{
			Days: []string{"monday"},
			From: "22:00",
			To:   "08:00",
		},
	}

	if err := validateRule(rule); err == nil {
		t.Fatalf("expected validation error for invalid notify.off day token")
	}
}

func TestValidateRuleNotifyOffRejectsInvalidTime(t *testing.T) {
	t.Parallel()

	rule := baseRuleForTypeTests()
	rule.AlertType = "count_total"
	rule.Raise.N = 1
	rule.Resolve.SilenceSec = 5
	rule.Notify.Off = []RuleNotifyOff{
		{
			Days: []string{"mon"},
			From: "25:00",
			To:   "08:00",
		},
	}

	if err := validateRule(rule); err == nil {
		t.Fatalf("expected validation error for invalid notify.off time")
	}
}

func TestValidateRuleNotifyOffRejectsZeroLengthWindow(t *testing.T) {
	t.Parallel()

	rule := baseRuleForTypeTests()
	rule.AlertType = "count_total"
	rule.Raise.N = 1
	rule.Resolve.SilenceSec = 5
	rule.Notify.Off = []RuleNotifyOff{
		{
			Days: []string{"mon"},
			From: "10:00",
			To:   "10:00",
		},
	}

	if err := validateRule(rule); err == nil {
		t.Fatalf("expected validation error for zero-length notify.off interval")
	}
}

func TestNotifyOffActiveAtOvernightUsesStartDay(t *testing.T) {
	t.Parallel()

	location := time.FixedZone("UTC+3", 3*60*60)
	windows := []RuleNotifyOff{
		{
			Days: []string{"mon"},
			From: "22:00",
			To:   "08:00",
		},
	}

	checks := []struct {
		name string
		at   time.Time
		want bool
	}{
		{
			name: "monday late evening is off",
			at:   time.Date(2026, 3, 2, 23, 0, 0, 0, location), // Monday
			want: true,
		},
		{
			name: "tuesday early morning belongs to monday window",
			at:   time.Date(2026, 3, 3, 7, 30, 0, 0, location), // Tuesday
			want: true,
		},
		{
			name: "monday early morning does not belong to monday start",
			at:   time.Date(2026, 3, 2, 7, 30, 0, 0, location), // Monday
			want: false,
		},
	}

	for _, check := range checks {
		check := check
		t.Run(check.name, func(t *testing.T) {
			active, err := NotifyOffActiveAt(windows, check.at, location)
			if err != nil {
				t.Fatalf("NotifyOffActiveAt error: %v", err)
			}
			if active != check.want {
				t.Fatalf("NotifyOffActiveAt=%t, want %t", active, check.want)
			}
		})
	}
}

func baseRuleForTypeTests() RuleConfig {
	return RuleConfig{
		Name: "type_rule",
		Match: RuleMatch{
			Type: []string{"event"},
			Var:  []string{"errors"},
			Tags: map[string]StringList{
				"dc":      {"dc1"},
				"service": {"api"},
			},
		},
		Key: RuleKey{
			FromTags: []string{"dc", "service", "host"},
		},
		Pending: RulePending{
			Enabled:  false,
			DelaySec: 300,
		},
		Notify: RuleNotify{
			Route: []RuleNotifyRoute{
				{
					Name:     "http_route",
					Channel:  "http",
					Template: "default",
					Mode:     NotifyRouteModeHistory,
				},
			},
		},
	}
}
