package notify

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"alerting/internal/config"
	"alerting/internal/domain"
	"alerting/internal/permanent"
	"alerting/internal/templatefmt"

	tgbot "github.com/go-telegram/bot"
	tgmodels "github.com/go-telegram/bot/models"
)

// SendResult returns channel-specific metadata after successful delivery.
// Params: sender-specific metadata fields.
// Returns: optional message identifiers.
type SendResult struct {
	MessageID   int
	ExternalRef string
}

// compiledTemplate holds parsed template with channel binding.
// Params: channel key and parsed template object.
// Returns: template metadata for dispatcher rendering.
type compiledTemplate struct {
	channel string
	body    *template.Template
}

// ChannelSender sends one outbound notification to one channel.
// Params: context and notification payload.
// Returns: channel send metadata and transport error when send fails.
type ChannelSender interface {
	Channel() string
	Send(ctx context.Context, notification domain.Notification) (SendResult, error)
}

// Dispatcher delivers notifications with configured retries/backoff.
// Params: sender list and retry policy.
// Returns: send helper for manager layer.
type Dispatcher struct {
	senders      map[string]ChannelSender
	channels     []string
	retries      map[string]config.NotifyRetry
	logger       *slog.Logger
	templates    map[string]compiledTemplate
	templateErrs map[string]error
}

// PermanentError marks delivery errors that must not be retried.
// Params: wrapped root cause.
// Returns: typed permanent error marker.
type PermanentError = permanent.Error

// MarkPermanent wraps error with non-retryable marker.
// Params: source error.
// Returns: wrapped error or nil.
func MarkPermanent(err error) error {
	return permanent.Mark(err)
}

// IsPermanent reports whether error is non-retryable.
// Params: candidate error.
// Returns: true when permanent marker is present.
func IsPermanent(err error) bool {
	return permanent.Is(err)
}

var channelSenderFactories = map[string]func(config.NotifyConfig) ChannelSender{
	config.NotifyChannelTelegram: func(cfg config.NotifyConfig) ChannelSender {
		return NewTelegramSender(cfg.Telegram)
	},
	config.NotifyChannelHTTP: func(cfg config.NotifyConfig) ChannelSender {
		return NewHTTPScenarioSender(cfg.HTTP)
	},
	config.NotifyChannelMattermost: func(cfg config.NotifyConfig) ChannelSender {
		return NewMattermostSender(cfg.Mattermost)
	},
	config.NotifyChannelJira: func(cfg config.NotifyConfig) ChannelSender {
		return NewTrackerSender(config.NotifyChannelJira, cfg.Jira)
	},
	config.NotifyChannelYouTrack: func(cfg config.NotifyConfig) ChannelSender {
		return NewTrackerSender(config.NotifyChannelYouTrack, cfg.YouTrack)
	},
}

// NewDispatcher builds notification dispatcher from enabled channels.
// Params: global notify config and optional logger.
// Returns: configured dispatcher with available senders.
func NewDispatcher(cfg config.NotifyConfig, logger *slog.Logger) *Dispatcher {
	senders := make(map[string]ChannelSender)
	retries := make(map[string]config.NotifyRetry)
	for _, channel := range config.NotifyChannelNames() {
		if !config.NotifyChannelEnabled(cfg, channel) {
			continue
		}
		sender := newSenderForChannel(channel, cfg)
		if sender == nil {
			continue
		}
		senders[channel] = sender
		retries[channel] = config.NotifyChannelRetry(cfg, channel)
	}
	channels := make([]string, 0, len(senders))
	for channel := range senders {
		channels = append(channels, channel)
	}
	sort.Strings(channels)
	compiledTemplates, templateErrs := buildTemplateSet(cfg)
	return &Dispatcher{
		senders:      senders,
		channels:     channels,
		retries:      retries,
		logger:       logger,
		templates:    compiledTemplates,
		templateErrs: templateErrs,
	}
}

// newSenderForChannel builds transport sender implementation for one channel key.
// Params: normalized channel key and full notify config.
// Returns: channel sender or nil when channel is unknown.
func newSenderForChannel(channel string, cfg config.NotifyConfig) ChannelSender {
	factory := channelSenderFactories[channel]
	if factory == nil {
		return nil
	}
	return factory(cfg)
}

// Send sends one notification to channel/template with retry policy.
// Params: destination channel, template name, and notification payload.
// Returns: channel metadata and final error after retries.
func (d *Dispatcher) Send(ctx context.Context, channel, templateName string, notification domain.Notification) (SendResult, error) {
	sender, ok := d.senders[channel]
	if !ok {
		return SendResult{}, MarkPermanent(fmt.Errorf("notify channel %q is not configured", channel))
	}
	compiled, err := d.resolveTemplate(templateName, channel)
	if err != nil {
		return SendResult{}, MarkPermanent(err)
	}

	renderedNotification := notification
	renderedNotification.Channel = channel
	renderedMessage, err := d.renderMessage(compiled, renderedNotification)
	if err != nil {
		return SendResult{}, MarkPermanent(err)
	}
	renderedNotification.Message = renderedMessage

	return d.sendWithRetry(ctx, sender, renderedNotification, d.retries[channel])
}

