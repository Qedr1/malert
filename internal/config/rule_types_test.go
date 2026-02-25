package config

import "testing"

func TestValidateRuleCountWindowValid(t *testing.T) {
	t.Parallel()

	rule := baseRuleForTypeTests()
	rule.AlertType = "count_window"
	rule.Raise.N = 3
	rule.Raise.TaggSec = 10
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
	rule.Raise.TaggSec = 0

	if err := validateRule(rule); err != nil {
		t.Fatalf("validateRule(missing_heartbeat) failed: %v", err)
	}
}

func TestValidateRuleCountWindowRejectsMissingSec(t *testing.T) {
	t.Parallel()

	rule := baseRuleForTypeTests()
	rule.AlertType = "count_window"
	rule.Raise.N = 1
	rule.Raise.TaggSec = 5
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
