package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"alerting/internal/clock"
	"alerting/internal/config"
	"alerting/test/testutil"
)

func TestReloadConfigIgnoresUIChangesUntilRestart(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	natsURL, stop := testutil.StartLocalNATSServer(t)
	defer stop()
	configPath := filepath.Join(tmpDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(serviceUIReloadConfig("rule_old", "errors", "127.0.0.1:18101", "127.0.0.1:18111", natsURL)), 0o644); err != nil {
		t.Fatalf("write initial config: %v", err)
	}

	source, err := config.FromCLI(configPath, "")
	if err != nil {
		t.Fatalf("config source: %v", err)
	}
	service, err := NewService(source, clock.RealClock{})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer service.cleanupInitResources()

	initial := service.currentConfig()
	if initial.UI.Listen != "127.0.0.1:18111" {
		t.Fatalf("unexpected initial ui listen %q", initial.UI.Listen)
	}

	if err := os.WriteFile(configPath, []byte(serviceUIReloadConfig("rule_new", "latency", "127.0.0.1:18101", "127.0.0.1:19111", natsURL)), 0o644); err != nil {
		t.Fatalf("write reloaded config: %v", err)
	}
	if err := service.reloadConfig(context.Background()); err != nil {
		t.Fatalf("reload config: %v", err)
	}

	reloaded := service.currentConfig()
	if reloaded.UI.Listen != "127.0.0.1:18111" {
		t.Fatalf("ui listen changed during reload: %q", reloaded.UI.Listen)
	}
	if len(reloaded.Rule) != 1 || reloaded.Rule[0].Name != "rule_new" || reloaded.Rule[0].Match.Var[0] != "latency" {
		t.Fatalf("runtime rules were not reloaded: %+v", reloaded.Rule)
	}
}

func serviceUIReloadConfig(ruleName, metricVar, httpListen, uiListen, natsURL string) string {
	return `
[service]
name = "alerting"
mode = "single"
reload_enabled = true
reload_interval_sec = 1
resolve_scan_interval_sec = 1

[ingest.http]
enabled = true
listen = "` + httpListen + `"
health_path = "/healthz"
ready_path = "/readyz"
ingest_path = "/ingest"

[notify.http]
enabled = true
url = "http://127.0.0.1:18090/notify"

[[notify.http.name-template]]
name = "http_default"
message = "{{ .Message }}"

[ui]
enabled = true
listen = "` + uiListen + `"
base_path = "/ui"
refresh_sec = 1

[ui.auth.basic]
enabled = true
username = "ops"
password = "secret"

[ui.nats]
url = ["` + natsURL + `"]
registry_bucket = "ui_registry_reload"
registry_ttl_sec = 30
stale_after_sec = 10
offline_after_sec = 20

[ui.health]
timeout_sec = 1

[ui.service]
public_base_url = "http://127.0.0.1:18101"

[rule.` + ruleName + `]
alert_type = "count_total"

[rule.` + ruleName + `.match]
type = ["event"]
var = ["` + metricVar + `"]
tags = { dc = ["dc1"] }

[rule.` + ruleName + `.key]
from_tags = ["dc"]

[rule.` + ruleName + `.raise]
n = 1

[rule.` + ruleName + `.resolve]
silence_sec = 5

[[rule.` + ruleName + `.notify.route]]
channel = "http"
template = "http_default"
`
}
