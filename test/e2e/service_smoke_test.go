package e2e

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServiceSmokeHealthReadyAndIngest(t *testing.T) {
	port, err := freePort()
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	natsURL, stopNATS := startLocalNATSServer(t)
	defer stopNATS()

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
repeat = true
repeat_every_sec = 300
repeat_on = ["firing"]
repeat_per_channel = true
on_pending = false

[notify.telegram]
enabled = false

[notify.http]
enabled = true
url = "http://127.0.0.1:1/notify"
method = "POST"
timeout_sec = 1

[notify.http.retry]
enabled = true
backoff = "exponential"
initial_ms = 1
max_ms = 2
max_attempts = 0
log_each_attempt = true

[[notify.http.name-template]]
name = "http_default"
message = "{{ .Message }}"

[rule.ct]
alert_type = "count_total"

[rule.ct.match]
type = ["event"]
var = ["errors"]
tags = { dc = ["dc1"], service = ["api"] }

[rule.ct.key]
from_tags = ["dc", "service"]

[rule.ct.raise]
n = 100

[rule.ct.resolve]
silence_sec = 5

[[rule.ct.notify.route]]
channel = "http"
template = "http_default"
`, port, natsURL)

	if err := os.WriteFile(configPath, []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	service := newServiceFromConfig(t, configPath)
	cancel, done := runService(t, service)
	defer cancel()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitReady(t, port)

	resp, err := http.Get(baseURL + "/healthz")
	if err != nil {
		t.Fatalf("health request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected health 200, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	eventJSON := []byte(`{"dt":1739876543210,"type":"event","tags":{"dc":"dc1","service":"api"},"var":"errors","value":{"t":"n","n":1},"agg_cnt":1,"win":0}`)
	resp, err = http.Post(baseURL+"/ingest", "application/json", bytes.NewReader(eventJSON))
	if err != nil {
		t.Fatalf("ingest request: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected ingest 202, got %d", resp.StatusCode)
	}

	cancel()
	waitServiceStop(t, done)
}

func freePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func waitFor(t *testing.T, timeout time.Duration, check func() bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for condition")
}
