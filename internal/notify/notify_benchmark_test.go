package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"alerting/internal/config"
	"alerting/internal/domain"
)

type benchmarkDispatcherCase struct {
	name         string
	channel      string
	templateName string
	cfg          config.NotifyConfig
	notification domain.Notification
}

// BenchmarkDispatcherChannelSend measures delivery-path cost per notify channel.
// Params: benchmark handle.
// Returns: per-channel send metrics for dispatcher+sender path.
func BenchmarkDispatcherChannelSend(b *testing.B) {
	for _, testCase := range benchmarkDispatcherCases(b) {
		testCase := testCase
		b.Run(testCase.name, func(b *testing.B) {
			dispatcher := NewDispatcher(testCase.cfg, nil)
			ctx := context.Background()

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := dispatcher.Send(ctx, testCase.channel, testCase.templateName, testCase.notification); err != nil {
					b.Fatalf("send failed: %v", err)
				}
			}
		})
	}
}

// benchmarkDispatcherCases builds per-channel dispatcher benchmark scenarios.
// Params: benchmark handle for cleanup registration.
// Returns: deterministic scenario list with isolated mock transports.
func benchmarkDispatcherCases(b *testing.B) []benchmarkDispatcherCase {
	b.Helper()

	telegramServer := newBenchmarkTelegramServer()
	b.Cleanup(telegramServer.Close)

	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		w.WriteHeader(http.StatusOK)
	}))
	b.Cleanup(httpServer.Close)

	mattermostServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"post-1"}`))
	}))
	b.Cleanup(mattermostServer.Close)

	jiraServer := newBenchmarkTrackerServer("key", "OPS-777")
	b.Cleanup(jiraServer.Close)

	youTrackServer := newBenchmarkTrackerServer("idReadable", "OPS-888")
	b.Cleanup(youTrackServer.Close)

	return []benchmarkDispatcherCase{
		{
			name:         "telegram",
			channel:      config.NotifyChannelTelegram,
			templateName: "tg_default",
			cfg: config.NotifyConfig{
				Telegram: config.TelegramNotifier{
					Enabled:  true,
					BotToken: "token",
					ChatID:   "1001",
					APIBase:  telegramServer.URL,
					NameTemplate: []config.NamedTemplateConfig{{
						Name:    "tg_default",
						Message: "{{ .RuleName }} {{ .State }} {{ .Message }}",
					}},
				},
			},
			notification: benchmarkNotification(domain.AlertStateFiring),
		},
		{
			name:         "http",
			channel:      config.NotifyChannelHTTP,
			templateName: "http_default",
			cfg: config.NotifyConfig{
				HTTP: config.HTTPNotifier{
					Enabled:    true,
					URL:        httpServer.URL,
					Method:     http.MethodPost,
					TimeoutSec: 2,
					NameTemplate: []config.NamedTemplateConfig{{
						Name:    "http_default",
						Message: "{{ .RuleName }} {{ .State }} {{ .Message }}",
					}},
				},
			},
			notification: benchmarkNotification(domain.AlertStateFiring),
		},
		{
			name:         "mattermost",
			channel:      config.NotifyChannelMattermost,
			templateName: "mm_default",
			cfg: config.NotifyConfig{
				Mattermost: config.MattermostConfig{
					Enabled:    true,
					BaseURL:    mattermostServer.URL,
					BotToken:   "token",
					ChannelID:  "channel-1",
					TimeoutSec: 2,
					NameTemplate: []config.NamedTemplateConfig{{
						Name:    "mm_default",
						Message: "{{ .RuleName }} {{ .State }} {{ .Message }}",
					}},
				},
			},
			notification: benchmarkNotification(domain.AlertStateFiring),
		},
		{
			name:         "jira",
			channel:      config.NotifyChannelJira,
			templateName: "jira_default",
			cfg: config.NotifyConfig{
				Jira: config.TrackerNotifier{
					Enabled:    true,
					BaseURL:    jiraServer.URL,
					TimeoutSec: 2,
					Auth: config.TrackerAuthConfig{
						Type: "none",
					},
					Create: config.TrackerActionConfig{
						Method:        http.MethodPost,
						Path:          "/rest/api/3/issue",
						BodyTemplate:  `{"summary": {{ json .Message }}, "alert_id": {{ json .AlertID }}}`,
						SuccessStatus: []int{http.StatusCreated},
						RefJSONPath:   "key",
					},
					Resolve: config.TrackerActionConfig{
						Method:        http.MethodPost,
						Path:          `/rest/api/3/issue/{{ .ExternalRef }}/transitions`,
						BodyTemplate:  `{"transition":{"id":"31"}}`,
						SuccessStatus: []int{http.StatusNoContent},
					},
					NameTemplate: []config.NamedTemplateConfig{{
						Name:    "jira_default",
						Message: "{{ .RuleName }} {{ .State }} {{ .Message }}",
					}},
				},
			},
			notification: benchmarkNotification(domain.AlertStateFiring),
		},
		{
			name:         "youtrack",
			channel:      config.NotifyChannelYouTrack,
			templateName: "youtrack_default",
			cfg: config.NotifyConfig{
				YouTrack: config.TrackerNotifier{
					Enabled:    true,
					BaseURL:    youTrackServer.URL,
					TimeoutSec: 2,
					Auth: config.TrackerAuthConfig{
						Type: "none",
					},
					Create: config.TrackerActionConfig{
						Method:        http.MethodPost,
						Path:          "/api/issues",
						BodyTemplate:  `{"summary": {{ json .Message }}, "alert_id": {{ json .AlertID }}}`,
						SuccessStatus: []int{http.StatusCreated},
						RefJSONPath:   "idReadable",
					},
					Resolve: config.TrackerActionConfig{
						Method:        http.MethodPost,
						Path:          `/api/issues/{{ .ExternalRef }}/commands`,
						BodyTemplate:  `{"query":"Fixed"}`,
						SuccessStatus: []int{http.StatusOK},
					},
					NameTemplate: []config.NamedTemplateConfig{{
						Name:    "youtrack_default",
						Message: "{{ .RuleName }} {{ .State }} {{ .Message }}",
					}},
				},
			},
			notification: benchmarkNotification(domain.AlertStateFiring),
		},
	}
}

// benchmarkNotification builds a stable notification payload for send benchmarks.
// Params: alert state for the send path under test.
// Returns: reusable notification payload.
func benchmarkNotification(state domain.AlertState) domain.Notification {
	return domain.Notification{
		AlertID:     "rule/bench/errors/dc1|api|host1",
		ShortID:     "host1",
		RuleName:    "bench_rule",
		Var:         "errors",
		State:       state,
		Message:     "alert is firing",
		MetricValue: "1",
	}
}

// newBenchmarkTelegramServer mocks Telegram Bot API sendMessage responses.
// Params: none.
// Returns: test HTTP server with incrementing message ids.
func newBenchmarkTelegramServer() *httptest.Server {
	var nextID atomic.Int64
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(2 << 20); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		id := nextID.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"date":1,"chat":{"id":1,"type":"private"}}}`, id)
	}))
}

// newBenchmarkTrackerServer mocks tracker create responses with stable ref field.
// Params: JSON ref field name and value.
// Returns: test HTTP server with created-issue response body.
func newBenchmarkTrackerServer(refField, refValue string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{refField: refValue})
	}))
}