// sendWithRetry sends one notification with channel-specific retry policy.
// Params: sender, payload, and retry policy for the sender channel.
// Returns: channel metadata and final error after retries.
func (d *Dispatcher) sendWithRetry(ctx context.Context, sender ChannelSender, notification domain.Notification, retry config.NotifyRetry) (SendResult, error) {
	if !retry.Enabled {
		return sender.Send(ctx, notification)
	}

	attempt := 0
	backoff := time.Duration(retry.InitialMS) * time.Millisecond
	maxBackoff := time.Duration(retry.MaxMS) * time.Millisecond
	var timer *time.Timer
	defer func() {
		stopAndDrainTimer(timer)
	}()

	for {
		attempt++
		result, err := sender.Send(ctx, notification)
		if err == nil {
			if retry.LogEachAttempt && attempt > 1 && d.logger != nil {
				d.logger.Info("notify send recovered after retries", "channel", sender.Channel(), "attempt", attempt)
			}
			return result, nil
		}
		if retry.LogEachAttempt && d.logger != nil {
			d.logger.Warn("notify send attempt failed", "channel", sender.Channel(), "attempt", attempt, "error", err.Error())
		}
		if IsPermanent(err) {
			return SendResult{}, err
		}

		if retry.MaxAttempts > 0 && attempt >= retry.MaxAttempts {
			return SendResult{}, fmt.Errorf("channel %s failed after %d attempts: %w", sender.Channel(), attempt, err)
		}

		if timer == nil {
			timer = time.NewTimer(backoff)
		} else {
			stopAndDrainTimer(timer)
			timer.Reset(backoff)
		}
		select {
		case <-ctx.Done():
			return SendResult{}, ctx.Err()
		case <-timer.C:
		}

		if strings.EqualFold(retry.Backoff, "exponential") {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// stopAndDrainTimer safely stops timer and drains stale tick when needed.
// Params: timer pointer that may be nil.
// Returns: timer stopped/drained best-effort.
func stopAndDrainTimer(timer *time.Timer) {
	if timer == nil {
		return
	}
	if timer.Stop() {
		return
	}
	select {
	case <-timer.C:
	default:
	}
}

// Channels returns configured channel list.
// Params: none.
// Returns: deterministic sender keys.
func (d *Dispatcher) Channels() []string {
	return d.channels
}

// resolveTemplate selects compiled template by name and validates channel binding.
// Params: template name from rule route and destination channel.
// Returns: compiled template for rendering.
func (d *Dispatcher) resolveTemplate(templateName, channel string) (compiledTemplate, error) {
	name := strings.ToLower(strings.TrimSpace(templateName))
	if name == "" {
		return compiledTemplate{}, errors.New("notify template name is required")
	}
	key := templateKey(channel, name)
	if d.templateErrs != nil {
		if err, ok := d.templateErrs[key]; ok && err != nil {
			return compiledTemplate{}, fmt.Errorf("notify template %q is invalid: %w", templateName, err)
		}
	}
	compiled, ok := d.templates[key]
	if !ok || compiled.body == nil {
		return compiledTemplate{}, fmt.Errorf("notify template %q is not configured", templateName)
	}
	if compiled.channel != channel {
		return compiledTemplate{}, fmt.Errorf("notify template %q is bound to channel %q, not %q", templateName, compiled.channel, channel)
	}
	return compiled, nil
}

// renderMessage applies shared template processing for the channel.
// Params: compiled template and outbound notification model.
// Returns: rendered message body.
func (d *Dispatcher) renderMessage(entry compiledTemplate, notification domain.Notification) (string, error) {
	var rendered strings.Builder
	if err := entry.body.Execute(&rendered, notification); err != nil {
		return "", fmt.Errorf("render notify template for channel %q: %w", entry.channel, err)
	}
	return rendered.String(), nil
}

// buildTemplateSet compiles named templates from channel-scoped notify config.
// Params: notify config snapshot.
// Returns: compiled template lookup and parse errors by template key.
func buildTemplateSet(cfg config.NotifyConfig) (map[string]compiledTemplate, map[string]error) {
	compiled := make(map[string]compiledTemplate)
	parseErrs := make(map[string]error)
	for _, channel := range config.NotifyChannelNames() {
		collectCompiledTemplates(compiled, parseErrs, channel, config.NotifyChannelTemplates(cfg, channel))
	}
	return compiled, parseErrs
}

// collectCompiledTemplates compiles one channel template list into dispatcher map.
// Params: destination maps, channel key, and template list.
// Returns: compiled template side-effects into destination maps.
func collectCompiledTemplates(
	compiled map[string]compiledTemplate,
	parseErrs map[string]error,
	channel string,
	templates []config.NamedTemplateConfig,
) {
	for _, templateConfig := range templates {
		name := strings.ToLower(strings.TrimSpace(templateConfig.Name))
		if name == "" {
			continue
		}
		key := templateKey(channel, name)
		entry, err := parseTemplate("notify."+channel+".name-template."+name+".message", templateConfig.Message)
		if err != nil {
			parseErrs[key] = err
		}
		compiled[key] = compiledTemplate{
			channel: channel,
			body:    entry,
		}
	}
}

// templateKey builds deterministic template lookup key by channel+template.
// Params: normalized channel and template names.
// Returns: unique dispatcher lookup key.
func templateKey(channel, name string) string {
	return strings.ToLower(strings.TrimSpace(channel)) + "/" + strings.ToLower(strings.TrimSpace(name))
}

// parseTemplate compiles one text/template expression for notifications.
// Params: template name and body.
// Returns: compiled template or parse error.
func parseTemplate(name, body string) (*template.Template, error) {
	return templatefmt.ParseNotificationTemplate(name, body)
}

// TelegramSender sends notifications to Telegram Bot API.
// Params: bot token, chat id, and base URL.
// Returns: Telegram channel sender.
type TelegramSender struct {
	client       *tgbot.Bot
	chatID       any
	activeChatID any
	initErr      error
}

// NewTelegramSender creates Telegram sender with HTTP client.
// Params: Telegram notifier config.
// Returns: initialized sender.
func NewTelegramSender(cfg config.TelegramNotifier) *TelegramSender {
	sender := &TelegramSender{
		chatID: normalizeChatID(cfg.ChatID),
	}
	if strings.TrimSpace(cfg.ActiveChatID) != "" {
		sender.activeChatID = normalizeChatID(cfg.ActiveChatID)
	}

	if strings.TrimSpace(cfg.BotToken) == "" {
		sender.initErr = errors.New("telegram bot token is required")
		return sender
	}
	if strings.TrimSpace(cfg.ChatID) == "" {
		sender.initErr = errors.New("telegram chat_id is required")
		return sender
	}

	options := []tgbot.Option{
		tgbot.WithSkipGetMe(),
		tgbot.WithServerURL(strings.TrimRight(cfg.APIBase, "/")),
	}
	botClient, err := tgbot.New(cfg.BotToken, options...)
	if err != nil {
		sender.initErr = fmt.Errorf("init telegram bot: %w", err)
		return sender
	}
	sender.client = botClient
	return sender
}

// Channel returns sender channel name.
// Params: none.
// Returns: static channel key.
func (s *TelegramSender) Channel() string {
	return "telegram"
}

// Send posts one notification message to Telegram chat.
// Params: context and notification payload.
// Returns: transport or HTTP error.
func (s *TelegramSender) Send(ctx context.Context, notification domain.Notification) (SendResult, error) {
	if s.initErr != nil {
		return SendResult{}, s.initErr
	}
	if s.client == nil {
		return SendResult{}, errors.New("telegram client is not initialized")
	}

	routeMode := config.NormalizeNotifyRouteMode(notification.RouteMode)
	chatID := s.chatID
	if routeMode == config.NotifyRouteModeActiveOnly && s.activeChatID != nil {
		chatID = s.activeChatID
	}

	if routeMode == config.NotifyRouteModeActiveOnly {
		return s.sendActiveOnly(ctx, chatID, notification)
	}
	return s.sendHistory(ctx, chatID, notification)
}

// sendHistory sends standard Telegram history notifications.
// Params: context, target chat id, and payload.
// Returns: send result with message id.
func (s *TelegramSender) sendHistory(ctx context.Context, chatID any, notification domain.Notification) (SendResult, error) {
	request := &tgbot.SendMessageParams{
		ChatID:    chatID,
		Text:      notification.Message,
		ParseMode: tgmodels.ParseModeHTML,
	}
	if notification.ReplyToMessageID != nil && *notification.ReplyToMessageID > 0 {
		request.ReplyParameters = &tgmodels.ReplyParameters{
			MessageID: *notification.ReplyToMessageID,
		}
	}

	sent, err := s.client.SendMessage(ctx, request)
	if err != nil {
		return SendResult{}, fmt.Errorf("telegram send: %w", err)
	}
	if sent == nil || sent.ID <= 0 {
		return SendResult{}, errors.New("telegram send returned empty message id")
	}
	return SendResult{MessageID: sent.ID}, nil
}

// sendActiveOnly maintains one active Telegram message per route.
// Params: context, target chat id, and payload.
// Returns: message metadata/external ref for create/update/delete lifecycle.
func (s *TelegramSender) sendActiveOnly(ctx context.Context, chatID any, notification domain.Notification) (SendResult, error) {
	switch notification.State {
	case domain.AlertStateResolved:
		messageID, err := parseTelegramMessageID(notification.ExternalRef)
		if err != nil {
			return SendResult{}, fmt.Errorf("telegram active delete: %w", err)
		}
		ok, err := s.client.DeleteMessage(ctx, &tgbot.DeleteMessageParams{
			ChatID:    chatID,
			MessageID: messageID,
		})
		if err != nil {
			return SendResult{}, fmt.Errorf("telegram delete: %w", err)
		}
		if !ok {
			return SendResult{}, errors.New("telegram delete returned false")
		}
		return SendResult{}, nil
	default:
		if strings.TrimSpace(notification.ExternalRef) != "" {
			messageID, err := parseTelegramMessageID(notification.ExternalRef)
			if err != nil {
				return SendResult{}, fmt.Errorf("telegram active edit: %w", err)
			}
			edited, err := s.client.EditMessageText(ctx, &tgbot.EditMessageTextParams{
				ChatID:    chatID,
				MessageID: messageID,
				Text:      notification.Message,
				ParseMode: tgmodels.ParseModeHTML,
			})
			if err != nil {
				return SendResult{}, fmt.Errorf("telegram edit: %w", err)
			}
			if edited == nil || edited.ID <= 0 {
				return SendResult{}, errors.New("telegram edit returned empty message")
			}
			return SendResult{MessageID: messageID, ExternalRef: strconv.Itoa(messageID)}, nil
		}

		sent, err := s.client.SendMessage(ctx, &tgbot.SendMessageParams{
			ChatID:    chatID,
			Text:      notification.Message,
			ParseMode: tgmodels.ParseModeHTML,
		})
		if err != nil {
			return SendResult{}, fmt.Errorf("telegram send: %w", err)
		}
		if sent == nil || sent.ID <= 0 {
			return SendResult{}, errors.New("telegram send returned empty message id")
		}
		return SendResult{MessageID: sent.ID, ExternalRef: strconv.Itoa(sent.ID)}, nil
	}
}

// parseTelegramMessageID parses positive message id from external reference.
// Params: stored external reference string.
// Returns: parsed message id or validation error.
func parseTelegramMessageID(value string) (int, error) {
	messageID, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || messageID <= 0 {
		return 0, fmt.Errorf("invalid telegram message id %q", value)
	}
	return messageID, nil
}

// normalizeChatID converts numeric chat IDs to int64 and keeps non-numeric IDs as string.
// Params: configured chat ID value from TOML.
// Returns: Telegram API chat id union value.
func normalizeChatID(raw string) any {
	trimmed := strings.TrimSpace(raw)
	if numeric, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
		return numeric
	}
	return trimmed
}

// HTTPScenarioSender posts notification payload to configured HTTP endpoint.
// Params: endpoint URL, method, timeout, and headers.
// Returns: generic HTTP sender.
type HTTPScenarioSender struct {
	cfg    config.HTTPNotifier
	client *http.Client
}

// NewHTTPScenarioSender creates generic HTTP sender.
// Params: HTTP notifier config.
// Returns: initialized sender.
func NewHTTPScenarioSender(cfg config.HTTPNotifier) *HTTPScenarioSender {
	return &HTTPScenarioSender{
		cfg: cfg,
		client: &http.Client{
			Timeout: time.Duration(cfg.TimeoutSec) * time.Second,
		},
	}
}

// Channel returns sender channel name.
// Params: none.
// Returns: static channel key.
func (s *HTTPScenarioSender) Channel() string {
	return "http"
}

// Send delivers JSON payload to configured HTTP endpoint.
// Params: context and notification payload.
// Returns: transport or HTTP error.
func (s *HTTPScenarioSender) Send(ctx context.Context, notification domain.Notification) (SendResult, error) {
	method := strings.ToUpper(strings.TrimSpace(s.cfg.Method))
	if method == "" {
		method = http.MethodPost
	}
	request, err := buildJSONRequest(ctx, method, s.cfg.URL, notification, "http notify")
	if err != nil {
		return SendResult{}, err
	}
	for key, value := range s.cfg.Headers {
		request.Header.Set(key, value)
	}

	if _, err := executeRequest(s.client, request, "http notify", nil); err != nil {
		return SendResult{}, err
	}
	return SendResult{}, nil
}

// MattermostSender posts notifications to Mattermost API posts endpoint.
// Params: API base URL, bot token, and channel id from config.
// Returns: Mattermost sender.
type MattermostSender struct {
	cfg    config.MattermostConfig
	client *http.Client
}

// NewMattermostSender creates Mattermost webhook sender.
// Params: Mattermost config.
// Returns: initialized sender.
func NewMattermostSender(cfg config.MattermostConfig) *MattermostSender {
	timeoutSec := cfg.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 10
	}
	return &MattermostSender{
		cfg:    cfg,
		client: &http.Client{Timeout: time.Duration(timeoutSec) * time.Second},
	}
}

// Channel returns sender channel name.
// Params: none.
// Returns: static channel key.
func (s *MattermostSender) Channel() string {
	return "mattermost"
}

// Send posts one formatted message to Mattermost API.
// Params: context and notification payload.
// Returns: transport or HTTP error.
func (s *MattermostSender) Send(ctx context.Context, notification domain.Notification) (SendResult, error) {
	mode := config.NormalizeNotifyRouteMode(notification.RouteMode)
	if mode == config.NotifyRouteModeActiveOnly {
		return s.sendActiveOnly(ctx, notification)
	}
	return s.sendHistory(ctx, notification)
}

// sendHistory posts regular Mattermost history notifications.
// Params: context and payload.
// Returns: send result (firing returns root post id as external ref).
func (s *MattermostSender) sendHistory(ctx context.Context, notification domain.Notification) (SendResult, error) {
	rootID := strings.TrimSpace(notification.ExternalRef)
	if notification.State == domain.AlertStateResolved && rootID == "" {
		return SendResult{}, errors.New("mattermost resolved requires external_ref")
	}

	payload := struct {
		ChannelID string `json:"channel_id"`
		Message   string `json:"message"`
		RootID    string `json:"root_id,omitempty"`
	}{
		ChannelID: strings.TrimSpace(s.cfg.ChannelID),
		Message:   notification.Message,
		RootID:    rootID,
	}
	decoded, err := s.createPost(ctx, payload)
	if err != nil {
		return SendResult{}, err
	}
	if notification.State == domain.AlertStateFiring {
		return SendResult{ExternalRef: decoded.ID}, nil
	}
	return SendResult{}, nil
}

// sendActiveOnly keeps one active Mattermost post per route.
// Params: context and payload.
// Returns: send result with active post id for create/update.
func (s *MattermostSender) sendActiveOnly(ctx context.Context, notification domain.Notification) (SendResult, error) {
	activePostID := strings.TrimSpace(notification.ExternalRef)
	switch notification.State {
	case domain.AlertStateResolved:
		if activePostID == "" {
			return SendResult{}, errors.New("mattermost active resolved requires external_ref")
		}
		if err := s.deletePost(ctx, activePostID); err != nil {
			return SendResult{}, err
		}
		return SendResult{}, nil
	default:
		if activePostID == "" {
			channelID := strings.TrimSpace(s.cfg.ActiveChannelID)
			if channelID == "" {
				channelID = strings.TrimSpace(s.cfg.ChannelID)
			}
			decoded, err := s.createPost(ctx, struct {
				ChannelID string `json:"channel_id"`
				Message   string `json:"message"`
			}{
				ChannelID: channelID,
				Message:   notification.Message,
			})
			if err != nil {
				return SendResult{}, err
			}
			return SendResult{ExternalRef: decoded.ID}, nil
		}
		if err := s.updatePost(ctx, activePostID, notification.Message); err != nil {
			return SendResult{}, err
		}
		return SendResult{ExternalRef: activePostID}, nil
	}
}

// createPost creates one Mattermost post and returns decoded response.
// Params: context and payload struct.
// Returns: created post response body.
func (s *MattermostSender) createPost(ctx context.Context, payload any) (struct {
	ID string `json:"id"`
}, error) {
	var empty struct {
		ID string `json:"id"`
	}
	request, err := buildJSONRequest(ctx, http.MethodPost, s.postsEndpoint(""), payload, "mattermost")
	if err != nil {
		return empty, err
	}
	s.applyAuth(request)
	responseBody, err := executeRequest(s.client, request, "mattermost", nil)
	if err != nil {
		return empty, err
	}
	var decoded struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return empty, fmt.Errorf("decode mattermost response: %w", err)
	}
	if strings.TrimSpace(decoded.ID) == "" {
		return empty, errors.New("mattermost response missing id")
	}
	return decoded, nil
}

