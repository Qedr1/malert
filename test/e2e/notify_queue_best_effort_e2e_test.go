package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"alerting/internal/domain"
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

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")
	cfg := fmt.Sprintf(`
[service]
name = "alerting"
reload_enabled = false
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

[rule.best_effort]
alert_type = "count_total"

[rule.best_effort.match]
type = ["event"]
var = ["errors"]
tags = { dc = ["dc1"], service = ["api"] }

[rule.best_effort.key]
from_tags = ["dc", "service", "host"]

[rule.best_effort.raise]
n = 1

[rule.best_effort.resolve]
silence_sec = 1

[[rule.best_effort.notify.route]]
channel = "http"
template = "http_default"

[[rule.best_effort.notify.route]]
channel = "mattermost"
template = "mm_default"
`, port, natsURL, natsURL, httpSink.URL, mattermostSink.URL)

	if err := os.WriteFile(configPath, []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	service := newServiceFromConfig(t, configPath)
	cancel, done := runService(t, service)
	defer cancel()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitReady(t, port)

	eventJSON := []byte(`{"dt":1739876543210,"type":"event","tags":{"dc":"dc1","service":"api","host":"h1"},"var":"errors","value":{"t":"n","n":1},"agg_cnt":1,"win":0}`)
	response, err := http.Post(baseURL+"/ingest", "application/json", bytes.NewReader(eventJSON))
	if err != nil {
		t.Fatalf("ingest request: %v", err)
	}
	_, _ = io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("expected ingest 202, got %d", response.StatusCode)
	}

	waitFor(t, 5*time.Second, func() bool {
		return collector.Count("best_effort", domain.AlertStateFiring) >= 1
	})
	waitFor(t, 6*time.Second, func() bool {
		return collector.Count("best_effort", domain.AlertStateResolved) >= 1
	})

	if atomic.LoadInt64(&mattermostCalls) == 0 {
		t.Fatalf("expected mattermost channel attempts > 0")
	}
	if collector.Count("best_effort", domain.AlertStateFiring) != 1 {
		t.Fatalf("expected one firing delivery to http channel")
	}
	if collector.Count("best_effort", domain.AlertStateResolved) != 1 {
		t.Fatalf("expected one resolved delivery to http channel")
	}

	cancel()
	waitServiceStop(t, done)
}
