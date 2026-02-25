package e2e

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type trackerE2EScenario struct {
	RouteChannel  string
	RouteTemplate string
	CreatePath    string
	ResolvePath   string
	CreateStatus  int
	CreateBody    string
	ResolveStatus int
	NotifyConfig  func(baseURL string) string
	RulePrefix    string
}

type trackerPathLog struct {
	mu    sync.Mutex
	paths []string
}

func (l *trackerPathLog) add(path string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.paths = append(l.paths, path)
}

func (l *trackerPathLog) count(path string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	total := 0
	for _, item := range l.paths {
		if item == path {
			total++
		}
	}
	return total
}

func runTrackerCreateAndResolveE2E(t *testing.T, scenario trackerE2EScenario) {
	t.Helper()
	for _, metric := range e2eFunctionalMetricCases() {
		metric := metric
		t.Run(metric.Name, func(t *testing.T) {
			port, err := freePort()
			if err != nil {
				t.Fatalf("free port: %v", err)
			}
			natsURL, stopNATS := startLocalNATSServer(t)
			defer stopNATS()

			logs := &trackerPathLog{}
			tracker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				_, _ = io.ReadAll(r.Body)
				_ = r.Body.Close()
				logs.add(r.URL.Path)

				switch r.URL.Path {
				case scenario.CreatePath:
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(scenario.CreateStatus)
					_, _ = w.Write([]byte(scenario.CreateBody))
				case scenario.ResolvePath:
					w.WriteHeader(scenario.ResolveStatus)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer tracker.Close()

			ruleName := scenario.RulePrefix + metric.Name
			ruleOptions := defaultE2ERuleOptions(metric)
			switch metric.AlertType {
			case "count_total", "count_window":
				ruleOptions.ResolveSilenceSec = 1
			default:
				ruleOptions.MissingSec = 2
			}

			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "config.toml")
			cfg := e2eStandardConfigPrefix(port, natsURL) + scenario.NotifyConfig(tracker.URL)
			cfg += buildRuleTOML(ruleName, metric, e2eMetricVar, ruleOptions, fmt.Sprintf(`
[[rule.%s.notify.route]]
channel = "%s"
template = "%s"
`, ruleName, scenario.RouteChannel, scenario.RouteTemplate))

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
				return logs.count(scenario.CreatePath) >= 1
			})
			if metricNeedsResolveEvent(metric) {
				postMetricEvent(t, baseURL, e2eMetricVar, "h1")
			}
			waitFor(t, 10*time.Second, func() bool {
				return logs.count(scenario.ResolvePath) >= 1
			})

			if logs.count(scenario.CreatePath) != 1 {
				t.Fatalf("expected exactly one create request %s, got %d", scenario.CreatePath, logs.count(scenario.CreatePath))
			}
			if logs.count(scenario.ResolvePath) != 1 {
				t.Fatalf("expected exactly one resolve request %s, got %d", scenario.ResolvePath, logs.count(scenario.ResolvePath))
			}

			cancel()
			waitServiceStop(t, done)
		})
	}
}