// updatePost updates existing Mattermost post text.
// Params: context, post id, and new message body.
// Returns: transport or HTTP error.
func (s *MattermostSender) updatePost(ctx context.Context, postID, message string) error {
	payload := struct {
		ID      string `json:"id"`
		Message string `json:"message"`
	}{
		ID:      postID,
		Message: message,
	}
	request, err := buildJSONRequest(ctx, http.MethodPut, s.postsEndpoint(postID), payload, "mattermost update")
	if err != nil {
		return err
	}
	s.applyAuth(request)
	if _, err := executeRequest(s.client, request, "mattermost", nil); err != nil {
		return err
	}
	return nil
}

// deletePost deletes one Mattermost post by id.
// Params: context and post id.
// Returns: transport or HTTP error.
func (s *MattermostSender) deletePost(ctx context.Context, postID string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, s.postsEndpoint(postID), nil)
	if err != nil {
		return fmt.Errorf("build mattermost delete request: %w", err)
	}
	s.applyAuth(request)
	if _, err := executeRequest(s.client, request, "mattermost", nil); err != nil {
		return err
	}
	return nil
}

// postsEndpoint returns Mattermost posts endpoint with optional post id.
// Params: optional post id.
// Returns: API endpoint URL.
func (s *MattermostSender) postsEndpoint(postID string) string {
	base := strings.TrimRight(strings.TrimSpace(s.cfg.BaseURL), "/") + "/api/v4/posts"
	if strings.TrimSpace(postID) == "" {
		return base
	}
	return base + "/" + url.PathEscape(postID)
}

