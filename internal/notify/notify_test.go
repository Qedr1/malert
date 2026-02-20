package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"alerting/internal/config"
	"alerting/internal/domain"
)

type flakySender struct {
	channel string
	fails   int
	calls   int
}

func (s *flakySender) Channel() string { return s.channel }

func (s *flakySender) Send(_ context.Context, _ domain.Notification) (SendResult, error) {
	s.calls++
	if s.calls <= s.fails {
		return SendResult{}, errors.New("temporary error")
	}
	return SendResult{}, nil
}

type captureSender struct {
	channel string
	items   []domain.Notification
}

func (s *captureSender) Channel() string { return s.channel }

func (s *captureSender) Send(_ context.Context, notification domain.Notification) (SendResult, error) {
	s.items = append(s.items, notification)
	return SendResult{}, nil
}

func TestDispatcherRetriesUntilSuccess(t *testing.T) {
	t.Parallel()

	sender := &flakySender{channel: "telegram", fails: 2}
	render, err := parseTemplate("test.retry", "{{ .Message }}")
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}
	dispatcher := &Dispatcher{
		senders: map[string]ChannelSender{"telegram": sender},
		retries: map[string]config.NotifyRetry{
			"telegram": {
				Enabled:     true,
				Backoff:     "exponential",
				InitialMS:   1,
				MaxMS:       2,
				MaxAttempts: 0,
			},
		},
		templates: map[string]compiledTemplate{
			templateKey("telegram", "telegram.retry"): {
				channel: "telegram",
				body:    render,
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err = dispatcher.Send(ctx, "telegram", "telegram.retry", domain.Notification{
		AlertID: "rule/r/v/h",
		Message: "firing",
	})
	if err != nil {
		t.Fatalf("expected retry success, got %v", err)
	}
	if sender.calls != 3 {
		t.Fatalf("expected 3 attempts, got %d", sender.calls)
	}
}

func TestDispatcherReturnsUnknownChannel(t *testing.T) {
	t.Parallel()

	dispatcher := &Dispatcher{senders: map[string]ChannelSender{}}
	if _, err := dispatcher.Send(context.Background(), "telegram", "telegram.main", domain.Notification{}); err == nil {
		t.Fatalf("expected unknown channel error")
	}
}

func TestNewDispatcherChannels(t *testing.T) {
	t.Parallel()

	dispatcher := NewDispatcher(config.NotifyConfig{
		Telegram: config.TelegramNotifier{
			Enabled:  true,
			BotToken: "token",
			ChatID:   "chat",
			APIBase:  "http://localhost",
			Retry: config.NotifyRetry{
				Enabled: true,
			},
		},
		HTTP: config.HTTPNotifier{
			Enabled: true,
			URL:     "http://localhost/callback",
			Retry: config.NotifyRetry{
				Enabled: true,
			},
		},
		Mattermost: config.MattermostConfig{
			Enabled:   true,
			BaseURL:   "http://localhost",
			BotToken:  "token",
			ChannelID: "channel-id",
			Retry: config.NotifyRetry{
				Enabled: true,
			},
		},
	}, nil)

	got := dispatcher.Channels()
	want := []string{"http", "mattermost", "telegram"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("channels mismatch: got=%v want=%v", got, want)
	}
}

func TestDispatcherAppliesChannelTemplate(t *testing.T) {
	t.Parallel()

	sender := &captureSender{channel: "telegram"}
	templates, templateErrs := buildTemplateSet(config.NotifyConfig{
		Telegram: config.TelegramNotifier{
			NameTemplate: []config.NamedTemplateConfig{
				{
					Name:    "telegram.main",
					Message: "TG {{ .AlertID }} {{ .State }} {{ .Message }}",
				},
			},
		},
	})
	dispatcher := &Dispatcher{
		senders:      map[string]ChannelSender{"telegram": sender},
		templates:    templates,
		templateErrs: templateErrs,
	}

	_, err := dispatcher.Send(context.Background(), "telegram", "telegram.main", domain.Notification{
		AlertID: "rule/r/v/h",
		State:   domain.AlertStateFiring,
		Message: "alert is firing",
	})
	if err != nil {
		t.Fatalf("send failed: %v", err)
	}
	if len(sender.items) != 1 {
		t.Fatalf("expected one sent notification, got %d", len(sender.items))
	}
	if sender.items[0].Message != "TG rule/r/v/h firing alert is firing" {
		t.Fatalf("unexpected rendered message: %q", sender.items[0].Message)
	}
}

func TestDispatcherAppliesFmtDurationTemplateFunc(t *testing.T) {
	t.Parallel()

	sender := &captureSender{channel: "telegram"}
	templates, templateErrs := buildTemplateSet(config.NotifyConfig{
		Telegram: config.TelegramNotifier{
			NameTemplate: []config.NamedTemplateConfig{
				{
					Name:    "telegram.duration",
					Message: "delta={{ fmtDuration .Duration }}",
				},
			},
		},
	})
	dispatcher := &Dispatcher{
		senders:      map[string]ChannelSender{"telegram": sender},
		templates:    templates,
		templateErrs: templateErrs,
	}

	_, err := dispatcher.Send(context.Background(), "telegram", "telegram.duration", domain.Notification{
		AlertID:  "rule/r/v/h",
		State:    domain.AlertStateResolved,
		Duration: 90 * time.Second,
	})
	if err != nil {
		t.Fatalf("send failed: %v", err)
	}
	if len(sender.items) != 1 {
		t.Fatalf("expected one sent notification, got %d", len(sender.items))
	}
	if sender.items[0].Message != "delta=1.5m" {
		t.Fatalf("unexpected rendered message: %q", sender.items[0].Message)
	}
}

func TestTelegramSenderSend(t *testing.T) {
	t.Parallel()

	type sendMessagePayload struct {
		ChatID    string
		Text      string
		ParseMode string
		Reply     *struct {
			MessageID int `json:"message_id"`
		}
	}

	var (
		mu       sync.Mutex
		received []sendMessagePayload
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method=%s", r.Method)
		}
		if r.URL.Path != "/bottoken/sendMessage" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		if err := r.ParseMultipartForm(2 << 20); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		payload := sendMessagePayload{
			ChatID:    r.FormValue("chat_id"),
			Text:      r.FormValue("text"),
			ParseMode: r.FormValue("parse_mode"),
		}
		if rawReply := r.FormValue("reply_parameters"); rawReply != "" {
			payload.Reply = &struct {
				MessageID int `json:"message_id"`
			}{}
			if err := json.Unmarshal([]byte(rawReply), payload.Reply); err != nil {
				t.Fatalf("decode reply_parameters: %v", err)
			}
		}
		mu.Lock()
		received = append(received, payload)
		messageID := 100 + len(received)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"date":1,"chat":{"id":1,"type":"private"}}}`, messageID)
	}))
	defer server.Close()

	sender := NewTelegramSender(config.TelegramNotifier{
		Enabled:  true,
		BotToken: "token",
		ChatID:   "chat",
		APIBase:  server.URL,
	})
	firstResult, err := sender.Send(context.Background(), domain.Notification{
		AlertID:  "rule/r/v/h",
		RuleName: "r",
		Var:      "v",
		State:    domain.AlertStateFiring,
		Message:  "firing",
	})
	if err != nil {
		t.Fatalf("send failed: %v", err)
	}
	if firstResult.MessageID != 101 {
		t.Fatalf("first message id=%d", firstResult.MessageID)
	}

	replyTo := firstResult.MessageID
	secondResult, err := sender.Send(context.Background(), domain.Notification{
		AlertID:          "rule/r/v/h",
		RuleName:         "r",
		Var:              "v",
		State:            domain.AlertStateResolved,
		Message:          "resolved",
		ReplyToMessageID: &replyTo,
	})
	if err != nil {
		t.Fatalf("send failed: %v", err)
	}
	if secondResult.MessageID != 102 {
		t.Fatalf("second message id=%d", secondResult.MessageID)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(received))
	}
	if received[0].Reply != nil {
		t.Fatalf("first message must not contain reply parameters")
	}
	if received[1].Reply == nil || received[1].Reply.MessageID != 101 {
		t.Fatalf("second message reply mismatch: %+v", received[1].Reply)
	}
	if received[0].ParseMode != "HTML" || received[1].ParseMode != "HTML" {
		t.Fatalf("parse mode mismatch: first=%q second=%q", received[0].ParseMode, received[1].ParseMode)
	}
	if received[0].Text != "firing" {
		t.Fatalf("text=%s", received[0].Text)
	}
}

func TestHTTPScenarioSenderSend(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Fatalf("method=%s", r.Method)
		}
		if r.Header.Get("X-Test") != "1" {
			t.Fatalf("missing custom header")
		}
		var payload domain.Notification
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if payload.AlertID != "rule/r/v/h" {
			t.Fatalf("alert_id=%s", payload.AlertID)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	sender := NewHTTPScenarioSender(config.HTTPNotifier{
		Enabled:    true,
		URL:        server.URL,
		Method:     http.MethodPut,
		TimeoutSec: 2,
		Headers: map[string]string{
			"X-Test": "1",
		},
	})
	_, err := sender.Send(context.Background(), domain.Notification{
		AlertID: "rule/r/v/h",
	})
	if err != nil {
		t.Fatalf("send failed: %v", err)
	}
}

func TestMattermostSenderSend(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method=%s", r.Method)
		}
		if r.URL.Path != "/api/v4/posts" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		var payload struct {
			ChannelID string `json:"channel_id"`
			Message   string `json:"message"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if payload.ChannelID != "channel-1" {
			t.Fatalf("channel_id=%s", payload.ChannelID)
		}
		if payload.Message != "firing" {
			t.Fatalf("message=%s", payload.Message)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"post-1"}`))
	}))
	defer server.Close()

	sender := NewMattermostSender(config.MattermostConfig{
		Enabled:   true,
		BaseURL:   server.URL,
		BotToken:  "token",
		ChannelID: "channel-1",
	})
	result, err := sender.Send(context.Background(), domain.Notification{
		AlertID:  "rule/r/v/h",
		RuleName: "r",
		State:    domain.AlertStateFiring,
		Message:  "firing",
	})
	if err != nil {
		t.Fatalf("send failed: %v", err)
	}
	if result.ExternalRef != "post-1" {
		t.Fatalf("unexpected external_ref %q", result.ExternalRef)
	}
}

func TestMattermostSenderResolvedUsesRootID(t *testing.T) {
	t.Parallel()

	var received struct {
		ChannelID string `json:"channel_id"`
		Message   string `json:"message"`
		RootID    string `json:"root_id"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method=%s", r.Method)
		}
		if r.URL.Path != "/api/v4/posts" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"post-2","root_id":"post-1"}`))
	}))
	defer server.Close()

	sender := NewMattermostSender(config.MattermostConfig{
		Enabled:    true,
		BaseURL:    server.URL,
		BotToken:   "token",
		ChannelID:  "channel-1",
		TimeoutSec: 2,
	})
	_, err := sender.Send(context.Background(), domain.Notification{
		AlertID:     "rule/r/v/h",
		RuleName:    "r",
		State:       domain.AlertStateResolved,
		Message:     "resolved",
		ExternalRef: "post-1",
	})
	if err != nil {
		t.Fatalf("send failed: %v", err)
	}

	if received.ChannelID != "channel-1" {
		t.Fatalf("channel_id=%s", received.ChannelID)
	}
	if received.Message != "resolved" {
		t.Fatalf("message=%s", received.Message)
	}
	if received.RootID != "post-1" {
		t.Fatalf("root_id=%s", received.RootID)
	}
}

func TestMattermostSenderStatusErrorIncludesResponseBody(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid payload"}`))
	}))
	defer server.Close()

	sender := NewMattermostSender(config.MattermostConfig{
		Enabled:    true,
		BaseURL:    server.URL,
		BotToken:   "token",
		ChannelID:  "channel-1",
		TimeoutSec: 1,
	})
	_, err := sender.Send(context.Background(), domain.Notification{
		AlertID: "rule/r/v/h",
		Message: "firing",
		State:   domain.AlertStateFiring,
	})
	if err == nil {
		t.Fatalf("expected status error")
	}
	if !strings.Contains(err.Error(), "mattermost status=400") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "invalid payload") {
		t.Fatalf("expected response body in error: %v", err)
	}
}

