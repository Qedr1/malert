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
	"strings"
	"sync"
	"testing"
	"time"
)

type telegramSendMessagePayload struct {
	ChatID string `json:"chat_id"`
	Text   string `json:"text"`
	Reply  *struct {
		MessageID int `json:"message_id"`
	}
}

type telegramCapturedRequest struct {
	Payload   telegramSendMessagePayload
	MessageID int
}

type telegramAPIMock struct {
	mu       sync.Mutex
	requests []telegramCapturedRequest
}

func (m *telegramAPIMock) Handle(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if request.URL.Path != "/bottoken/sendMessage" {
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	defer request.Body.Close()

	if err := request.ParseMultipartForm(2 << 20); err != nil {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}

	payload := telegramSendMessagePayload{
		ChatID: request.FormValue("chat_id"),
		Text:   request.FormValue("text"),
	}
	if rawReply := request.FormValue("reply_parameters"); rawReply != "" {
		payload.Reply = &struct {
			MessageID int `json:"message_id"`
		}{}
		if err := json.Unmarshal([]byte(rawReply), payload.Reply); err != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
	}

	m.mu.Lock()
	messageID := len(m.requests) + 101
	m.requests = append(m.requests, telegramCapturedRequest{
		Payload:   payload,
		MessageID: messageID,
	})
	m.mu.Unlock()

	writer.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(writer, `{"ok":true,"result":{"message_id":%d,"date":1,"chat":{"id":1,"type":"private"}}}`, messageID)
}

func (m *telegramAPIMock) Snapshot() []telegramCapturedRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]telegramCapturedRequest, len(m.requests))
	copy(out, m.requests)
	return out
}

func TestTelegramResolvedRepliesToOpeningMessage(t *testing.T) {
	port, err := freePort()
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	natsURL, stopNATS := startLocalNATSServer(t)
	defer stopNATS()

	mock := &telegramAPIMock{}
	telegramServer := httptest.NewServer(http.HandlerFunc(mock.Handle))
	defer telegramServer.Close()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(telegramReplyConfigTOML(port, natsURL, telegramServer.URL)), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	service := newServiceFromConfig(t, configPath)
	cancel, done := runService(t, service)
	defer cancel()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitReady(t, port)

	body := []byte(fmt.Sprintf(`{"dt":%d,"type":"event","tags":{"dc":"dc1","service":"api","host":"h1"},"var":"errors","value":{"t":"n","n":1},"agg_cnt":1,"win":0}`, time.Now().UnixMilli()))
	response, err := http.Post(baseURL+"/ingest", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("ingest request: %v", err)
	}
	_, _ = io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("expected ingest 202, got %d", response.StatusCode)
	}

	waitFor(t, 5*time.Second, func() bool {
		return len(mock.Snapshot()) >= 2
	})

	requests := mock.Snapshot()
	if len(requests) < 2 {
		t.Fatalf("expected at least 2 telegram notifications, got %d", len(requests))
	}
	first := requests[0]
	second := requests[1]
	if first.Payload.Reply != nil {
		t.Fatalf("first telegram message must not reply to another message")
	}
	if !strings.Contains(first.Payload.Text, "state=firing") {
		t.Fatalf("first message text mismatch: %s", first.Payload.Text)
	}
	if second.Payload.Reply == nil {
		t.Fatalf("resolved telegram message must contain reply_parameters")
	}
	if second.Payload.Reply.MessageID != first.MessageID {
		t.Fatalf("resolved reply target mismatch: got=%d want=%d", second.Payload.Reply.MessageID, first.MessageID)
	}
	if !strings.Contains(second.Payload.Text, "state=resolved") {
		t.Fatalf("second message text mismatch: %s", second.Payload.Text)
	}

	cancel()
	waitServiceStop(t, done)
}

func telegramReplyConfigTOML(port int, natsURL, telegramAPIBase string) string {
	return fmt.Sprintf(`
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
bot_token = "token"
chat_id = "1001"
api_base = "%s"

[notify.telegram.retry]
enabled = false

[[notify.telegram.name-template]]
name = "tg_default"
message = "[{{ .RuleName }}] {{ .Message }}\nalert_id={{ .AlertID }}\nstate={{ .State }}"

[rule.ct_telegram]
alert_type = "count_total"

[rule.ct_telegram.match]
type = ["event"]
var = ["errors"]
tags = { dc = ["dc1"], service = ["api"] }

[rule.ct_telegram.key]
from_tags = ["dc", "service", "host"]

[rule.ct_telegram.raise]
n = 1

[rule.ct_telegram.resolve]
silence_sec = 1

[[rule.ct_telegram.notify.route]]
channel = "telegram"
template = "tg_default"
`, port, natsURL, telegramAPIBase)
}
