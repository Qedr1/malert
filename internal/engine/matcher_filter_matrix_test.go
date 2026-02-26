package engine

import (
	"testing"

	"alerting/internal/config"
	"alerting/internal/domain"
)

func TestMatchEventFilterMatrix(t *testing.T) {
	t.Parallel()

	floatPtr := func(v float64) *float64 { return &v }
	stringPtr := func(v string) *string { return &v }
	boolPtr := func(v bool) *bool { return &v }

	newRule := func() config.RuleConfig {
		return config.RuleConfig{
			Match: config.RuleMatch{
				Type: []string{"event"},
				Var:  []string{"errors"},
				Tags: map[string]config.StringList{
					"dc":      {"dc1"},
					"service": {"api"},
				},
			},
		}
	}

	newEvent := func() domain.Event {
		numeric := 10.0
		return domain.Event{
			Type: domain.EventTypeEvent,
			Var:  "errors",
			Tags: map[string]string{
				"dc":      "DC1",
				"service": "api",
			},
			Value: domain.TypedValue{
				Type: "n",
				N:    &numeric,
			},
		}
	}

	type testCase struct {
		name        string
		mutateRule  func(*config.RuleConfig)
		mutateEvent func(*domain.Event)
		want        bool
	}

	cases := []testCase{
		{
			name: "base filters match without value predicate",
			want: true,
		},
		{
			name: "type filter blocks unmatched event type",
			mutateEvent: func(event *domain.Event) {
				event.Type = domain.EventTypeAgg
			},
			want: false,
		},
		{
			name: "var filter blocks unmatched variable name",
			mutateEvent: func(event *domain.Event) {
				event.Var = "latency"
			},
			want: false,
		},
		{
			name: "tag filter blocks mismatched tag value",
			mutateEvent: func(event *domain.Event) {
				event.Tags["dc"] = "dc2"
			},
			want: false,
		},
		{
			name: "tag filter blocks missing tag",
			mutateEvent: func(event *domain.Event) {
				delete(event.Tags, "service")
			},
			want: false,
		},
		{
			name: "value n == pass",
			mutateRule: func(rule *config.RuleConfig) {
				rule.Match.Value = &config.RuleValuePredicate{
					Type: "n",
					Op:   "==",
					N:    floatPtr(10),
				}
			},
			want: true,
		},
		{
			name: "value n == fail",
			mutateRule: func(rule *config.RuleConfig) {
				rule.Match.Value = &config.RuleValuePredicate{
					Type: "n",
					Op:   "==",
					N:    floatPtr(11),
				}
			},
			want: false,
		},
		{
			name: "value n != pass",
			mutateRule: func(rule *config.RuleConfig) {
				rule.Match.Value = &config.RuleValuePredicate{
					Type: "n",
					Op:   "!=",
					N:    floatPtr(9),
				}
			},
			want: true,
		},
		{
			name: "value n != fail",
			mutateRule: func(rule *config.RuleConfig) {
				rule.Match.Value = &config.RuleValuePredicate{
					Type: "n",
					Op:   "!=",
					N:    floatPtr(10),
				}
			},
			want: false,
		},
		{
			name: "value n > pass",
			mutateRule: func(rule *config.RuleConfig) {
				rule.Match.Value = &config.RuleValuePredicate{
					Type: "n",
					Op:   ">",
					N:    floatPtr(9),
				}
			},
			want: true,
		},
		{
			name: "value n > fail",
			mutateRule: func(rule *config.RuleConfig) {
				rule.Match.Value = &config.RuleValuePredicate{
					Type: "n",
					Op:   ">",
					N:    floatPtr(10),
				}
			},
			want: false,
		},
		{
			name: "value n >= pass",
			mutateRule: func(rule *config.RuleConfig) {
				rule.Match.Value = &config.RuleValuePredicate{
					Type: "n",
					Op:   ">=",
					N:    floatPtr(10),
				}
			},
			want: true,
		},
		{
			name: "value n >= fail",
			mutateRule: func(rule *config.RuleConfig) {
				rule.Match.Value = &config.RuleValuePredicate{
					Type: "n",
					Op:   ">=",
					N:    floatPtr(11),
				}
			},
			want: false,
		},
		{
			name: "value n < pass",
			mutateRule: func(rule *config.RuleConfig) {
				rule.Match.Value = &config.RuleValuePredicate{
					Type: "n",
					Op:   "<",
					N:    floatPtr(11),
				}
			},
			want: true,
		},
		{
			name: "value n < fail",
			mutateRule: func(rule *config.RuleConfig) {
				rule.Match.Value = &config.RuleValuePredicate{
					Type: "n",
					Op:   "<",
					N:    floatPtr(10),
				}
			},
			want: false,
		},
		{
			name: "value n <= pass",
			mutateRule: func(rule *config.RuleConfig) {
				rule.Match.Value = &config.RuleValuePredicate{
					Type: "n",
					Op:   "<=",
					N:    floatPtr(10),
				}
			},
			want: true,
		},
		{
			name: "value n <= fail",
			mutateRule: func(rule *config.RuleConfig) {
				rule.Match.Value = &config.RuleValuePredicate{
					Type: "n",
					Op:   "<=",
					N:    floatPtr(9),
				}
			},
			want: false,
		},
		{
			name: "value s == pass",
			mutateRule: func(rule *config.RuleConfig) {
				rule.Match.Value = &config.RuleValuePredicate{
					Type: "s",
					Op:   "==",
					S:    stringPtr("error"),
				}
			},
			mutateEvent: func(event *domain.Event) {
				event.Value = domain.TypedValue{Type: "s", S: stringPtr("ERROR")}
			},
			want: true,
		},
		{
			name: "value s == fail",
			mutateRule: func(rule *config.RuleConfig) {
				rule.Match.Value = &config.RuleValuePredicate{
					Type: "s",
					Op:   "==",
					S:    stringPtr("error"),
				}
			},
			mutateEvent: func(event *domain.Event) {
				event.Value = domain.TypedValue{Type: "s", S: stringPtr("warn")}
			},
			want: false,
		},
		{
			name: "value s != pass",
			mutateRule: func(rule *config.RuleConfig) {
				rule.Match.Value = &config.RuleValuePredicate{
					Type: "s",
					Op:   "!=",
					S:    stringPtr("error"),
				}
			},
			mutateEvent: func(event *domain.Event) {
				event.Value = domain.TypedValue{Type: "s", S: stringPtr("warn")}
			},
			want: true,
		},
		{
			name: "value s != fail",
			mutateRule: func(rule *config.RuleConfig) {
				rule.Match.Value = &config.RuleValuePredicate{
					Type: "s",
					Op:   "!=",
					S:    stringPtr("error"),
				}
			},
			mutateEvent: func(event *domain.Event) {
				event.Value = domain.TypedValue{Type: "s", S: stringPtr("ERROR")}
			},
			want: false,
		},
		{
			name: "value s in pass",
			mutateRule: func(rule *config.RuleConfig) {
				rule.Match.Value = &config.RuleValuePredicate{
					Type: "s",
					Op:   "in",
					In:   []string{"error", "warn"},
				}
			},
			mutateEvent: func(event *domain.Event) {
				event.Value = domain.TypedValue{Type: "s", S: stringPtr("WARN")}
			},
			want: true,
		},
		{
			name: "value s in fail",
			mutateRule: func(rule *config.RuleConfig) {
				rule.Match.Value = &config.RuleValuePredicate{
					Type: "s",
					Op:   "in",
					In:   []string{"error", "warn"},
				}
			},
			mutateEvent: func(event *domain.Event) {
				event.Value = domain.TypedValue{Type: "s", S: stringPtr("info")}
			},
			want: false,
		},
		{
			name: "value s prefix pass",
			mutateRule: func(rule *config.RuleConfig) {
				rule.Match.Value = &config.RuleValuePredicate{
					Type: "s",
					Op:   "prefix",
					S:    stringPtr("api-"),
				}
			},
			mutateEvent: func(event *domain.Event) {
				event.Value = domain.TypedValue{Type: "s", S: stringPtr("API-01")}
			},
			want: true,
		},
		{
			name: "value s prefix fail",
			mutateRule: func(rule *config.RuleConfig) {
				rule.Match.Value = &config.RuleValuePredicate{
					Type: "s",
					Op:   "prefix",
					S:    stringPtr("api-"),
				}
			},
			mutateEvent: func(event *domain.Event) {
				event.Value = domain.TypedValue{Type: "s", S: stringPtr("db-01")}
			},
			want: false,
		},
		{
			name: "value s match pass",
			mutateRule: func(rule *config.RuleConfig) {
				rule.Match.Value = &config.RuleValuePredicate{
					Type: "s",
					Op:   "match",
					S:    stringPtr(`(?i)^api-[0-9]+$`),
				}
			},
			mutateEvent: func(event *domain.Event) {
				event.Value = domain.TypedValue{Type: "s", S: stringPtr("API-42")}
			},
			want: true,
		},
		{
			name: "value s match fail",
			mutateRule: func(rule *config.RuleConfig) {
				rule.Match.Value = &config.RuleValuePredicate{
					Type: "s",
					Op:   "match",
					S:    stringPtr(`(?i)^api-[0-9]+$`),
				}
			},
			mutateEvent: func(event *domain.Event) {
				event.Value = domain.TypedValue{Type: "s", S: stringPtr("db-42")}
			},
			want: false,
		},
		{
			name: "value s wildcard pass",
			mutateRule: func(rule *config.RuleConfig) {
				rule.Match.Value = &config.RuleValuePredicate{
					Type: "s",
					Op:   "*",
					S:    stringPtr("api-?"),
				}
			},
			mutateEvent: func(event *domain.Event) {
				event.Value = domain.TypedValue{Type: "s", S: stringPtr("API-1")}
			},
			want: true,
		},
		{
			name: "value s wildcard fail",
			mutateRule: func(rule *config.RuleConfig) {
				rule.Match.Value = &config.RuleValuePredicate{
					Type: "s",
					Op:   "*",
					S:    stringPtr("api-?"),
				}
			},
			mutateEvent: func(event *domain.Event) {
				event.Value = domain.TypedValue{Type: "s", S: stringPtr("api-12")}
			},
			want: false,
		},
		{
			name: "value b == pass",
			mutateRule: func(rule *config.RuleConfig) {
				rule.Match.Value = &config.RuleValuePredicate{
					Type: "b",
					Op:   "==",
					B:    boolPtr(true),
				}
			},
			mutateEvent: func(event *domain.Event) {
				event.Value = domain.TypedValue{Type: "b", B: boolPtr(true)}
			},
			want: true,
		},
		{
			name: "value b == fail",
			mutateRule: func(rule *config.RuleConfig) {
				rule.Match.Value = &config.RuleValuePredicate{
					Type: "b",
					Op:   "==",
					B:    boolPtr(true),
				}
			},
			mutateEvent: func(event *domain.Event) {
				event.Value = domain.TypedValue{Type: "b", B: boolPtr(false)}
			},
			want: false,
		},
		{
			name: "value b != pass",
			mutateRule: func(rule *config.RuleConfig) {
				rule.Match.Value = &config.RuleValuePredicate{
					Type: "b",
					Op:   "!=",
					B:    boolPtr(true),
				}
			},
			mutateEvent: func(event *domain.Event) {
				event.Value = domain.TypedValue{Type: "b", B: boolPtr(false)}
			},
			want: true,
		},
		{
			name: "value b != fail",
			mutateRule: func(rule *config.RuleConfig) {
				rule.Match.Value = &config.RuleValuePredicate{
					Type: "b",
					Op:   "!=",
					B:    boolPtr(true),
				}
			},
			mutateEvent: func(event *domain.Event) {
				event.Value = domain.TypedValue{Type: "b", B: boolPtr(true)}
			},
			want: false,
		},
		{
			name: "value predicate type mismatch returns false",
			mutateRule: func(rule *config.RuleConfig) {
				rule.Match.Value = &config.RuleValuePredicate{
					Type: "n",
					Op:   ">=",
					N:    floatPtr(1),
				}
			},
			mutateEvent: func(event *domain.Event) {
				event.Value = domain.TypedValue{Type: "s", S: stringPtr("10")}
			},
			want: false,
		},
	}

	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			rule := newRule()
			event := newEvent()
			if testCase.mutateRule != nil {
				testCase.mutateRule(&rule)
			}
			if testCase.mutateEvent != nil {
				testCase.mutateEvent(&event)
			}
			got := MatchEvent(rule, event)
			if got != testCase.want {
				t.Fatalf("MatchEvent()=%t, want %t", got, testCase.want)
			}
		})
	}
}