// applyAuth injects bot bearer token into Mattermost request.
// Params: mutable HTTP request.
// Returns: request mutated in place.
func (s *MattermostSender) applyAuth(request *http.Request) {
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(s.cfg.BotToken))
}

// buildJSONRequest builds HTTP request with JSON-encoded payload when provided.
// Params: context, method, endpoint, optional payload, and prefix for wrapped errors.
// Returns: initialized request with `Content-Type: application/json` when payload is present.
func buildJSONRequest(ctx context.Context, method, endpoint string, payload any, prefix string) (*http.Request, error) {
	var bodyReader io.Reader
	if payload != nil {
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("encode %s payload: %w", prefix, err)
		}
		bodyReader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("build %s request: %w", prefix, err)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return request, nil
}

// executeRequest sends request and validates response status.
// Params: HTTP client, request, error prefix, and optional accepted status set (nil => any 2xx).
// Returns: full response body for successful status.
func executeRequest(client *http.Client, request *http.Request, prefix string, successStatus map[int]struct{}) ([]byte, error) {
	if client == nil {
		return nil, fmt.Errorf("%s client is nil", prefix)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%s send: %w", prefix, err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("%s read response: %w", prefix, err)
	}
	if !isHTTPStatusAccepted(response.StatusCode, successStatus) {
		return nil, formatHTTPStatusError(prefix, response.StatusCode, body)
	}
	return body, nil
}

