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
	"strings"
	"sync"
	"testing"
	"time"
)

type mattermostWebhookPayload struct {
	ChannelID string `json:"channel_id"`
	Message   string `json:"message"`
	RootID    string `json:"root_id,omitempty"`
	Auth      string `json:"-"`
}

type mattermostWebhookLog struct {
	mu    sync.Mutex
	items []mattermostWebhookPayload
}

func (l *mattermostWebhookLog) add(item mattermostWebhookPayload) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.items = append(l.items, item)
}

func (l *mattermostWebhookLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.items)
}

func (l *mattermostWebhookLog) at(index int) (mattermostWebhookPayload, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if index < 0 || index >= len(l.items) {
		return mattermostWebhookPayload{}, false
	}
	return l.items[index], true
}

func TestMattermostWebhookFiringAndResolveE2E(t *testing.T) {
	port, err := freePort()
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	natsURL, stopNATS := startLocalNATSServer(t)
	defer stopNATS()

	logs := &mattermostWebhookLog{}
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != "/api/v4/posts" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		defer r.Body.Close()

		var payload mattermostWebhookPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		payload.Auth = r.Header.Get("Authorization")
		logs.add(payload)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"id":"post-%d"}`, logs.count())
	}))
	defer webhook.Close()

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
message = "{{ .RuleName }} {{ .State }} {{ .Var }}={{ .MetricValue }} id={{ .ShortID }}"

[rule.mm_live]
alert_type = "count_total"

[rule.mm_live.match]
type = ["event"]
var = ["errors"]
tags = { dc = ["dc1"], service = ["api"] }

[rule.mm_live.key]
from_tags = ["dc", "service", "host"]

[rule.mm_live.raise]
n = 1

[rule.mm_live.resolve]
silence_sec = 1

[rule.mm_live.pending]
enabled = false
delay_sec = 300

[[rule.mm_live.notify.route]]
channel = "mattermost"
template = "mm_default"
`, port, natsURL, webhook.URL)

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

	waitFor(t, 4*time.Second, func() bool {
		return logs.count() >= 2
	})

	first, ok := logs.at(0)
	if !ok {
		t.Fatalf("missing first mattermost payload")
	}
	second, ok := logs.at(1)
	if !ok {
		t.Fatalf("missing second mattermost payload")
	}

	if first.Auth != "Bearer mm-token" {
		t.Fatalf("unexpected mattermost auth header: %q", first.Auth)
	}
	if !strings.Contains(first.Message, "mm_live firing") {
		t.Fatalf("unexpected firing payload text: %q", first.Message)
	}
	if first.ChannelID != "channel-1" || first.RootID != "" {
		t.Fatalf("unexpected first mattermost payload fields: %+v", first)
	}
	if !strings.Contains(second.Message, "mm_live resolved") {
		t.Fatalf("unexpected resolved payload text: %q", second.Message)
	}
	if second.ChannelID != "channel-1" || second.RootID != "post-1" {
		t.Fatalf("resolved payload must reply to firing post: %+v", second)
	}

	cancel()
	waitServiceStop(t, done)
}

func TestMattermostWebhookRetryOnTransientFailureE2E(t *testing.T) {
	port, err := freePort()
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	natsURL, stopNATS := startLocalNATSServer(t)
	defer stopNATS()

	var (
		mu         sync.Mutex
		calls      int
		successful int
	)
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != "/api/v4/posts" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		defer r.Body.Close()
		var payload mattermostWebhookPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		mu.Lock()
		calls++
		current := calls
		if current >= 2 {
			successful++
		}
		mu.Unlock()

		if current == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("temporary failure"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"id":"post-retry-%d"}`, current)
	}))
	defer webhook.Close()

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

[notify.telegram]
enabled = false

[notify.mattermost]
enabled = true
base_url = "%s"
bot_token = "mm-token"
channel_id = "channel-1"
timeout_sec = 2

[notify.mattermost.retry]
enabled = true
backoff = "exponential"
initial_ms = 10
max_ms = 20
max_attempts = 3
log_each_attempt = true

[[notify.mattermost.name-template]]
name = "mm_default"
message = "{{ .RuleName }} {{ .State }} {{ .Var }}={{ .MetricValue }}"

[rule.mm_retry]
alert_type = "count_total"

[rule.mm_retry.match]
type = ["event"]
var = ["errors"]
tags = { dc = ["dc1"], service = ["api"] }

[rule.mm_retry.key]
from_tags = ["dc", "service", "host"]

[rule.mm_retry.raise]
n = 1

[rule.mm_retry.resolve]
silence_sec = 3600

[rule.mm_retry.pending]
enabled = false
delay_sec = 300

[[rule.mm_retry.notify.route]]
channel = "mattermost"
template = "mm_default"
`, port, natsURL, webhook.URL)

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

	waitFor(t, 4*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return calls >= 2 && successful >= 1
	})

	mu.Lock()
	finalCalls := calls
	finalSuccess := successful
	mu.Unlock()
	if finalCalls < 2 {
		t.Fatalf("expected retry calls >=2, got %d", finalCalls)
	}
	if finalSuccess < 1 {
		t.Fatalf("expected at least one successful retry call, got %d", finalSuccess)
	}

	cancel()
	waitServiceStop(t, done)
}
