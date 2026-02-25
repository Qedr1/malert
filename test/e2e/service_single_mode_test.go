package e2e

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestServiceSingleModeHTTPOnly(t *testing.T) {
	for _, metric := range allE2EMetricCases() {
		metric := metric
		t.Run(metric.Name, func(t *testing.T) {
			port, err := freePort()
			if err != nil {
				t.Fatalf("free port: %v", err)
			}

			ruleName := "single_" + metric.Name
			options := defaultE2ERuleOptions(metric)
			switch metric.AlertType {
			case "count_total":
				options.RaiseN = 100
				options.ResolveSilenceSec = 5
			case "count_window":
				options.RaiseN = 100
				options.WindowSec = 60
				options.ResolveSilenceSec = 5
			default:
				options.MissingSec = 3600
			}

			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "config.toml")
			cfg := e2eConfigPrefixWithMode(port, "", e2eNotifyOptions{
				Repeat:           true,
				RepeatEverySec:   300,
				RepeatOn:         "firing",
				RepeatPerChannel: true,
				OnPending:        false,
			}, "single")
			cfg += e2eHTTPNotifyConfig("http://127.0.0.1:1/notify")
			cfg += buildRuleTOML(ruleName, metric, e2eMetricVar, options, fmt.Sprintf(`
[[rule.%[1]s.notify.route]]
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

			resp, err := http.Get(baseURL + "/healthz")
			if err != nil {
				t.Fatalf("health request: %v", err)
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("expected health 200, got %d", resp.StatusCode)
			}
			_ = resp.Body.Close()

			postMetricEvent(t, baseURL, e2eMetricVar, "single-h1")

			cancel()
			waitServiceStop(t, done)
		})
	}
}