// isHTTPStatusAccepted checks status against optional explicit allow list.
// Params: response status code and optional allowed statuses.
// Returns: true when status is accepted.
func isHTTPStatusAccepted(statusCode int, successStatus map[int]struct{}) bool {
	if len(successStatus) == 0 {
		return statusCode >= 200 && statusCode < 300
	}
	_, ok := successStatus[statusCode]
	return ok
}

// formatHTTPStatusError formats status/body failure details.
// Params: error prefix, status code, and response body bytes.
// Returns: status-only or status+trimmed-body error.
func formatHTTPStatusError(prefix string, statusCode int, body []byte) error {
	trimmedBody := strings.TrimSpace(string(body))
	if trimmedBody == "" {
		return fmt.Errorf("%s status=%d", prefix, statusCode)
	}
	return fmt.Errorf("%s status=%d body=%s", prefix, statusCode, trimmedBody)
}

// TrackerSender sends tracker lifecycle actions (create/resolve) over HTTP.
// Params: tracker channel and tracker notifier config.
// Returns: sender with compiled request templates.
type TrackerSender struct {
	channel string
	cfg     config.TrackerNotifier
	client  *http.Client

	create  trackerActionRuntime
	resolve trackerActionRuntime
	initErr error
}

// trackerActionRuntime stores compiled request templates for one tracker action.
// Params: method/path/body/header templates, success statuses, and response ref path.
// Returns: runtime action descriptor.
type trackerActionRuntime struct {
	method         string
	pathTemplate   *template.Template
	bodyTemplate   *template.Template
	headers        map[string]*template.Template
	successStatus  map[int]struct{}
	responseRefKey string
}

