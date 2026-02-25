package e2e

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"alerting/internal/domain"

	"github.com/nats-io/nats.go"
)

func TestMultiServiceNATSQueueLifecycleSingleNotifications(t *testing.T) {
	for _, metric := range allE2EMetricCases() {
		metric := metric
		t.Run(metric.Name, func(t *testing.T) {
			natsURL, stopNATS := startLocalNATSServer(t)
			defer stopNATS()

			ensureEventStream(t, natsURL, e2eEventsStream, e2eEventsSubj)
			ensureStateBuckets(t, natsURL, e2eTickBucket, e2eDataBucket)

			collector := &notificationCollector{}
			webhook := httptest.NewServer(http.HandlerFunc(collector.Handle))
			defer webhook.Close()

			portA, err := freePort()
			if err != nil {
				t.Fatalf("free port A: %v", err)
			}
			portB, err := freePort()
			if err != nil {
				t.Fatalf("free port B: %v", err)
			}

			ruleName := "nats_multi_" + metric.Name
			ruleOptions := defaultE2ERuleOptions(metric)
			switch metric.AlertType {
			case "count_total", "count_window":
				ruleOptions.ResolveSilenceSec = 2
			default:
				ruleOptions.MissingSec = 2
			}

			tmpDir := t.TempDir()
			cfgAPath := filepath.Join(tmpDir, "svc-a.toml")
			cfgBPath := filepath.Join(tmpDir, "svc-b.toml")
			if err := os.WriteFile(cfgAPath, []byte(multiserviceNATSConfigTOML(portA, webhook.URL, natsURL, "svc-a", metric, ruleName, ruleOptions)), 0o644); err != nil {
				t.Fatalf("write config A: %v", err)
			}
			if err := os.WriteFile(cfgBPath, []byte(multiserviceNATSConfigTOML(portB, webhook.URL, natsURL, "svc-b", metric, ruleName, ruleOptions)), 0o644); err != nil {
				t.Fatalf("write config B: %v", err)
			}

			serviceA := newServiceFromConfig(t, cfgAPath)
			serviceB := newServiceFromConfig(t, cfgBPath)

			cancelA, doneA := runService(t, serviceA)
			defer cancelA()
			cancelB, doneB := runService(t, serviceB)
			defer cancelB()

			waitReady(t, portA)
			waitReady(t, portB)

			if err := publishNATSEvent(natsURL, e2eEventsSubj, fmt.Sprintf(`{"dt":%d,"type":"event","tags":{"dc":"dc1","service":"api","host":"h1"},"var":"%s","value":{"t":"n","n":1},"agg_cnt":1,"win":0}`, time.Now().UnixMilli(), e2eMetricVar)); err != nil {
				t.Fatalf("publish nats event: %v", err)
			}

			if !waitUntil(10*time.Second, func() bool {
				return collector.Count(ruleName, domain.AlertStateFiring) >= 1
			}) {
				t.Fatalf("missing firing notification for %s: total=%d snapshot=%+v", ruleName, collector.Total(), collector.Snapshot())
			}
			if metricNeedsResolveEvent(metric) {
				resolveDeadline := time.Now().Add(12 * time.Second)
				for collector.Count(ruleName, domain.AlertStateResolved) < 1 && time.Now().Before(resolveDeadline) {
					if err := publishNATSEvent(natsURL, e2eEventsSubj, fmt.Sprintf(`{"dt":%d,"type":"event","tags":{"dc":"dc1","service":"api","host":"h1"},"var":"%s","value":{"t":"n","n":1},"agg_cnt":1,"win":0}`, time.Now().UnixMilli(), e2eMetricVar)); err != nil {
						t.Fatalf("publish nats resolve event: %v", err)
					}
					time.Sleep(300 * time.Millisecond)
				}
				if collector.Count(ruleName, domain.AlertStateResolved) < 1 {
					t.Fatalf("missing resolved notification for %s: total=%d snapshot=%+v", ruleName, collector.Total(), collector.Snapshot())
				}
			} else {
				if !waitUntil(12*time.Second, func() bool {
					return collector.Count(ruleName, domain.AlertStateResolved) >= 1
				}) {
					t.Fatalf("missing resolved notification for %s: total=%d snapshot=%+v", ruleName, collector.Total(), collector.Snapshot())
				}
			}

			time.Sleep(1200 * time.Millisecond)
			firing := collector.Count(ruleName, domain.AlertStateFiring)
			resolved := collector.Count(ruleName, domain.AlertStateResolved)
			if metricNeedsResolveEvent(metric) {
				if firing < 1 {
					t.Fatalf("expected firing notifications >=1, got %d", firing)
				}
				if resolved < 1 {
					t.Fatalf("expected resolved notifications >=1, got %d", resolved)
				}
			} else {
				if firing != 1 {
					t.Fatalf("expected single firing notification, got %d", firing)
				}
				if resolved != 1 {
					t.Fatalf("expected single resolved notification, got %d", resolved)
				}
				if total := collector.Total(); total != 2 {
					t.Fatalf("expected exactly 2 notifications total, got %d", total)
				}
			}

			cancelA()
			cancelB()
			waitServiceStop(t, doneA)
			waitServiceStop(t, doneB)
		})
	}
}

func waitUntil(timeout time.Duration, check func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func publishNATSEvent(url, subject, body string) error {
	nc, err := nats.Connect(url)
	if err != nil {
		return err
	}
	defer nc.Close()
	if err := nc.Publish(subject, []byte(body)); err != nil {
		return err
	}
	return nc.FlushTimeout(3 * time.Second)
}

func multiserviceNATSConfigTOML(port int, webhookURL, natsURL, serviceName string, metric e2eMetricCase, ruleName string, ruleOptions e2eRuleOptions) string {
	base := fmt.Sprintf(`
[service]
name = "%s"
reload_enabled = false
resolve_scan_interval_sec = 1

[log.console]
enabled = true
level = "error"
format = "line"

[ingest.http]
enabled = false
listen = "127.0.0.1:%d"
health_path = "/healthz"
ready_path = "/readyz"
ingest_path = "/ingest"

[ingest.nats]
enabled = true
url = ["%s"]
workers = 2
ack_wait_sec = 10
nack_delay_ms = 100
max_deliver = -1
max_ack_pending = 4096

[notify]
repeat = false
repeat_every_sec = 300
repeat_on = ["firing"]
repeat_per_channel = true
on_pending = false

[notify.http]
enabled = true
url = "%s"
method = "POST"
timeout_sec = 2

[notify.http.retry]
enabled = false

[notify.telegram]
enabled = false

[[notify.http.name-template]]
name = "http_default"
message = "[{{ .RuleName }}] {{ .Message }} state={{ .State }}"
`, serviceName, port, natsURL, webhookURL)
	return base + buildRuleTOML(ruleName, metric, e2eMetricVar, ruleOptions, fmt.Sprintf(`
[[rule.%s.notify.route]]
channel = "http"
template = "http_default"
`, ruleName))
}
