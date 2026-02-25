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

type notificationCollector struct {
	mu    sync.Mutex
	items []domain.Notification
}

func (c *notificationCollector) Handle(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	defer request.Body.Close()

	var payload domain.Notification
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}

	c.mu.Lock()
	c.items = append(c.items, payload)
	c.mu.Unlock()

	writer.WriteHeader(http.StatusOK)
}

func (c *notificationCollector) Count(ruleName string, state domain.AlertState) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	count := 0
	for _, item := range c.items {
		if item.RuleName == ruleName && item.State == state {
			count++
		}
	}
	return count
}

func (c *notificationCollector) Total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

func (c *notificationCollector) Snapshot() []domain.Notification {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]domain.Notification, len(c.items))
	copy(out, c.items)
	return out
}

func TestHotReloadApplyValidSnapshot(t *testing.T) {
	for _, metric := range e2eFunctionalMetricCases() {
		metric := metric
		t.Run(metric.Name, func(t *testing.T) {
			port, err := freePort()
			if err != nil {
				t.Fatalf("free port: %v", err)
			}
			natsURL, stopNATS := startLocalNATSServer(t)
			defer stopNATS()

			collector := &notificationCollector{}
			webhook := httptest.NewServer(http.HandlerFunc(collector.Handle))
			defer webhook.Close()

			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "config.toml")
			if err := os.WriteFile(configPath, []byte(reloadConfigTOML(port, natsURL, webhook.URL, metric, "rule_old", "errors", false, true)), 0o644); err != nil {
				t.Fatalf("write initial config: %v", err)
			}

			service := newServiceFromConfig(t, configPath)
			cancel, done := runService(t, service)
			defer cancel()

			baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
			waitReady(t, port)

			sendEvent(t, baseURL, "errors", "h1")
			time.Sleep(300 * time.Millisecond)
			if collector.Total() != 0 {
				t.Fatalf("expected no notifications before reload threshold update")
			}

			if err := os.WriteFile(configPath, []byte(reloadConfigTOML(port, natsURL, webhook.URL, metric, "rule_new", "latency", true, true)), 0o644); err != nil {
				t.Fatalf("write reloaded config: %v", err)
			}
			time.Sleep(1500 * time.Millisecond)

			if metricNeedsResolveEvent(metric) {
				sendEvent(t, baseURL, "latency", "h10")
				waitFor(t, 10*time.Second, func() bool {
					return collector.Count("rule_new", domain.AlertStateFiring) >= 1
				})
			} else {
				nextHost := 10
				waitFor(t, 8*time.Second, func() bool {
					sendEvent(t, baseURL, "latency", fmt.Sprintf("h%d", nextHost))
					nextHost++
					return collector.Count("rule_new", domain.AlertStateFiring) >= 1
				})
			}

			before := collector.Total()
			sendEvent(t, baseURL, "errors", "h999")
			time.Sleep(300 * time.Millisecond)
			after := collector.Total()
			if after != before {
				t.Fatalf("old rule produced notification after reload: before=%d after=%d", before, after)
			}

			cancel()
			waitServiceStop(t, done)
		})
	}
}

func TestHotReloadKeepsPreviousSnapshotOnValidationError(t *testing.T) {
	for _, metric := range e2eFunctionalMetricCases() {
		metric := metric
		t.Run(metric.Name, func(t *testing.T) {
			port, err := freePort()
			if err != nil {
				t.Fatalf("free port: %v", err)
			}
			natsURL, stopNATS := startLocalNATSServer(t)
			defer stopNATS()

			collector := &notificationCollector{}
			webhook := httptest.NewServer(http.HandlerFunc(collector.Handle))
			defer webhook.Close()

			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "config.toml")
			if err := os.WriteFile(configPath, []byte(reloadConfigTOML(port, natsURL, webhook.URL, metric, "rule_stable", "errors", true, true)), 0o644); err != nil {
				t.Fatalf("write initial config: %v", err)
			}

			service := newServiceFromConfig(t, configPath)
			cancel, done := runService(t, service)
			defer cancel()

			baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
			waitReady(t, port)
			waitTimeout := 6 * time.Second
			if metricNeedsResolveEvent(metric) {
				waitTimeout = 10 * time.Second
			}

			sendEvent(t, baseURL, "errors", "s1")
			waitFor(t, waitTimeout, func() bool {
				return collector.Count("rule_stable", domain.AlertStateFiring) >= 1
			})

			if err := os.WriteFile(configPath, []byte(reloadConfigTOML(port, natsURL, webhook.URL, metric, "rule_broken", "latency", true, false)), 0o644); err != nil {
				t.Fatalf("write invalid config: %v", err)
			}
			time.Sleep(1500 * time.Millisecond)

			sendEvent(t, baseURL, "errors", "s2")
			waitFor(t, waitTimeout, func() bool {
				return collector.Count("rule_stable", domain.AlertStateFiring) >= 2
			})

			cancel()
			waitServiceStop(t, done)
		})
	}
}

func reloadConfigTOML(port int, natsURL, webhookURL string, metric e2eMetricCase, ruleName, metricVar string, emitOnFirstEvent bool, withRule bool) string {
	base := fmt.Sprintf(`
[service]
name = "alerting"
reload_enabled = true
reload_interval_sec = 1
resolve_scan_interval_sec = 1

[log.console]
enabled = true
level = "error"
format = "line"

[ingest.http]
enabled = true
listen = "127.0.0.1:%d"
health_path = "/healthz"
ready_path = "/readyz"
ingest_path = "/ingest"
max_body_bytes = 1048576

[ingest.nats]
enabled = false
url = ["%s"]

[notify]
repeat = false
repeat_every_sec = 300
repeat_on = ["firing"]
repeat_per_channel = true
on_pending = false

[notify.telegram]
enabled = false

[notify.http]
enabled = true
url = "%s"
method = "POST"
timeout_sec = 2

[notify.http.retry]
enabled = false

[notify.mattermost]
enabled = false

[[notify.http.name-template]]
name = "http_default"
message = "{{ .Message }}"
`, port, natsURL, webhookURL)

	if !withRule {
		return base
	}

	options := defaultE2ERuleOptions(metric)
	if emitOnFirstEvent {
		switch metric.AlertType {
		case "count_total", "count_window":
			options.RaiseN = 1
		default:
			options.MissingSec = 1
		}
	} else {
		switch metric.AlertType {
		case "count_total", "count_window":
			options.RaiseN = 2
			options.ResolveSilenceSec = 3600
		default:
			options.MissingSec = 3600
		}
	}

	return base + buildRuleTOML(ruleName, metric, metricVar, options, fmt.Sprintf(`
[[rule.%[1]s.notify.route]]
channel = "http"
template = "http_default"
`, ruleName))
}

func sendEvent(t *testing.T, baseURL, metricVar, host string) {
	t.Helper()
	postMetricEvent(t, baseURL, metricVar, host)
}
