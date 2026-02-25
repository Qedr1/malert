package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type mattermostActiveCall struct {
	Method    string
	Path      string
	ChannelID string
	Message   string
}

type mattermostActiveLog struct {
	mu    sync.Mutex
	calls []mattermostActiveCall
}

func (l *mattermostActiveLog) add(call mattermostActiveCall) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, call)
}

func (l *mattermostActiveLog) snapshot() []mattermostActiveCall {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]mattermostActiveCall, len(l.calls))
	copy(out, l.calls)
	return out
}

func TestMattermostActiveOnlyLifecycleE2E(t *testing.T) {
	for _, metric := range e2eFunctionalMetricCases() {
		metric := metric
		t.Run(metric.Name, func(t *testing.T) {
			port, err := freePort()
			if err != nil {
				t.Fatalf("free port: %v", err)
			}
			natsURL, stopNATS := startLocalNATSServer(t)
			defer stopNATS()

			logs := &mattermostActiveLog{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload struct {
					ChannelID string `json:"channel_id"`
					Message   string `json:"message"`
				}
				if r.Body != nil {
					defer r.Body.Close()
					_ = json.NewDecoder(r.Body).Decode(&payload)
				}
				logs.add(mattermostActiveCall{
					Method:    r.Method,
					Path:      r.URL.Path,
					ChannelID: payload.ChannelID,
					Message:   payload.Message,
				})

				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/api/v4/posts":
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"id":"post-1"}`))
				case r.Method == http.MethodPut && r.URL.Path == "/api/v4/posts/post-1":
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"id":"post-1"}`))
				case r.Method == http.MethodDelete && r.URL.Path == "/api/v4/posts/post-1":
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"id":"post-1","delete_at":1}`))
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()

			ruleName := "mm_active_" + metric.Name
			ruleOptions := defaultE2ERuleOptions(metric)
			switch metric.AlertType {
			case "count_total", "count_window":
				ruleOptions.ResolveSilenceSec = 3
			default:
				ruleOptions.MissingSec = 2
			}

			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "config.toml")
			configBody := e2eConfigPrefix(port, natsURL, e2eNotifyOptions{
				Repeat:           true,
				RepeatEverySec:   1,
				RepeatOn:         "firing",
				RepeatPerChannel: true,
				OnPending:        false,
			}) + fmt.Sprintf(`
[notify.mattermost]
	enabled = true
	base_url = "%s"
	bot_token = "mm-token"
	channel_id = "channel-history"
	active_channel_id = "channel-active"
	timeout_sec = 2

[notify.mattermost.retry]
	enabled = false

[[notify.mattermost.name-template]]
	name = "mm_active"
	message = "{{ .RuleName }} {{ .State }} {{ .Var }}={{ .MetricValue }}"
`, server.URL)
			configBody += buildRuleTOML(ruleName, metric, e2eMetricVar, ruleOptions, fmt.Sprintf(`
[[rule.%[1]s.notify.route]]
name = "%[1]s"
channel = "mattermost"
template = "mm_active"
mode = "active_only"
`, ruleName))
			if err := os.WriteFile(configPath, []byte(configBody), 0o644); err != nil {
				t.Fatalf("write config: %v", err)
			}

			service := newServiceFromConfig(t, configPath)
			cancel, done := runService(t, service)
			defer cancel()

			baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
			waitReady(t, port)

			postMetricEvent(t, baseURL, e2eMetricVar, "h1")
			waitFor(t, 8*time.Second, func() bool {
				calls := logs.snapshot()
				for _, call := range calls {
					if call.Method == http.MethodPost && call.Path == "/api/v4/posts" {
						return true
					}
				}
				return false
			})

			if metricNeedsResolveEvent(metric) {
				waitFor(t, 8*time.Second, func() bool {
					calls := logs.snapshot()
					for _, call := range calls {
						if call.Method == http.MethodPut && call.Path == "/api/v4/posts/post-1" {
							return true
						}
					}
					return false
				})
				postMetricEvent(t, baseURL, e2eMetricVar, "h1")
			}

			waitFor(t, 10*time.Second, func() bool {
				calls := logs.snapshot()
				for _, call := range calls {
					if call.Method == http.MethodDelete && call.Path == "/api/v4/posts/post-1" {
						return true
					}
				}
				return false
			})

			calls := logs.snapshot()
			createCount := 0
			updateCount := 0
			deleteCount := 0
			for _, call := range calls {
				switch {
				case call.Method == http.MethodPost && call.Path == "/api/v4/posts":
					createCount++
					if call.ChannelID != "channel-active" {
						t.Fatalf("active create must use active channel, got %+v", call)
					}
				case call.Method == http.MethodPut && call.Path == "/api/v4/posts/post-1":
					updateCount++
				case call.Method == http.MethodDelete && call.Path == "/api/v4/posts/post-1":
					deleteCount++
				}
			}
			if createCount != 1 {
				t.Fatalf("expected exactly one create call, got %d (calls=%+v)", createCount, calls)
			}
			if updateCount < 1 {
				t.Fatalf("expected at least one update call, got %d (calls=%+v)", updateCount, calls)
			}
			if deleteCount != 1 {
				t.Fatalf("expected exactly one delete call, got %d (calls=%+v)", deleteCount, calls)
			}

			cancel()
			waitServiceStop(t, done)
		})
	}
}
