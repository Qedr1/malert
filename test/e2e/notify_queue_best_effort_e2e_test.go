package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"alerting/internal/domain"
	"alerting/internal/notifyqueue"

	"github.com/nats-io/nats.go"
)

type queueBestEffortCollector struct {
	mu    sync.Mutex
	items []domain.Notification
}

func (c *queueBestEffortCollector) Handle(w http.ResponseWriter, r *http.Request) {
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
	c.mu.Lock()
	c.items = append(c.items, payload)
	c.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (c *queueBestEffortCollector) Count(ruleName string, state domain.AlertState) int {
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

func TestNotifyQueueBestEffortPerChannelE2E(t *testing.T) {
	for _, metric := range allE2EMetricCases() {
		metric := metric
		t.Run(metric.Name, func(t *testing.T) {
			port, err := freePort()
			if err != nil {
				t.Fatalf("free port: %v", err)
			}
			natsURL, stopNATS := startLocalNATSServer(t)
			defer stopNATS()

			collector := &queueBestEffortCollector{}
			httpSink := httptest.NewServer(http.HandlerFunc(collector.Handle))
			defer httpSink.Close()

			var mattermostCalls int64
			mattermostSink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt64(&mattermostCalls, 1)
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"forced failure"}`))
			}))
			defer mattermostSink.Close()

			ruleName := "best_effort_" + metric.Name
			ruleOptions := defaultE2ERuleOptions(metric)
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "config.toml")
			cfg := e2eStandardConfigPrefix(port, natsURL) + fmt.Sprintf(`
[notify.queue]
	enabled = true
	url = "%s"
	subject = "alerting.notify.jobs.best-effort"
stream = "ALERTING_NOTIFY_BEST_EFFORT"
consumer_name = "alerting-notify-best-effort"
deliver_group = "alerting-notify-best-effort"
ack_wait_sec = 5
nack_delay_ms = 10
max_deliver = 1
max_ack_pending = 1024

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
	message = "{{ .RuleName }} {{ .State }} {{ .Var }}={{ .MetricValue }}"

[notify.mattermost]
	enabled = true
	base_url = "%s"
	bot_token = "mm-token"
	channel_id = "channel-1"
	timeout_sec = 2

[notify.mattermost.retry]
	enabled = false

[[notify.mattermost.name-template]]
	name = "mm_default"
	message = "{{ .RuleName }} {{ .State }} {{ .Var }}={{ .MetricValue }}"
	`, natsURL, httpSink.URL, mattermostSink.URL)
			cfg += buildRuleTOML(ruleName, metric, e2eMetricVar, ruleOptions, fmt.Sprintf(`
[[rule.%[1]s.notify.route]]
channel = "http"
template = "http_default"

[[rule.%[1]s.notify.route]]
channel = "mattermost"
template = "mm_default"
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
				return collector.Count(ruleName, domain.AlertStateFiring) >= 1
			})
			if metricNeedsResolveEvent(metric) {
				postMetricEvent(t, baseURL, e2eMetricVar, "h1")
			}
			waitFor(t, 6*time.Second, func() bool {
				return collector.Count(ruleName, domain.AlertStateResolved) >= 1
			})

			if atomic.LoadInt64(&mattermostCalls) == 0 {
				t.Fatalf("expected mattermost channel attempts > 0")
			}
			if collector.Count(ruleName, domain.AlertStateFiring) != 1 {
				t.Fatalf("expected one firing delivery to http channel")
			}
			if collector.Count(ruleName, domain.AlertStateResolved) != 1 {
				t.Fatalf("expected one resolved delivery to http channel")
			}

			cancel()
			waitServiceStop(t, done)
		})
	}
}

func TestNotifyQueueDLQOnMaxDeliverE2E(t *testing.T) {
	for _, metric := range allE2EMetricCases() {
		metric := metric
		t.Run(metric.Name, func(t *testing.T) {
			port, err := freePort()
			if err != nil {
				t.Fatalf("free port: %v", err)
			}
			natsURL, stopNATS := startLocalNATSServer(t)
			defer stopNATS()

			mattermostSink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"forced failure"}`))
			}))
			defer mattermostSink.Close()

			ruleName := "dlq_case_" + metric.Name
			ruleOptions := defaultE2ERuleOptions(metric)
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "config.toml")
			cfg := e2eStandardConfigPrefix(port, natsURL) + fmt.Sprintf(`
[notify.queue]
enabled = true
url = "%s"
subject = "alerting.notify.jobs.dlq-e2e"
stream = "ALERTING_NOTIFY_DLQ_E2E"
consumer_name = "alerting-notify-dlq-e2e"
deliver_group = "alerting-notify-dlq-e2e"
ack_wait_sec = 5
nack_delay_ms = 10
max_deliver = 1
max_ack_pending = 1024

[notify.queue.dlq]
enabled = true
subject = "alerting.notify.jobs.dlq-e2e.dead"
stream = "ALERTING_NOTIFY_DLQ_E2E_DEAD"

[notify.telegram]
enabled = false

[notify.mattermost]
enabled = true
base_url = "%s"
bot_token = "mm-token"
channel_id = "channel-1"
timeout_sec = 2

[notify.mattermost.retry]
enabled = false

[[notify.mattermost.name-template]]
name = "mm_default"
message = "{{ .RuleName }} {{ .State }} {{ .Var }}={{ .MetricValue }}"
`, natsURL, mattermostSink.URL)
			cfg += buildRuleTOML(ruleName, metric, e2eMetricVar, ruleOptions, fmt.Sprintf(`
[[rule.%s.notify.route]]
channel = "mattermost"
template = "mm_default"
`, ruleName))

			if err := os.WriteFile(configPath, []byte(cfg), 0o644); err != nil {
				t.Fatalf("write config: %v", err)
			}

			nc, err := nats.Connect(natsURL)
			if err != nil {
				t.Fatalf("connect nats: %v", err)
			}
			defer nc.Close()
			dlqSub, err := nc.SubscribeSync("alerting.notify.jobs.dlq-e2e.dead")
			if err != nil {
				t.Fatalf("subscribe dlq: %v", err)
			}
			if err := nc.Flush(); err != nil {
				t.Fatalf("flush subscribe: %v", err)
			}

			service := newServiceFromConfig(t, configPath)
			cancel, done := runService(t, service)
			defer cancel()

			baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
			waitReady(t, port)

			postMetricEvent(t, baseURL, e2eMetricVar, "h1")

			message, err := dlqSub.NextMsg(10 * time.Second)
			if err != nil {
				t.Fatalf("wait dlq message: %v", err)
			}
			var entry notifyqueue.DLQEntry
			if err := json.Unmarshal(message.Data, &entry); err != nil {
				t.Fatalf("decode dlq entry: %v", err)
			}
			if entry.Reason != notifyqueue.DLQReasonMaxDeliverExceeded {
				t.Fatalf("unexpected dlq reason: %s", entry.Reason)
			}
			if entry.Job.Channel != "mattermost" {
				t.Fatalf("unexpected dlq job channel: %s", entry.Job.Channel)
			}
			if entry.Job.Notification.State != domain.AlertStateFiring {
				t.Fatalf("unexpected dlq state: %s", entry.Job.Notification.State)
			}
			if entry.Attempts != 1 {
				t.Fatalf("unexpected dlq attempts: %d", entry.Attempts)
			}

			cancel()
			waitServiceStop(t, done)
		})
	}
}
