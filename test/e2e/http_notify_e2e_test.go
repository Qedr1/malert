package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"alerting/internal/domain"
)

type httpNotifyLog struct {
	mu    sync.Mutex
	items []domain.Notification
}

// add stores one delivered HTTP notification snapshot.
// Params: captured notification payload.
// Returns: log updated in place.
func (l *httpNotifyLog) add(item domain.Notification) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.items = append(l.items, item)
}

// snapshot returns a copy of captured HTTP notification payloads.
// Params: none.
// Returns: immutable copy for assertions.
func (l *httpNotifyLog) snapshot() []domain.Notification {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]domain.Notification, len(l.items))
	copy(out, l.items)
	return out
}

// TestHTTPNotifyDeliveryE2E verifies HTTP notify delivery for firing->resolved lifecycle.
// Params: test handle.
// Returns: end-to-end delivery assertions or test failure.
func TestHTTPNotifyDeliveryE2E(t *testing.T) {
	for _, metric := range e2eFunctionalMetricCases() {
		metric := metric
		t.Run(metric.Name, func(t *testing.T) {
			port, err := freePort()
			if err != nil {
				t.Fatalf("free port: %v", err)
			}
			natsURL, stopNATS := startLocalNATSServer(t)
			defer stopNATS()

			logs := &httpNotifyLog{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				defer r.Body.Close()

				var payload domain.Notification
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				logs.add(payload)
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			ruleName := "http_notify_" + metric.Name
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

[notify.http]
enabled = true
url = "%s"
method = "POST"
timeout_sec = 2

[notify.http.retry]
enabled = false

[[notify.http.name-template]]
name = "http_default"
message = "{{ .RuleName }} {{ .State }} {{ .Var }}={{ .MetricValue }} id={{ .ShortID }}"
`, server.URL)
			cfg += buildRuleTOML(ruleName, metric, e2eMetricVar, ruleOptions, fmt.Sprintf(`
[[rule.%s.notify.route]]
channel = "http"
template = "http_default"
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
				return len(logs.snapshot()) >= 1
			})
			if metricNeedsResolveEvent(metric) {
				postMetricEvent(t, baseURL, e2eMetricVar, "h1")
			}
			waitFor(t, 10*time.Second, func() bool {
				return len(logs.snapshot()) >= 2
			})

			items := logs.snapshot()
			if len(items) < 2 {
				t.Fatalf("expected at least 2 http notifications, got %d", len(items))
			}
			if items[0].State != domain.AlertStateFiring {
				t.Fatalf("first state=%s", items[0].State)
			}
			if items[1].State != domain.AlertStateResolved {
				t.Fatalf("second state=%s", items[1].State)
			}
			if items[0].RuleName != ruleName || items[1].RuleName != ruleName {
				t.Fatalf("rule name mismatch: first=%q second=%q", items[0].RuleName, items[1].RuleName)
			}
			if items[0].AlertID != items[1].AlertID {
				t.Fatalf("alert id mismatch: first=%q second=%q", items[0].AlertID, items[1].AlertID)
			}

			cancel()
			waitServiceStop(t, done)
		})
	}
}