func TestMattermostSenderResolvedRequiresExternalRef(t *testing.T) {
	t.Parallel()

	sender := NewMattermostSender(config.MattermostConfig{
		Enabled:    true,
		BaseURL:    "https://mattermost.example",
		BotToken:   "token",
		ChannelID:  "channel-1",
		TimeoutSec: 1,
	})
	_, err := sender.Send(context.Background(), domain.Notification{
		AlertID: "rule/r/v/h",
		State:   domain.AlertStateResolved,
		Message: "resolved",
	})
	if err == nil {
		t.Fatalf("expected external_ref error")
	}
	if !strings.Contains(err.Error(), "mattermost resolved requires external_ref") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTrackerSenderCreateAndResolve(t *testing.T) {
	t.Parallel()

	type requestSnapshot struct {
		Method string
		Path   string
		Auth   string
		Body   string
	}

	var (
		mu       sync.Mutex
		requests []requestSnapshot
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := ioReadAllAndClose(r.Body)
		snapshot := requestSnapshot{
			Method: r.Method,
			Path:   r.URL.RequestURI(),
			Auth:   r.Header.Get("Authorization"),
			Body:   string(body),
		}
		mu.Lock()
		requests = append(requests, snapshot)
		count := len(requests)
		mu.Unlock()

		switch count {
		case 1:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"key":"OPS-123"}`))
		case 2:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request count=%d", count)
		}
	}))
	defer server.Close()

	sender := NewTrackerSender(config.NotifyChannelJira, config.TrackerNotifier{
		Enabled:    true,
		BaseURL:    server.URL,
		TimeoutSec: 2,
		Auth: config.TrackerAuthConfig{
			Type:     "basic",
			Username: "user@example.com",
			Password: "secret",
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
	})

	createResult, err := sender.Send(context.Background(), domain.Notification{
		AlertID: "rule/r/v/h",
		State:   domain.AlertStateFiring,
		Message: "firing",
	})
	if err != nil {
		t.Fatalf("create send failed: %v", err)
	}
	if createResult.ExternalRef != "OPS-123" {
		t.Fatalf("unexpected external ref %q", createResult.ExternalRef)
	}

	_, err = sender.Send(context.Background(), domain.Notification{
		AlertID:     "rule/r/v/h",
		State:       domain.AlertStateResolved,
		Message:     "resolved",
		ExternalRef: createResult.ExternalRef,
	})
	if err != nil {
		t.Fatalf("resolve send failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(requests))
	}
	if requests[0].Method != http.MethodPost || requests[0].Path != "/rest/api/3/issue" {
		t.Fatalf("unexpected create request: %+v", requests[0])
	}
	if !strings.HasPrefix(requests[0].Auth, "Basic ") {
		t.Fatalf("expected basic auth header, got %q", requests[0].Auth)
	}
	if !strings.Contains(requests[0].Body, `"summary": "firing"`) {
		t.Fatalf("unexpected create body: %s", requests[0].Body)
	}
	if requests[1].Path != "/rest/api/3/issue/OPS-123/transitions" {
		t.Fatalf("unexpected resolve path: %s", requests[1].Path)
	}
}

func TestTrackerSenderResolveRequiresExternalRef(t *testing.T) {
	t.Parallel()

	sender := NewTrackerSender(config.NotifyChannelYouTrack, config.TrackerNotifier{
		Enabled:    true,
		BaseURL:    "http://localhost",
		TimeoutSec: 1,
		Create: config.TrackerActionConfig{
			Method:        http.MethodPost,
			Path:          "/create",
			SuccessStatus: []int{http.StatusCreated},
			RefJSONPath:   "idReadable",
		},
		Resolve: config.TrackerActionConfig{
			Method:        http.MethodPost,
			Path:          "/resolve/{{ .ExternalRef }}",
			SuccessStatus: []int{http.StatusOK},
		},
	})

	_, err := sender.Send(context.Background(), domain.Notification{
		AlertID: "rule/r/v/h",
		State:   domain.AlertStateResolved,
	})
	if err == nil {
		t.Fatalf("expected external ref validation error")
	}
	if !strings.Contains(err.Error(), "external reference") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExtractJSONPathString(t *testing.T) {
	t.Parallel()

	value, err := extractJSONPathString([]byte(`{"result":{"issue":{"key":"OPS-42"}}}`), "result.issue.key")
	if err != nil {
		t.Fatalf("extract failed: %v", err)
	}
	if value != "OPS-42" {
		t.Fatalf("value=%q", value)
	}
}

func ioReadAllAndClose(body io.ReadCloser) ([]byte, error) {
	defer body.Close()
	return io.ReadAll(body)
}
