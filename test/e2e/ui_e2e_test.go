package e2e

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUIDashboardBasicAuthE2E(t *testing.T) {
	natsURL, stop := startLocalNATSServer(t)
	defer stop()

	configPath := filepath.Join(t.TempDir(), "ui.toml")
	writeFile(t, configPath, fmt.Sprintf(`
[service]
name = "alerting"
mode = "single"

[ingest.http]
enabled = true
listen = "127.0.0.1:18081"
health_path = "/healthz"
ready_path = "/readyz"
ingest_path = "/ingest"

[notify.http]
enabled = true
url = "http://127.0.0.1:18091/notify"

[[notify.http.name-template]]
name = "http_default"
message = "{{ .Message }}"

[ui]
enabled = true
listen = "127.0.0.1:18082"
base_path = "/ui"
refresh_sec = 1

[ui.auth.basic]
enabled = true
username = "ops"
password = "secret"

[ui.nats]
url = [%q]
registry_bucket = "ui_registry_e2e"
registry_ttl_sec = 30
stale_after_sec = 10
offline_after_sec = 20

[ui.health]
timeout_sec = 1

[ui.service]
public_base_url = "http://127.0.0.1:18081"

[rule.ct]
alert_type = "count_total"

[rule.ct.match]
type = ["event"]
var = ["errors"]
tags = { dc = ["dc1"] }

[rule.ct.key]
from_tags = ["dc"]

[rule.ct.raise]
n = 1

[rule.ct.resolve]
silence_sec = 5

[[rule.ct.notify.route]]
channel = "http"
template = "http_default"
`, natsURL))

	service := newServiceFromConfig(t, configPath)
	cancel, done := runService(t, service)
	defer func() {
		cancel()
		waitServiceStop(t, done)
	}()

	waitReady(t, 18081)

	response, err := http.Get("http://127.0.0.1:18082/ui")
	if err != nil {
		t.Fatalf("get ui: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.StatusCode)
	}

	request, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:18082/ui", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.SetBasicAuth("ops", "secret")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("authorized get ui: %v", err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("unexpected ui code %d body=%s", response.StatusCode, string(body))
	}
	if !strings.Contains(string(body), "Alerting Dashboard") {
		t.Fatalf("unexpected ui body: %s", string(body))
	}
	if !strings.Contains(string(body), `fetch(basePath + "/api/dashboard"`) {
		t.Fatalf("dashboard page missing interactive snapshot api usage: %s", string(body))
	}
	if !strings.Contains(string(body), "Backend Snapshot Feed Lost") || !strings.Contains(string(body), "showOfflineModal(") {
		t.Fatalf("dashboard page missing backend-offline overlay: %s", string(body))
	}

	request, err = http.NewRequest(http.MethodGet, "http://127.0.0.1:18082/ui/api/dashboard", nil)
	if err != nil {
		t.Fatalf("new api request: %v", err)
	}
	request.SetBasicAuth("ops", "secret")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("authorized get dashboard api: %v", err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("unexpected dashboard api code %d body=%s", response.StatusCode, string(body))
	}
	if !strings.Contains(string(body), `"services":[`) {
		t.Fatalf("unexpected dashboard api body: %s", string(body))
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
}