// NewTrackerSender creates tracker HTTP sender for one channel.
// Params: tracker channel key and transport config.
// Returns: initialized sender with compiled action templates.
func NewTrackerSender(channel string, cfg config.TrackerNotifier) *TrackerSender {
	sender := &TrackerSender{
		channel: strings.ToLower(strings.TrimSpace(channel)),
		cfg:     cfg,
		client: &http.Client{
			Timeout: time.Duration(cfg.TimeoutSec) * time.Second,
		},
	}
	createAction, err := buildTrackerActionRuntime(channel, "create", cfg.Create)
	if err != nil {
		sender.initErr = err
		return sender
	}
	resolveAction, err := buildTrackerActionRuntime(channel, "resolve", cfg.Resolve)
	if err != nil {
		sender.initErr = err
		return sender
	}
	sender.create = createAction
	sender.resolve = resolveAction
	return sender
}

// Channel returns sender channel name.
// Params: none.
// Returns: configured tracker channel key.
func (s *TrackerSender) Channel() string {
	return s.channel
}

// Send executes tracker lifecycle action for notification state.
// Params: context and rendered notification payload.
// Returns: external issue reference for create action.
func (s *TrackerSender) Send(ctx context.Context, notification domain.Notification) (SendResult, error) {
	if s.initErr != nil {
		return SendResult{}, s.initErr
	}

	switch notification.State {
	case domain.AlertStateFiring:
		if strings.TrimSpace(notification.ExternalRef) != "" {
			return SendResult{ExternalRef: notification.ExternalRef}, nil
		}
		result, err := s.executeAction(ctx, s.create, notification)
		if err != nil {
			return SendResult{}, err
		}
		if strings.TrimSpace(result.ExternalRef) == "" {
			return SendResult{}, errors.New("tracker create response does not contain external reference")
		}
		return result, nil
	case domain.AlertStateResolved:
		if strings.TrimSpace(notification.ExternalRef) == "" {
			return SendResult{}, errors.New("tracker resolve requires external reference")
		}
		_, err := s.executeAction(ctx, s.resolve, notification)
		if err != nil {
			return SendResult{}, err
		}
		return SendResult{ExternalRef: notification.ExternalRef}, nil
	default:
		return SendResult{}, nil
	}
}

