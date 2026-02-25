package e2e

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

type telegramActiveCall struct {
	Path      string
	ChatID    string
	MessageID string
	Text      string
}

type telegramActiveMock struct {
	mu    sync.Mutex
	calls []telegramActiveCall
}

func (m *telegramActiveMock) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseMultipartForm(2 << 20); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	call := telegramActiveCall{
		Path:      r.URL.Path,
		ChatID:    r.FormValue("chat_id"),
		MessageID: r.FormValue("message_id"),
		Text:      r.FormValue("text"),
	}
	m.mu.Lock()
	m.calls = append(m.calls, call)
	m.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/bottoken/sendMessage":
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":301,"date":1,"chat":{"id":1,"type":"private"}}}`))
	case "/bottoken/editMessageText":
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":301,"date":1,"chat":{"id":1,"type":"private"}}}`))
	case "/bottoken/deleteMessage":
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (m *telegramActiveMock) snapshot() []telegramActiveCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]telegramActiveCall, len(m.calls))
	copy(out, m.calls)
	return out
}

func TestTelegramActiveOnlyLifecycleE2E(t *testing.T) {
	for _, metric := range e2eFunctionalMetricCases() {
		metric := metric
		t.Run(metric.Name, func(t *testing.T) {
			port, err := freePort()
			if err != nil {
				t.Fatalf("free port: %v", err)
			}
			natsURL, stopNATS := startLocalNATSServer(t)
			defer stopNATS()

			mock := &telegramActiveMock{}
			apiServer := httptest.NewServer(http.HandlerFunc(mock.handle))
			defer apiServer.Close()

			ruleName := "tg_active_" + metric.Name
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
[notify.telegram]
	enabled = true
	bot_token = "token"
	chat_id = "history-chat"
	active_chat_id = "active-chat"
	api_base = "%s"

[notify.telegram.retry]
	enabled = false

[[notify.telegram.name-template]]
	name = "tg_active"
	message = "{{ .RuleName }} {{ .State }} {{ .Var }}={{ .MetricValue }}"
`, apiServer.URL)
			configBody += buildRuleTOML(ruleName, metric, e2eMetricVar, ruleOptions, fmt.Sprintf(`
[[rule.%[1]s.notify.route]]
name = "%[1]s"
channel = "telegram"
template = "tg_active"
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
				calls := mock.snapshot()
				for _, call := range calls {
					if call.Path == "/bottoken/sendMessage" {
						return true
					}
				}
				return false
			})

			if metricNeedsResolveEvent(metric) {
				waitFor(t, 8*time.Second, func() bool {
					calls := mock.snapshot()
					for _, call := range calls {
						if call.Path == "/bottoken/editMessageText" {
							return true
						}
					}
					return false
				})
				postMetricEvent(t, baseURL, e2eMetricVar, "h1")
			}

			waitFor(t, 10*time.Second, func() bool {
				calls := mock.snapshot()
				for _, call := range calls {
					if call.Path == "/bottoken/deleteMessage" {
						return true
					}
				}
				return false
			})

			calls := mock.snapshot()
			sendCount := 0
			editCount := 0
			deleteCount := 0
			var sentMessageID string
			for _, call := range calls {
				if call.ChatID != "active-chat" {
					t.Fatalf("unexpected chat id in active route call: %+v", call)
				}
				switch call.Path {
				case "/bottoken/sendMessage":
					sendCount++
					sentMessageID = "301"
				case "/bottoken/editMessageText":
					editCount++
					if call.MessageID != "301" {
						t.Fatalf("unexpected edit message id: %+v", call)
					}
				case "/bottoken/deleteMessage":
					deleteCount++
					if call.MessageID != "301" {
						t.Fatalf("unexpected delete message id: %+v", call)
					}
				}
			}
			if sendCount != 1 {
				t.Fatalf("expected exactly one sendMessage call, got %d (calls=%+v)", sendCount, calls)
			}
			if editCount < 1 {
				t.Fatalf("expected at least one editMessageText call, got %d (calls=%+v)", editCount, calls)
			}
			if deleteCount != 1 {
				t.Fatalf("expected exactly one deleteMessage call, got %d (calls=%+v)", deleteCount, calls)
			}
			if sentMessageID == "" {
				t.Fatalf("missing initial sendMessage")
			}
			if _, err := strconv.Atoi(sentMessageID); err != nil {
				t.Fatalf("unexpected send message id: %q", sentMessageID)
			}

			cancel()
			waitServiceStop(t, done)
		})
	}
}
