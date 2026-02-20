package engine

import (
	"strings"
	"testing"

	"alerting/internal/config"
	"alerting/internal/domain"
)

func TestBuildAlertKeyDeterministic(t *testing.T) {
	t.Parallel()

	rule := config.RuleConfig{
		Name: "rule-a",
		Key:  config.RuleKey{FromTags: []string{"dc", "service"}},
	}
	eventA := domain.Event{Var: "errors", Tags: map[string]string{"service": "api", "dc": "dc1"}}
	eventB := domain.Event{Var: "errors", Tags: map[string]string{"dc": "dc1", "service": "api"}}

	keyA, err := BuildAlertKey(rule, eventA)
	if err != nil {
		t.Fatalf("build keyA: %v", err)
	}
	keyB, err := BuildAlertKey(rule, eventB)
	if err != nil {
		t.Fatalf("build keyB: %v", err)
	}

	if keyA != keyB {
		t.Fatalf("expected deterministic key, got %q and %q", keyA, keyB)
	}
	if !strings.HasPrefix(keyA, "rule/rule-a/errors/") {
		t.Fatalf("unexpected key format %q", keyA)
	}
}

func TestBuildAlertKeyMissingTag(t *testing.T) {
	t.Parallel()

	rule := config.RuleConfig{Name: "rule-a", Key: config.RuleKey{FromTags: []string{"dc", "service"}}}
	event := domain.Event{Var: "errors", Tags: map[string]string{"dc": "dc1"}}
	if _, err := BuildAlertKey(rule, event); err == nil {
		t.Fatalf("expected missing tag error")
	}
}
