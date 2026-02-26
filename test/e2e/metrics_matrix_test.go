package e2e

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const e2eMetricVar = "errors"

type e2eMetricCase struct {
	Name      string
	AlertType string
}

type e2eRuleOptions struct {
	RaiseN            int
	WindowSec         int
	ResolveSilenceSec int
	MissingSec        int
	PendingEnabled    bool
	PendingDelaySec   int
}

// allE2EMetricCases returns all supported alert metric groups for e2e scenarios.
// Params: none.
// Returns: deterministic list of metric cases.
func allE2EMetricCases() []e2eMetricCase {
	return []e2eMetricCase{
		{Name: "count_total", AlertType: "count_total"},
		{Name: "count_window", AlertType: "count_window"},
		{Name: "missing_heartbeat", AlertType: "missing_heartbeat"},
	}
}

// e2eFunctionalMetricCases returns minimal representative metric set
// for transport/lifecycle e2e scenarios that do not verify rule math.
// Params: none.
// Returns: reduced metric list for functional e2e coverage.
func e2eFunctionalMetricCases() []e2eMetricCase {
	return []e2eMetricCase{
		{Name: "count_total", AlertType: "count_total"},
	}
}

// defaultE2ERuleOptions returns baseline thresholds per metric type.
// Params: metric group.
// Returns: normalized rule options.
func defaultE2ERuleOptions(metric e2eMetricCase) e2eRuleOptions {
	switch metric.AlertType {
	case "count_total":
		return e2eRuleOptions{
			RaiseN:            1,
			ResolveSilenceSec: 1,
			PendingEnabled:    false,
			PendingDelaySec:   300,
		}
	case "count_window":
		return e2eRuleOptions{
			RaiseN:            1,
			WindowSec:         1,
			ResolveSilenceSec: 1,
			PendingEnabled:    false,
			PendingDelaySec:   300,
		}
	default:
		return e2eRuleOptions{
			MissingSec:      2,
			PendingEnabled:  false,
			PendingDelaySec: 300,
		}
	}
}

// buildRuleTOML renders one rule section for selected metric type.
// Params: rule name, metric case, matched var, tuned options, and route TOML block.
// Returns: rule TOML snippet.
func buildRuleTOML(ruleName string, metric e2eMetricCase, metricVar string, opts e2eRuleOptions, routeTOML string) string {
	if strings.TrimSpace(metricVar) == "" {
		metricVar = e2eMetricVar
	}
	if opts.PendingDelaySec <= 0 {
		opts.PendingDelaySec = 300
	}

	var builder strings.Builder
	builder.WriteString(fmt.Sprintf(`
[rule.%[1]s]
alert_type = "%[2]s"

[rule.%[1]s.match]
type = ["event"]
var = ["%[3]s"]
tags = { dc = ["dc1"], service = ["api"] }

[rule.%[1]s.key]
from_tags = ["dc", "service", "host"]
`, ruleName, metric.AlertType, metricVar))

	switch metric.AlertType {
	case "count_total":
		if opts.RaiseN <= 0 {
			opts.RaiseN = 1
		}
		builder.WriteString(fmt.Sprintf(`
[rule.%[1]s.raise]
n = %[2]d

[rule.%[1]s.resolve]
silence_sec = %[3]d
`, ruleName, opts.RaiseN, opts.ResolveSilenceSec))
	case "count_window":
		if opts.RaiseN <= 0 {
			opts.RaiseN = 1
		}
		if opts.WindowSec <= 0 {
			opts.WindowSec = 1
		}
		builder.WriteString(fmt.Sprintf(`
[rule.%[1]s.raise]
n = %[2]d
window_sec = %[3]d

[rule.%[1]s.resolve]
silence_sec = %[4]d
`, ruleName, opts.RaiseN, opts.WindowSec, opts.ResolveSilenceSec))
	default:
		if opts.MissingSec <= 0 {
			opts.MissingSec = 2
		}
		builder.WriteString(fmt.Sprintf(`
[rule.%[1]s.raise]
missing_sec = %[2]d
`, ruleName, opts.MissingSec))
	}

	builder.WriteString(fmt.Sprintf(`
[rule.%[1]s.pending]
enabled = %[2]t
delay_sec = %[3]d
`, ruleName, opts.PendingEnabled, opts.PendingDelaySec))
	builder.WriteString(routeTOML)
	return builder.String()
}

// metricNeedsResolveEvent reports whether resolved transition requires a new event.
// Params: metric case.
// Returns: true for missing-heartbeat rules.
func metricNeedsResolveEvent(metric e2eMetricCase) bool {
	return metric.AlertType == "missing_heartbeat"
}

// postMetricEvent sends one input event by HTTP for metric lifecycle checks.
// Params: test handle, base service URL, matched var, and host key.
// Returns: request accepted or test fails.
func postMetricEvent(t *testing.T, baseURL, metricVar, host string) {
	t.Helper()
	body := []byte(fmt.Sprintf(
		`{"dt":%d,"type":"event","tags":{"dc":"dc1","service":"api","host":"%s"},"var":"%s","value":{"t":"n","n":1},"agg_cnt":1,"win":0}`,
		time.Now().UnixMilli(),
		host,
		metricVar,
	))
	response, err := http.Post(baseURL+"/ingest", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("ingest request: %v", err)
	}
	_, _ = io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("expected ingest 202, got %d", response.StatusCode)
	}
}
