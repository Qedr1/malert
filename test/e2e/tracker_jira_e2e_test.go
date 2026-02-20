package e2e

import (
	"bytes"
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

[rule.jira_tracker]
alert_type = "count_total"

[rule.jira_tracker.match]
type = ["event"]
var = ["errors"]
tags = { dc = ["dc1"], service = ["api"] }

[rule.jira_tracker.key]
from_tags = ["dc", "service", "host"]

[rule.jira_tracker.raise]
n = 1

[rule.jira_tracker.resolve]
silence_sec = 1

[rule.jira_tracker.pending]
enabled = false
delay_sec = 300

[[rule.jira_tracker.notify.route]]
channel = "jira"
template = "jira_default"
`, port, natsURL, tracker.URL)

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
		return logs.countCreate() >= 1
	})
	waitFor(t, 6*time.Second, func() bool {
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
}
