package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type telegramLiveReply struct {
	MessageID int `json:"message_id"`
}

type telegramLiveCapturedRequest struct {
	ChatID      string
	Text        string
	ParseMode   string
	Reply       *telegramLiveReply
	MessageID   int
	HTTPStatus  int
	TelegramOK  bool
	Description string
}

type telegramLiveProxy struct {
	mu       sync.Mutex
	client   *http.Client
	requests []telegramLiveCapturedRequest
}

var deltaPattern = regexp.MustCompile(`Дельта: \d+\.\d[smh]`)

func newTelegramLiveProxy() *telegramLiveProxy {
	return &telegramLiveProxy{
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (proxy *telegramLiveProxy) Handle(writer http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		http.Error(writer, "read request body", http.StatusBadRequest)
		return
	}
	_ = request.Body.Close()

	parsed := telegramLiveCapturedRequest{}
	isSendMessage := request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/sendMessage")
	if isSendMessage {
		parseReq, parseErr := http.NewRequest(http.MethodPost, "http://proxy.local"+request.URL.Path, bytes.NewReader(body))
		if parseErr == nil {
			parseReq.Header = request.Header.Clone()
			if multipartErr := parseReq.ParseMultipartForm(2 << 20); multipartErr == nil {
				parsed.ChatID = parseReq.FormValue("chat_id")
				parsed.Text = parseReq.FormValue("text")
				parsed.ParseMode = parseReq.FormValue("parse_mode")
				if rawReply := parseReq.FormValue("reply_parameters"); rawReply != "" {
					reply := &telegramLiveReply{}
					if decodeReplyErr := json.Unmarshal([]byte(rawReply), reply); decodeReplyErr == nil {
						parsed.Reply = reply
					}
				}
			}
		}
	}

	upstreamURL := "https://api.telegram.org" + request.URL.RequestURI()
	upstreamReq, err := http.NewRequestWithContext(request.Context(), request.Method, upstreamURL, bytes.NewReader(body))
	if err != nil {
		http.Error(writer, "build upstream request", http.StatusInternalServerError)
		return
	}
	upstreamReq.Header = request.Header.Clone()

	response, err := proxy.client.Do(upstreamReq)
	if err != nil {
		http.Error(writer, "telegram upstream request failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		http.Error(writer, "read upstream response", http.StatusBadGateway)
		return
	}

	for key, values := range response.Header {
		for _, value := range values {
			writer.Header().Add(key, value)
		}
	}
	writer.WriteHeader(response.StatusCode)
	_, _ = writer.Write(responseBody)

	if !isSendMessage {
		return
	}

	parsed.HTTPStatus = response.StatusCode
	var telegramResponse struct {
		OK          bool `json:"ok"`
		Description string
		Result      struct {
			MessageID int `json:"message_id"`
		} `json:"result"`
	}
	if decodeErr := json.Unmarshal(responseBody, &telegramResponse); decodeErr == nil {
		parsed.TelegramOK = telegramResponse.OK
		parsed.Description = telegramResponse.Description
		parsed.MessageID = telegramResponse.Result.MessageID
	}

	proxy.mu.Lock()
	proxy.requests = append(proxy.requests, parsed)
	proxy.mu.Unlock()
}

func (proxy *telegramLiveProxy) Snapshot() []telegramLiveCapturedRequest {
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	out := make([]telegramLiveCapturedRequest, len(proxy.requests))
	copy(out, proxy.requests)
	return out
}

func TestTelegramLiveEndToEnd(t *testing.T) {
	botToken := strings.TrimSpace(os.Getenv("E2E_TG_BOT_TOKEN"))
	chatID := strings.TrimSpace(os.Getenv("E2E_TG_CHAT_ID"))
	if botToken == "" || chatID == "" {
		t.Skip("set E2E_TG_BOT_TOKEN and E2E_TG_CHAT_ID for live telegram e2e")
	}
	silenceSec := parsePositiveIntEnv("E2E_TG_SILENCE_SEC", 60)

	for _, metric := range allE2EMetricCases() {
		metric := metric
		t.Run(metric.Name, func(t *testing.T) {
			port, err := freePort()
			if err != nil {
				t.Fatalf("free port: %v", err)
			}
			natsURL, stopNATS := startLocalNATSServer(t)
			defer stopNATS()

			proxy := newTelegramLiveProxy()
			proxyServer := httptest.NewServer(http.HandlerFunc(proxy.Handle))
			defer proxyServer.Close()

			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "config.toml")
			if err := os.WriteFile(configPath, []byte(telegramLiveConfigTOML(port, natsURL, proxyServer.URL, botToken, chatID, silenceSec, metric)), 0o644); err != nil {
				t.Fatalf("write config: %v", err)
			}

			service := newServiceFromConfig(t, configPath)
			cancel, done := runService(t, service)
			defer cancel()

			baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
			waitReady(t, port)

			postMetricEvent(t, baseURL, e2eMetricVar, "live-h1")
			waitFor(t, 25*time.Second, func() bool {
				return len(proxy.Snapshot()) >= 1
			})
			if metricNeedsResolveEvent(metric) {
				postMetricEvent(t, baseURL, e2eMetricVar, "live-h1")
			}

			waitTimeoutSec := silenceSec + 45
			if waitTimeoutSec < 40 {
				waitTimeoutSec = 40
			}
			waitFor(t, time.Duration(waitTimeoutSec)*time.Second, func() bool {
				return len(proxy.Snapshot()) >= 2
			})

			requests := proxy.Snapshot()
			if len(requests) < 2 {
				t.Fatalf("expected at least 2 telegram sends, got %d", len(requests))
			}
			first := requests[0]
			second := requests[1]

			if first.HTTPStatus != http.StatusOK || !first.TelegramOK {
				t.Fatalf("first send failed: status=%d ok=%v desc=%q", first.HTTPStatus, first.TelegramOK, first.Description)
			}
			if first.ChatID != chatID {
				t.Fatalf("first chat id mismatch: got=%q want=%q", first.ChatID, chatID)
			}
			if first.Reply != nil {
				t.Fatalf("first send must not contain reply_parameters")
			}
			if first.MessageID <= 0 {
				t.Fatalf("first send missing message_id")
			}
			if first.ParseMode != "HTML" {
				t.Fatalf("first send parse_mode mismatch: %q", first.ParseMode)
			}
			if !strings.Contains(first.Text, "<b>☝️") || strings.Contains(first.Text, "АЛЕРТ(") || !strings.Contains(first.Text, "<pre>") || !strings.Contains(first.Text, "errors: 1") {
				t.Fatalf("first message text mismatch: %q", first.Text)
			}

			if second.HTTPStatus != http.StatusOK || !second.TelegramOK {
				t.Fatalf("second send failed: status=%d ok=%v desc=%q", second.HTTPStatus, second.TelegramOK, second.Description)
			}
			if second.Reply == nil {
				t.Fatalf("resolved send must contain reply_parameters")
			}
			if second.Reply.MessageID != first.MessageID {
				t.Fatalf("resolved reply target mismatch: got=%d want=%d", second.Reply.MessageID, first.MessageID)
			}
			if second.ParseMode != "HTML" {
				t.Fatalf("second send parse_mode mismatch: %q", second.ParseMode)
			}
			if !strings.Contains(second.Text, "<b>👍") || !strings.Contains(second.Text, "<pre>") || !strings.Contains(second.Text, "errors: 1 (OK)") || !deltaPattern.MatchString(second.Text) {
				t.Fatalf("second message text mismatch: %q", second.Text)
			}

			cancel()
			waitServiceStop(t, done)
		})
	}
}

func telegramLiveConfigTOML(port int, natsURL, telegramAPIBase, botToken, chatID string, silenceSec int, metric e2eMetricCase) string {
	ruleName := "ct_telegram_live_" + metric.Name
	ruleOptions := defaultE2ERuleOptions(metric)
	switch metric.AlertType {
	case "count_total", "count_window":
		ruleOptions.ResolveSilenceSec = silenceSec
	default:
		ruleOptions.MissingSec = 2
	}

	base := fmt.Sprintf(`
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
enabled = true
bot_token = "%s"
chat_id = "%s"
api_base = "%s"

[notify.telegram.retry]
enabled = false

[[notify.telegram.name-template]]
name = "tg_default"
message = '''
{{- if eq .State "firing" -}}
<b>☝️{{ .RuleName }} ({{ .ShortID }})</b>
<pre>{{ .Var }}: {{ .MetricValue }}
Начало: {{ .StartedAt.Format "15:04" }}
</pre>
{{- else if eq .State "resolved" -}}
<b>👍{{ .RuleName }} ({{ .ShortID }})</b>
<pre>{{ .Var }}: {{ .MetricValue }} (OK)
Начало: {{ .StartedAt.Format "15:04" }}
Дельта: {{ fmtDuration .Duration }}
</pre>
{{- end -}}
'''
`, port, natsURL, botToken, chatID, telegramAPIBase)
	return base + buildRuleTOML(ruleName, metric, e2eMetricVar, ruleOptions, fmt.Sprintf(`
[[rule.%s.notify.route]]
channel = "telegram"
template = "tg_default"
`, ruleName))
}

func parsePositiveIntEnv(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
