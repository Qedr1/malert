package e2e

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServiceSmokeHealthReadyAndIngest(t *testing.T) {
	type modeCase struct {
		name    string
		mode    string
		useNATS bool
	}
	modes := []modeCase{
		{name: "nats", mode: "", useNATS: true},
		{name: "single", mode: "single", useNATS: false},
	}

	for _, metric := range e2eFunctionalMetricCases() {
		metric := metric
		t.Run(metric.Name, func(t *testing.T) {
			for _, mode := range modes {
				mode := mode
				t.Run(mode.name, func(t *testing.T) {
					port, err := freePort()
					if err != nil {
						t.Fatalf("free port: %v", err)
					}

					natsURL := ""
					if mode.useNATS {
						var stopNATS func()
						natsURL, stopNATS = startLocalNATSServer(t)
						defer stopNATS()
					}

					ruleName := mode.name + "_smoke_" + metric.Name
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
					cfg := e2eConfigPrefixWithMode(port, natsURL, e2eNotifyOptions{
						Repeat:           true,
						RepeatEverySec:   300,
						RepeatOn:         "firing",
						RepeatPerChannel: true,
						OnPending:        false,
					}, mode.mode)
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

					postMetricEvent(t, baseURL, e2eMetricVar, "smoke-h1")

					cancel()
					waitServiceStop(t, done)
				})
			}
		})
	}
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
