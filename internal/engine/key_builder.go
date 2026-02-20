package engine

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"alerting/internal/config"
	"alerting/internal/domain"
)

// BuildAlertKey builds deterministic alert_id in required namespace.
// Params: rule and event after successful match.
// Returns: formatted alert ID or error on missing required key tags.
func BuildAlertKey(rule config.RuleConfig, event domain.Event) (string, error) {
	if strings.TrimSpace(rule.Name) == "" {
		return "", errors.New("rule name is required")
	}

	sortedTags := sortedKeyTags(rule.Key.FromTags)
	capacity := 0
	for _, tag := range sortedTags {
		value, ok := event.Tags[tag]
		if !ok {
			return "", fmt.Errorf("required key tag %q is missing", tag)
		}
		capacity += len(tag) + 1 + len(value)
	}
	if len(sortedTags) > 1 {
		capacity += len(sortedTags) - 1
	}

	canonical := make([]byte, 0, capacity)
	for index, tag := range sortedTags {
		value, ok := event.Tags[tag]
		if !ok {
			return "", fmt.Errorf("required key tag %q is missing", tag)
		}
		if index > 0 {
			canonical = append(canonical, '\n')
		}
		canonical = append(canonical, tag...)
		canonical = append(canonical, '=')
		canonical = append(canonical, value...)
	}
	digest := sha1.Sum(canonical)
	var hashValue [sha1.Size * 2]byte
	hex.Encode(hashValue[:], digest[:])

	ruleName := sanitize(rule.Name)
	varName := sanitize(event.Var)
	var builder strings.Builder
	builder.Grow(len("rule/") + len(ruleName) + len(varName) + len(hashValue) + 2)
	builder.WriteString("rule/")
	builder.WriteString(ruleName)
	builder.WriteByte('/')
	builder.WriteString(varName)
	builder.WriteByte('/')
	builder.Write(hashValue[:])
	return builder.String(), nil
}

// sortedKeyTags returns stable key-tag order, copying only when reordering is needed.
// Params: rule key tags from config.
// Returns: sorted tag-name slice.
func sortedKeyTags(tags []string) []string {
	if len(tags) <= 1 || sort.StringsAreSorted(tags) {
		return tags
	}
	sortedTags := append([]string(nil), tags...)
	sort.Strings(sortedTags)
	return sortedTags
}

// sanitize converts key path fragments into stable bucket-safe tokens.
// Params: raw value with possible separators.
// Returns: sanitized string with unsupported chars replaced by underscore.
func sanitize(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "_"
	}

	var b strings.Builder
	b.Grow(len(trimmed))
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + 32)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}
