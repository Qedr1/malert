package engine

import (
	"testing"

	"alerting/internal/config"
	"alerting/internal/domain"
)

func TestMatchEventStringWildcardCaseInsensitive(t *testing.T) {
	t.Parallel()

	pattern := "api-*"
	rule := config.RuleConfig{
		Match: config.RuleMatch{
			Type: []string{"event"},
			Var:  []string{"status"},
			Tags: map[string]config.StringList{"service": {"gateway"}},
			Value: &config.RuleValuePredicate{
				Type: "s",
				Op:   "*",
				S:    &pattern,
			},
		},
	}

	value := "API-01"
	event := domain.Event{
		Type:  domain.EventTypeEvent,
		Var:   "status",
		Tags:  map[string]string{"service": "gateway"},
		Value: domain.TypedValue{Type: "s", S: &value},
	}
	if !MatchEvent(rule, event) {
		t.Fatalf("expected wildcard match")
	}
}

func TestMatchEventTagAllowOnly(t *testing.T) {
	t.Parallel()

	rule := config.RuleConfig{
		Match: config.RuleMatch{
			Type: []string{"event"},
			Var:  []string{"errors"},
			Tags: map[string]config.StringList{"dc": {"dc1"}},
		},
	}
	event := domain.Event{Type: domain.EventTypeEvent, Var: "errors", Tags: map[string]string{"dc": "dc2"}}
	if MatchEvent(rule, event) {
		t.Fatalf("expected allow-only mismatch")
	}
}
