package e2e

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type trackerRequestLog struct {
	mu    sync.Mutex
	items []trackerRequestItem
}

type trackerRequestItem struct {
	Path string
	Body string
}

func (l *trackerRequestLog) add(path, body string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.items = append(l.items, trackerRequestItem{Path: path, Body: body})
}

func (l *trackerRequestLog) countCreate() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	total := 0
	for _, item := range l.items {
		if item.Path == "/rest/api/3/issue" {
			total++
		}
	}
	return total
}

func (l *trackerRequestLog) countResolve() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	total := 0
	for _, item := range l.items {
		if strings.Contains(item.Path, "/rest/api/3/issue/") && strings.HasSuffix(item.Path, "/transitions") {
			total++
		}
	}
	return total
}

func (l *trackerRequestLog) resolvePath() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, item := range l.items {
		if strings.Contains(item.Path, "/rest/api/3/issue/") && strings.HasSuffix(item.Path, "/transitions") {
			return item.Path
		}
	}
	return ""
}

func TestJiraTrackerCreateAndResolveE2E(t *testing.T) {
	for _, metric := range allE2EMetricCases() {
		metric := metric
		t.Run(metric.Name, func(t *testing.T) {
			port, err := freePort()
			if err != nil {
				t.Fatalf("free port: %v", err)
			}
			natsURL, stopNATS := startLocalNATSServer(t)
			defer stopNATS()

			logs := &trackerRequestLog{}
			tracker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				body, _ := io.ReadAll(r.Body)
				_ = r.Body.Close()
				logs.add(r.URL.Path, string(body))

				switch r.URL.Path {
				case "/rest/api/3/issue":
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"key":"OPS-777"}`))
				case "/rest/api/3/issue/OPS-777/transitions":
					w.WriteHeader(http.StatusNoContent)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer tracker.Close()

			ruleName := "jira_tracker_" + metric.Name
			ruleOptions := defaultE2ERuleOptions(metric)
			switch metric.AlertType {
			case "count_total", "count_window":
				ruleOptions.ResolveSilenceSec = 1
			default:
				ruleOptions.MissingSec = 2
			}

			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "config.toml")
			cfg := e2eStandardConfigPrefix(port, natsURL) + fmt.Sprintf(`
[notify.telegram]
	enabled = false

[notify.jira]
	enabled = true
	base_url = "%s"
	timeout_sec = 2

[notify.jira.auth]
	type = "none"

[notify.jira.retry]
	enabled = false

[notify.jira.create]
	method = "POST"
	path = "/rest/api/3/issue"
	headers = { Content-Type = "application/json" }
	body_template = "{\"summary\": {{ json .Message }}, \"alert_id\": {{ json .AlertID }}}"
	success_status = [201]
	ref_json_path = "key"

[notify.jira.resolve]
	method = "POST"
	path = "/rest/api/3/issue/{{ .ExternalRef }}/transitions"
	headers = { Content-Type = "application/json" }
	body_template = "{\"transition\":{\"id\":\"31\"}}"
	success_status = [204]

[[notify.jira.name-template]]
	name = "jira_default"
	message = "{{ .RuleName }} {{ .State }} {{ .Var }}={{ .MetricValue }} id={{ .ShortID }}"
`, tracker.URL)
			cfg += buildRuleTOML(ruleName, metric, e2eMetricVar, ruleOptions, fmt.Sprintf(`
[[rule.%s.notify.route]]
channel = "jira"
template = "jira_default"
`, ruleName))

			if err := os.WriteFile(configPath, []byte(cfg), 0o644); err != nil {
				t.Fatalf("write config: %v", err)
			}

			service := newServiceFromConfig(t, configPath)
			cancel, done := runService(t, service)
			defer cancel()

			baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
			waitReady(t, port)

			postMetricEvent(t, baseURL, e2eMetricVar, "h1")

			waitFor(t, 8*time.Second, func() bool {
				return logs.countCreate() >= 1
			})
			if metricNeedsResolveEvent(metric) {
				postMetricEvent(t, baseURL, e2eMetricVar, "h1")
			}
			waitFor(t, 10*time.Second, func() bool {
				return logs.countResolve() >= 1
			})

			if logs.countCreate() != 1 {
				t.Fatalf("expected exactly one create request, got %d", logs.countCreate())
			}
			if logs.countResolve() != 1 {
				t.Fatalf("expected exactly one resolve request, got %d", logs.countResolve())
			}
			if gotPath := logs.resolvePath(); gotPath != "/rest/api/3/issue/OPS-777/transitions" {
				t.Fatalf("resolve path mismatch: %s", gotPath)
			}

			cancel()
			waitServiceStop(t, done)
		})
	}
}
