package engine

import (
	"fmt"
	"regexp"
	"strings"

	"alerting/internal/config"
	"alerting/internal/domain"
)

// MatchEvent checks whether rule predicates match one event.
// Params: rule selector and incoming event.
// Returns: true when event is applicable to rule.
func MatchEvent(rule config.RuleConfig, event domain.Event) bool {
	if !containsString(rule.Match.Type, string(event.Type)) {
		return false
	}
	if !containsString(rule.Match.Var, event.Var) {
		return false
	}

	for tagKey, allowed := range rule.Match.Tags {
		eventValue, ok := event.Tags[tagKey]
		if !ok {
			return false
		}
		if !containsStringInsensitive(allowed, eventValue) {
			return false
		}
	}

	if rule.Match.Value == nil {
		return true
	}
	ok, err := matchValue(*rule.Match.Value, event.Value)
	if err != nil {
		return false
	}
	return ok
}

// ValidateKeyTags ensures all key.from_tags exist in event.
// Params: rule key requirements and event tags.
// Returns: validation error for missing required key tags.
func ValidateKeyTags(rule config.RuleConfig, event domain.Event) error {
	for _, key := range rule.Key.FromTags {
		if _, ok := event.Tags[key]; !ok {
			return fmt.Errorf("missing key tag %q", key)
		}
	}
	return nil
}

// matchValue evaluates typed value predicate against event value.
// Params: predicate from rule and typed event value.
// Returns: predicate result or evaluation error.
func matchValue(predicate config.RuleValuePredicate, value domain.TypedValue) (bool, error) {
	if predicate.Type != value.Type {
		return false, nil
	}
	if !config.IsSupportedValuePredicateOp(predicate.Type, predicate.Op) {
		return false, fmt.Errorf("unsupported value predicate %s/%s", predicate.Type, predicate.Op)
	}

	switch predicate.Type {
	case "n":
		lhs := *value.N
		rhs := *predicate.N
		switch predicate.Op {
		case "==":
			return lhs == rhs, nil
		case "!=":
			return lhs != rhs, nil
		case ">":
			return lhs > rhs, nil
		case ">=":
			return lhs >= rhs, nil
		case "<":
			return lhs < rhs, nil
		case "<=":
			return lhs <= rhs, nil
		}
	case "s":
		lhs := *value.S
		switch predicate.Op {
		case "==":
			return strings.EqualFold(lhs, *predicate.S), nil
		case "!=":
			return !strings.EqualFold(lhs, *predicate.S), nil
		case "prefix":
			return strings.HasPrefix(strings.ToLower(lhs), strings.ToLower(*predicate.S)), nil
		case "in":
			return containsStringInsensitive(predicate.In, lhs), nil
		case "match":
			if predicate.CompiledMatchRE != nil {
				return predicate.CompiledMatchRE.MatchString(lhs), nil
			}
			compiled, err := regexp.Compile(*predicate.S)
			if err != nil {
				return false, err
			}
			return compiled.MatchString(lhs), nil
		case "*":
			valueLower := strings.ToLower(lhs)
			if predicate.CompiledWildcardRE != nil {
				return predicate.CompiledWildcardRE.MatchString(valueLower), nil
			}
			compiled, err := config.CompileWildcardPattern(*predicate.S)
			if err != nil {
				return false, err
			}
			return compiled.MatchString(valueLower), nil
		}
	case "b":
		lhs := *value.B
		rhs := *predicate.B
		switch predicate.Op {
		case "==":
			return lhs == rhs, nil
		case "!=":
			return lhs != rhs, nil
		}
	}
	return false, fmt.Errorf("unsupported value predicate %s/%s", predicate.Type, predicate.Op)
}

// containsString checks case-sensitive membership.
// Params: haystack string list and expected value.
// Returns: true when value exists in list.
func containsString(values []string, expected string) bool {
	for _, v := range values {
		if v == expected {
			return true
		}
	}
	return false
}

// containsStringInsensitive checks case-insensitive membership.
// Params: haystack string list and expected value.
// Returns: true when case-insensitive match exists.
func containsStringInsensitive(values []string, expected string) bool {
	for _, v := range values {
		if strings.EqualFold(v, expected) {
			return true
		}
	}
	return false
}