// executeAction renders and sends one tracker HTTP action.
// Params: action descriptor and notification payload.
// Returns: send result with extracted external reference when configured.
func (s *TrackerSender) executeAction(ctx context.Context, action trackerActionRuntime, notification domain.Notification) (SendResult, error) {
	pathValue, err := executeStringTemplate(action.pathTemplate, notification)
	if err != nil {
		return SendResult{}, fmt.Errorf("tracker %s render path: %w", s.channel, err)
	}
	targetURL, err := resolveTrackerURL(s.cfg.BaseURL, pathValue)
	if err != nil {
		return SendResult{}, fmt.Errorf("tracker %s resolve url: %w", s.channel, err)
	}

	var bodyReader io.Reader
	contentTypeSet := false
	if action.bodyTemplate != nil {
		bodyValue, err := executeStringTemplate(action.bodyTemplate, notification)
		if err != nil {
			return SendResult{}, fmt.Errorf("tracker %s render body: %w", s.channel, err)
		}
		if strings.TrimSpace(bodyValue) != "" {
			bodyReader = strings.NewReader(bodyValue)
		}
	}

	request, err := http.NewRequestWithContext(ctx, action.method, targetURL, bodyReader)
	if err != nil {
		return SendResult{}, fmt.Errorf("tracker %s build request: %w", s.channel, err)
	}

	for key, tmpl := range action.headers {
		value, headerErr := executeStringTemplate(tmpl, notification)
		if headerErr != nil {
			return SendResult{}, fmt.Errorf("tracker %s render header %q: %w", s.channel, key, headerErr)
		}
		request.Header.Set(key, value)
		if strings.EqualFold(key, "content-type") {
			contentTypeSet = true
		}
	}
	if bodyReader != nil && !contentTypeSet {
		request.Header.Set("Content-Type", "application/json")
	}
	applyTrackerAuth(request, s.cfg.Auth)

	responseBody, err := executeRequest(s.client, request, "tracker "+s.channel, action.successStatus)
	if err != nil {
		return SendResult{}, err
	}

	if strings.TrimSpace(action.responseRefKey) == "" {
		return SendResult{}, nil
	}
	ref, err := extractJSONPathString(responseBody, action.responseRefKey)
	if err != nil {
		return SendResult{}, fmt.Errorf("tracker %s parse response ref %q: %w", s.channel, action.responseRefKey, err)
	}
	return SendResult{ExternalRef: ref}, nil
}

// buildTrackerActionRuntime compiles one tracker action template set.
// Params: channel/action labels and action config.
// Returns: compiled action runtime or parse error.
func buildTrackerActionRuntime(channel, actionName string, cfg config.TrackerActionConfig) (trackerActionRuntime, error) {
	pathTemplate, err := parseTemplate("notify."+channel+"."+actionName+".path", cfg.Path)
	if err != nil {
		return trackerActionRuntime{}, err
	}

	var bodyTemplate *template.Template
	if strings.TrimSpace(cfg.BodyTemplate) != "" {
		bodyTemplate, err = parseTemplate("notify."+channel+"."+actionName+".body_template", cfg.BodyTemplate)
		if err != nil {
			return trackerActionRuntime{}, err
		}
	}

	headerTemplates := make(map[string]*template.Template, len(cfg.Headers))
	for key, rawValue := range cfg.Headers {
		templateValue, parseErr := parseTemplate("notify."+channel+"."+actionName+".headers."+key, rawValue)
		if parseErr != nil {
			return trackerActionRuntime{}, parseErr
		}
		headerTemplates[key] = templateValue
	}

	successStatus := make(map[int]struct{}, len(cfg.SuccessStatus))
	for _, statusCode := range cfg.SuccessStatus {
		successStatus[statusCode] = struct{}{}
	}

	return trackerActionRuntime{
		method:         strings.ToUpper(strings.TrimSpace(cfg.Method)),
		pathTemplate:   pathTemplate,
		bodyTemplate:   bodyTemplate,
		headers:        headerTemplates,
		successStatus:  successStatus,
		responseRefKey: strings.TrimSpace(cfg.RefJSONPath),
	}, nil
}

// executeStringTemplate renders one compiled template into string.
// Params: compiled template and data context.
// Returns: rendered string.
func executeStringTemplate(tmpl *template.Template, payload any) (string, error) {
	if tmpl == nil {
		return "", nil
	}
	var builder strings.Builder
	if err := tmpl.Execute(&builder, payload); err != nil {
		return "", err
	}
	return builder.String(), nil
}

// resolveTrackerURL combines base URL with rendered action path.
// Params: base URL and rendered path/url.
// Returns: absolute request URL.
func resolveTrackerURL(baseURL, pathOrURL string) (string, error) {
	trimmedPath := strings.TrimSpace(pathOrURL)
	if trimmedPath == "" {
		return "", errors.New("empty request path")
	}
	if strings.HasPrefix(trimmedPath, "http://") || strings.HasPrefix(trimmedPath, "https://") {
		if _, err := url.Parse(trimmedPath); err != nil {
			return "", err
		}
		return trimmedPath, nil
	}
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return "", errors.New("empty base_url")
	}
	if strings.HasPrefix(trimmedPath, "/") {
		return base + trimmedPath, nil
	}
	return base + "/" + trimmedPath, nil
}

// applyTrackerAuth injects configured auth headers into tracker request.
// Params: mutable request pointer and auth config.
// Returns: request mutated in place.
func applyTrackerAuth(request *http.Request, cfg config.TrackerAuthConfig) {
	authType := strings.ToLower(strings.TrimSpace(cfg.Type))
	switch authType {
	case "", "none":
		return
	case "bearer":
		prefix := strings.TrimSpace(cfg.Prefix)
		if prefix == "" {
			prefix = "Bearer"
		}
		request.Header.Set("Authorization", prefix+" "+strings.TrimSpace(cfg.Token))
	case "basic":
		credentials := strings.TrimSpace(cfg.Username) + ":" + strings.TrimSpace(cfg.Password)
		encoded := base64.StdEncoding.EncodeToString([]byte(credentials))
		request.Header.Set("Authorization", "Basic "+encoded)
	case "header":
		header := strings.TrimSpace(cfg.Header)
		if header == "" {
			return
		}
		prefix := strings.TrimSpace(cfg.Prefix)
		token := strings.TrimSpace(cfg.Token)
		if prefix != "" {
			request.Header.Set(header, prefix+" "+token)
			return
		}
		request.Header.Set(header, token)
	}
}

// extractJSONPathString extracts string-like field by dotted JSON path.
// Params: raw JSON body and dotted path (e.g. "result.key").
// Returns: extracted value converted to string.
func extractJSONPathString(body []byte, path string) (string, error) {
	trimmedPath := strings.TrimSpace(path)
	if trimmedPath == "" {
		return "", errors.New("empty json path")
	}
	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		return "", err
	}

	current := root
	parts := strings.Split(trimmedPath, ".")
	for _, part := range parts {
		token := strings.TrimSpace(part)
		if token == "" {
			return "", errors.New("json path contains empty segment")
		}
		switch typed := current.(type) {
		case map[string]any:
			next, ok := typed[token]
			if !ok {
				return "", fmt.Errorf("path segment %q not found", token)
			}
			current = next
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil {
				return "", fmt.Errorf("path segment %q is not array index", token)
			}
			if index < 0 || index >= len(typed) {
				return "", fmt.Errorf("array index %d out of bounds", index)
			}
			current = typed[index]
		default:
			return "", fmt.Errorf("path segment %q not reachable from %T", token, current)
		}
	}

	switch typed := current.(type) {
	case string:
		value := strings.TrimSpace(typed)
		if value == "" {
			return "", errors.New("json path resolved to empty string")
		}
		return value, nil
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64), nil
	case bool:
		return strconv.FormatBool(typed), nil
	default:
		return "", fmt.Errorf("json path resolved to unsupported type %T", current)
	}
}
