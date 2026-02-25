package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"alerting/internal/templatefmt"

	"github.com/pelletier/go-toml/v2"
)

const (
	defaultHTTPListen          = ":8080"
	defaultHealthPath          = "/healthz"
	defaultReadyPath           = "/readyz"
	defaultIngestPath          = "/ingest"
	defaultNATSSubject         = "alerting.events"
	defaultNATSIngestStream    = "ALERTING_EVENTS"
	defaultNATSIngestConsumer  = "alerting-ingest"
	defaultNATSIngestGroup     = "alerting-workers"
	defaultNATSIngestWorkers   = 1
	defaultNATSAckWaitSec      = 30
	defaultNATSNackDelayMS     = 1000
	defaultNATSMaxDeliver      = -1
	defaultNATSMaxAckPending   = 2048
	defaultNATSURL             = "nats://127.0.0.1:4222"
	defaultNATSTickBucket      = "tick"
	defaultNATSDataBucket      = "data"
	defaultDeleteConsumer      = "alerting-resolve"
	defaultDeleteDeliver       = "alerting-resolve"
	defaultReloadSeconds       = 5
	defaultResolveScanSeconds  = 1
	defaultNotifyRepeatSeconds = 300
	defaultPendingDelaySeconds = 300

	// ServiceModeNATS keeps NATS-backed state/ingest settings.
	ServiceModeNATS = "nats"
	// ServiceModeSingle keeps single-instance mode without NATS dependencies.
	ServiceModeSingle = "single"

	// NotifyChannelTelegram identifies Telegram transport.
	NotifyChannelTelegram = "telegram"
	// NotifyChannelHTTP identifies generic HTTP transport.
	NotifyChannelHTTP = "http"
	// NotifyChannelMattermost identifies Mattermost transport.
	NotifyChannelMattermost = "mattermost"
	// NotifyChannelJira identifies Jira tracker transport.
	NotifyChannelJira = "jira"
	// NotifyChannelYouTrack identifies YouTrack tracker transport.
	NotifyChannelYouTrack = "youtrack"

	// NotifyRouteModeHistory keeps normal message history flow.
	NotifyRouteModeHistory = "history"
	// NotifyRouteModeActiveOnly keeps one active message and removes it on resolve.
	NotifyRouteModeActiveOnly = "active_only"
)

var (
	notifyChannelOrder = []string{
		NotifyChannelTelegram,
		NotifyChannelHTTP,
		NotifyChannelMattermost,
		NotifyChannelJira,
		NotifyChannelYouTrack,
	}
	notifyChannelRegistry = map[string]notifyChannelDescriptor{
		NotifyChannelTelegram: {
			enabled: func(cfg NotifyConfig) bool { return cfg.Telegram.Enabled },
			retry:   func(cfg NotifyConfig) NotifyRetry { return cfg.Telegram.Retry },
			templates: func(cfg NotifyConfig) []NamedTemplateConfig {
				return cfg.Telegram.NameTemplate
			},
		},
		NotifyChannelHTTP: {
			enabled: func(cfg NotifyConfig) bool { return cfg.HTTP.Enabled },
			retry:   func(cfg NotifyConfig) NotifyRetry { return cfg.HTTP.Retry },
			templates: func(cfg NotifyConfig) []NamedTemplateConfig {
				return cfg.HTTP.NameTemplate
			},
		},
		NotifyChannelMattermost: {
			enabled: func(cfg NotifyConfig) bool { return cfg.Mattermost.Enabled },
			retry:   func(cfg NotifyConfig) NotifyRetry { return cfg.Mattermost.Retry },
			templates: func(cfg NotifyConfig) []NamedTemplateConfig {
				return cfg.Mattermost.NameTemplate
			},
		},
		NotifyChannelJira: {
			enabled: func(cfg NotifyConfig) bool { return cfg.Jira.Enabled },
			retry:   func(cfg NotifyConfig) NotifyRetry { return cfg.Jira.Retry },
			templates: func(cfg NotifyConfig) []NamedTemplateConfig {
				return cfg.Jira.NameTemplate
			},
		},
		NotifyChannelYouTrack: {
			enabled: func(cfg NotifyConfig) bool { return cfg.YouTrack.Enabled },
			retry:   func(cfg NotifyConfig) NotifyRetry { return cfg.YouTrack.Retry },
			templates: func(cfg NotifyConfig) []NamedTemplateConfig {
				return cfg.YouTrack.NameTemplate
			},
		},
	}
	supportedValuePredicateOps = map[string]map[string]struct{}{
		"n": {
			"==": {},
			"!=": {},
			">":  {},
			">=": {},
			"<":  {},
			"<=": {},
		},
		"s": {
			"==":     {},
			"!=":     {},
			"in":     {},
			"prefix": {},
			"match":  {},
			"*":      {},
		},
		"b": {
			"==": {},
			"!=": {},
		},
	}
	legacyRuleArrayPattern                = regexp.MustCompile(`(?m)^\s*\[\[\s*rule\s*\]\]`)
	unsupportedStatePattern               = regexp.MustCompile(`(?m)^\s*\[\[?\s*state(?:\.[^\]\s]+)*\s*\]\]?`)
	unsupportedIngestNATSFixedKeysPattern = regexp.MustCompile(`(?mi)^\s*(?:subject|stream|consumer_name|deliver_group)\s*=`)
	unsupportedNotifyQueueURLPattern      = regexp.MustCompile(`(?si)\[\s*notify\.queue\s*\][^\[]*\burl\s*=`)
	unsupportedNotifyQueueDLQTablePattern = regexp.MustCompile(`(?mi)^\s*\[\s*notify\.queue\.dlq\s*\]`)
)

// notifyChannelDescriptor stores generic accessors for one notify transport.
// Params: config readers for enabled/retry/templates fields.
// Returns: channel metadata used by generic helpers.
type notifyChannelDescriptor struct {
	enabled   func(NotifyConfig) bool
	retry     func(NotifyConfig) NotifyRetry
	templates func(NotifyConfig) []NamedTemplateConfig
}

// Config holds service runtime settings and alert rules.
// Params: TOML sections from file or merged directory snapshot.
// Returns: validated runtime configuration.
type Config struct {
	Service ServiceConfig `toml:"service"`
	Log     LogConfig     `toml:"log"`
	Ingest  IngestConfig  `toml:"ingest"`
	Notify  NotifyConfig  `toml:"notify"`
	Rule    []RuleConfig  `toml:"rule"`
}

// rawConfig mirrors TOML model before runtime normalization.
// Params: decoded sections from one TOML source.
// Returns: raw rule map keyed by rule name.
type rawConfig struct {
	Service ServiceConfig            `toml:"service"`
	Log     LogConfig                `toml:"log"`
	Ingest  IngestConfig             `toml:"ingest"`
	Notify  NotifyConfig             `toml:"notify"`
	Rule    map[string]rawRuleConfig `toml:"rule"`
}

// rawRuleConfig stores one rule body from `[rule.<name>]` table.
// Params: rule fields except top-level key-derived name.
// Returns: intermediate rule body used for normalization.
type rawRuleConfig struct {
	Name       string         `toml:"name"`
	AlertType  string         `toml:"alert_type"`
	Match      RuleMatch      `toml:"match"`
	Key        RuleKey        `toml:"key"`
	Raise      RuleRaise      `toml:"raise"`
	Resolve    RuleResolve    `toml:"resolve"`
	Pending    RulePending    `toml:"pending"`
	Notify     RuleNotify     `toml:"notify"`
	OutOfOrder RuleOutOfOrder `toml:"out_of_order"`
}

// ServiceConfig contains process-level settings.
// Params: name and runtime/reload settings.
// Returns: service behavior defaults.
type ServiceConfig struct {
	Name                string `toml:"name"`
	Mode                string `toml:"mode"`
	ReloadEnabled       bool   `toml:"reload_enabled"`
	ReloadIntervalSec   int    `toml:"reload_interval_sec"`
	ResolveScanInterval int    `toml:"resolve_scan_interval_sec"`
	RuntimeStateIdleSec int    `toml:"runtime_state_idle_sec"`
	RuntimeStateMax     int    `toml:"runtime_state_max"`
}

// IngestConfig defines inbound event interfaces.
// Params: embedded HTTP and NATS subscription controls.
// Returns: ingestion runtime options.
type IngestConfig struct {
	HTTP HTTPIngestConfig `toml:"http"`
	NATS NATSIngestConfig `toml:"nats"`
}

// HTTPIngestConfig configures HTTP event ingestion endpoint.
// Params: enable flag, listen/endpoints, and optional body size limit.
// Returns: HTTP ingest behavior.
type HTTPIngestConfig struct {
	Enabled      bool   `toml:"enabled"`
	Listen       string `toml:"listen"`
	HealthPath   string `toml:"health_path"`
	ReadyPath    string `toml:"ready_path"`
	IngestPath   string `toml:"ingest_path"`
	MaxBodyBytes int64  `toml:"max_body_bytes"`
}

// NATSIngestConfig configures JetStream queue-consumer ingestion.
// Params: connection + worker/ack/redelivery policy; stream routing keys are runtime-fixed.
// Returns: NATS ingest behavior.
type NATSIngestConfig struct {
	Enabled       bool     `toml:"enabled"`
	URL           []string `toml:"url"`
	Subject       string   `toml:"-"`
	Stream        string   `toml:"-"`
	ConsumerName  string   `toml:"-"`
	DeliverGroup  string   `toml:"-"`
	Workers       int      `toml:"workers"`
	AckWaitSec    int      `toml:"ack_wait_sec"`
	NackDelayMS   int      `toml:"nack_delay_ms"`
	MaxDeliver    int      `toml:"max_deliver"`
	MaxAckPending int      `toml:"max_ack_pending"`
}

// NATSStateConfig contains fixed JetStream KV and consumer controls for state backend.
// Params: URL, bucket names, and delete-marker consumer settings.
// Returns: NATS state backend options.
type NATSStateConfig struct {
	URL                    []string `toml:"url"`
	TickBucket             string   `toml:"tick_bucket"`
	DataBucket             string   `toml:"data_bucket"`
	DeleteConsumerName     string   `toml:"delete_consumer_name"`
	DeleteDeliverGroup     string   `toml:"delete_deliver_group"`
	DeleteSubjectWildcard  string   `toml:"delete_subject_wildcard"`
	EnableDeleteConsumer   bool     `toml:"enable_delete_consumer"`
	AllowCreateBuckets     bool     `toml:"allow_create_buckets"`
	ResolveReasonByTTLOnly bool     `toml:"resolve_reason_by_ttl_only"`
}

// DeriveStateNATSConfig builds fixed state-backend settings from runtime config.
// Params: full runtime configuration snapshot.
// Returns: non-user-overridable NATS state settings.
func DeriveStateNATSConfig(cfg Config) NATSStateConfig {
	urls := normalizeNATSURLs(cfg.Ingest.NATS.URL)
	if len(urls) == 0 {
		urls = []string{defaultNATSURL}
	}
	return NATSStateConfig{
		URL:                   urls,
		TickBucket:            defaultNATSTickBucket,
		DataBucket:            defaultNATSDataBucket,
		DeleteConsumerName:    defaultDeleteConsumer,
		DeleteDeliverGroup:    defaultDeleteDeliver,
		DeleteSubjectWildcard: "$KV." + defaultNATSTickBucket + ".>",
		EnableDeleteConsumer:  true,
		AllowCreateBuckets:    true,
	}
}

// NotifyConfig defines outbound notification behavior.
// Params: repeat strategy and per-channel transport settings.
// Returns: notification controls.
type NotifyConfig struct {
	Repeat           bool             `toml:"repeat"`
	RepeatEverySec   int              `toml:"repeat_every_sec"`
	RepeatOn         []string         `toml:"repeat_on"`
	RepeatPerChannel bool             `toml:"repeat_per_channel"`
	OnPending        bool             `toml:"on_pending"`
	Queue            NotifyQueue      `toml:"queue"`
	Telegram         TelegramNotifier `toml:"telegram"`
	HTTP             HTTPNotifier     `toml:"http"`
	Mattermost       MattermostConfig `toml:"mattermost"`
	Jira             TrackerNotifier  `toml:"jira"`
	YouTrack         TrackerNotifier  `toml:"youtrack"`
}

// NotifyQueue defines asynchronous delivery queue settings.
// Params: enable flag, worker/ack policy, and optional fixed DLQ toggle.
// Returns: async notify pipeline controls.
type NotifyQueue struct {
	Enabled       bool     `toml:"enabled"`
	URL           []string `toml:"-"`
	AckWaitSec    int      `toml:"ack_wait_sec"`
	NackDelayMS   int      `toml:"nack_delay_ms"`
	MaxDeliver    int      `toml:"max_deliver"`
	MaxAckPending int      `toml:"max_ack_pending"`
	DLQ           bool     `toml:"dlq"`
}

// NamedTemplateConfig describes one reusable message template within one channel section.
// Params: template name and Go text/template body.
// Returns: template entry that can be referenced from rule.notify.route.
type NamedTemplateConfig struct {
	Name    string `toml:"name"`
	Message string `toml:"message"`
}

// NotifyRetry configures outbound delivery retries.
// Params: retry toggle, backoff, attempt limits, and logging.
// Returns: retry policy for notifications.
type NotifyRetry struct {
	Enabled        bool   `toml:"enabled"`
	Backoff        string `toml:"backoff"`
	InitialMS      int    `toml:"initial_ms"`
	MaxMS          int    `toml:"max_ms"`
	MaxAttempts    int    `toml:"max_attempts"`
	LogEachAttempt bool   `toml:"log_each_attempt"`
}

// TelegramNotifier defines Telegram channel settings for MVP.
// Params: enabled flag, bot token, chat ID, API base URL, and retry policy.
// Returns: Telegram sender configuration.
type TelegramNotifier struct {
	Enabled      bool                  `toml:"enabled"`
	BotToken     string                `toml:"bot_token"`
	ChatID       string                `toml:"chat_id"`
	ActiveChatID string                `toml:"active_chat_id"`
	APIBase      string                `toml:"api_base"`
	Retry        NotifyRetry           `toml:"retry"`
	NameTemplate []NamedTemplateConfig `toml:"name-template"`
}

// HTTPNotifier defines generic outbound HTTP scenario endpoint.
// Params: URL, method, timeout, optional static headers, and retry policy.
// Returns: HTTP notification sender configuration.
type HTTPNotifier struct {
	Enabled      bool                  `toml:"enabled"`
	URL          string                `toml:"url"`
	Method       string                `toml:"method"`
	TimeoutSec   int                   `toml:"timeout_sec"`
	Headers      map[string]string     `toml:"headers"`
	Retry        NotifyRetry           `toml:"retry"`
	NameTemplate []NamedTemplateConfig `toml:"name-template"`
}

// MattermostConfig defines Mattermost API channel settings.
// Params: enabled flag, API base URL, bot token, channel id, and retry policy.
// Returns: Mattermost sender configuration.
type MattermostConfig struct {
	Enabled         bool                  `toml:"enabled"`
	BaseURL         string                `toml:"base_url"`
	BotToken        string                `toml:"bot_token"`
	ChannelID       string                `toml:"channel_id"`
	ActiveChannelID string                `toml:"active_channel_id"`
	TimeoutSec      int                   `toml:"timeout_sec"`
	Retry           NotifyRetry           `toml:"retry"`
	NameTemplate    []NamedTemplateConfig `toml:"name-template"`
}

// TrackerNotifier defines tracker transport settings (Jira/YouTrack) over HTTP.
// Params: endpoint/auth/action templates, timeout, retry policy, and message templates.
// Returns: tracker sender configuration.
type TrackerNotifier struct {
	Enabled      bool                  `toml:"enabled"`
	BaseURL      string                `toml:"base_url"`
	TimeoutSec   int                   `toml:"timeout_sec"`
	Auth         TrackerAuthConfig     `toml:"auth"`
	Create       TrackerActionConfig   `toml:"create"`
	Resolve      TrackerActionConfig   `toml:"resolve"`
	Retry        NotifyRetry           `toml:"retry"`
	NameTemplate []NamedTemplateConfig `toml:"name-template"`
}

// TrackerAuthConfig defines tracker auth strategy.
// Params: auth type and credentials/header options.
// Returns: auth controls for tracker requests.
type TrackerAuthConfig struct {
	Type     string `toml:"type"`
	Username string `toml:"username"`
	Password string `toml:"password"`
	Token    string `toml:"token"`
	Header   string `toml:"header"`
	Prefix   string `toml:"prefix"`
}

// TrackerActionConfig defines one HTTP action (create/resolve) for tracker channels.
// Params: HTTP method/path templates, optional headers/body templates, success statuses, and response ref path.
// Returns: action settings used by tracker sender.
type TrackerActionConfig struct {
	Method        string            `toml:"method"`
	Path          string            `toml:"path"`
	Headers       map[string]string `toml:"headers"`
	BodyTemplate  string            `toml:"body_template"`
	SuccessStatus []int             `toml:"success_status"`
	RefJSONPath   string            `toml:"ref_json_path"`
}

// LogConfig contains console/file logging sinks.
// Params: sink settings for each output target.
// Returns: logger setup options.
type LogConfig struct {
	Console LogSinkConfig `toml:"console"`
	File    LogSinkConfig `toml:"file"`
}

// LogSinkConfig defines one logging sink.
// Params: sink enable flag, level, format, and path.
// Returns: sink-specific behavior.
type LogSinkConfig struct {
	Enabled bool   `toml:"enabled"`
	Level   string `toml:"level"`
	Format  string `toml:"format"`
	Path    string `toml:"path"`
}

// RuleConfig describes one alert rule.
// Params: matching logic, key building, thresholds, and notify toggles.
// Returns: runtime rule definition.
type RuleConfig struct {
	Name       string         `toml:"name"`
	AlertType  string         `toml:"alert_type"`
	Match      RuleMatch      `toml:"match"`
	Key        RuleKey        `toml:"key"`
	Raise      RuleRaise      `toml:"raise"`
	Resolve    RuleResolve    `toml:"resolve"`
	Pending    RulePending    `toml:"pending"`
	Notify     RuleNotify     `toml:"notify"`
	OutOfOrder RuleOutOfOrder `toml:"out_of_order"`
}

// RuleMatch contains event selector predicates.
// Params: event type list, variable list, tags allow-map, and optional value predicate.
// Returns: matching criteria.
type RuleMatch struct {
	Type  []string              `toml:"type"`
	Var   []string              `toml:"var"`
	Tags  map[string]StringList `toml:"tags"`
	Value *RuleValuePredicate   `toml:"value"`
}

// StringList decodes TOML scalar/string-array into a normalized list.
// Params: raw TOML bytes for one tag value.
// Returns: normalized list of allowed values.
type StringList []string

// UnmarshalTOML decodes tag-value allow list from scalar or array.
// Params: parsed TOML value from decoder.
// Returns: conversion error for unsupported types.
func (s *StringList) UnmarshalTOML(v interface{}) error {
	switch t := v.(type) {
	case string:
		*s = []string{t}
		return nil
	case []interface{}:
		out := make([]string, 0, len(t))
		for _, raw := range t {
			str, ok := raw.(string)
			if !ok {
				return fmt.Errorf("string list contains non-string value %T", raw)
			}
			out = append(out, str)
		}
		*s = out
		return nil
	default:
		return fmt.Errorf("unsupported tag value type %T", v)
	}
}

// RuleValuePredicate describes typed value filtering.
// Params: value type selector, operation, and type-specific operands.
// Returns: value predicate config.
type RuleValuePredicate struct {
	Type string   `toml:"t"`
	Op   string   `toml:"op"`
	N    *float64 `toml:"n"`
	S    *string  `toml:"s"`
	B    *bool    `toml:"b"`
	In   []string `toml:"in"`

	CompiledMatchRE    *regexp.Regexp `toml:"-"`
	CompiledWildcardRE *regexp.Regexp `toml:"-"`
}

// RuleKey defines tags used to build alert key.
// Params: ordered tag list expected in incoming events.
// Returns: key generation settings.
type RuleKey struct {
	FromTags []string `toml:"from_tags"`
}

// RuleRaise defines raise thresholds by alert type.
// Params: count/window thresholds and missing heartbeat timeout.
// Returns: raise behavior.
type RuleRaise struct {
	N          int `toml:"n"`
	TaggSec    int `toml:"tagg_sec"`
	MissingSec int `toml:"missing_sec"`
}

// RuleResolve defines resolve timeout settings.
// Params: silence timeout and optional hysteresis confirmation window before resolved.
// Returns: resolve behavior.
type RuleResolve struct {
	SilenceSec    int `toml:"silence_sec"`
	HysteresisSec int `toml:"hysteresis_sec"`
}

// RulePending controls pending-to-firing confirmation.
// Params: enable flag and delay in seconds.
// Returns: pending behavior.
type RulePending struct {
	Enabled  bool `toml:"enabled"`
	DelaySec int  `toml:"delay_sec"`
}

// RuleNotify overrides global notification behavior.
// Params: per-rule repeat controls.
// Returns: rule-specific notify toggles.
type RuleNotify struct {
	Repeat         *bool             `toml:"repeat"`
	RepeatEverySec int               `toml:"repeat_every_sec"`
	OnPending      *bool             `toml:"on_pending"`
	Route          []RuleNotifyRoute `toml:"route"`
}

// RuleNotifyRoute binds one transport channel with one named message template.
// Params: transport channel and notify template name.
// Returns: one outbound routing rule for alert notifications.
type RuleNotifyRoute struct {
	Name     string `toml:"name"`
	Channel  string `toml:"channel"`
	Template string `toml:"template"`
	Mode     string `toml:"mode"`
}

// RuleOutOfOrder defines safeguards for delayed/future events.
// Params: max late age and max future skew in milliseconds.
// Returns: out-of-order controls.
type RuleOutOfOrder struct {
	MaxLateMS       int64 `toml:"max_late_ms"`
	MaxFutureSkewMS int64 `toml:"max_future_skew_ms"`
}

// ConfigSource describes file or directory config source.
// Params: exactly one of file path or directory path.
// Returns: normalized source descriptor.
type ConfigSource struct {
	File string
	Dir  string
}

// FromCLI builds normalized source configuration from input paths.
// Params: optional file and directory arguments.
// Returns: source descriptor or validation error.
func FromCLI(filePath, dirPath string) (ConfigSource, error) {
	filePath = strings.TrimSpace(filePath)
	dirPath = strings.TrimSpace(dirPath)

	if filePath == "" && dirPath == "" {
		return ConfigSource{}, errors.New("either --config-file or --config-dir must be provided")
	}
	if filePath != "" && dirPath != "" {
		return ConfigSource{}, errors.New("config source must be either file or dir")
	}

	if filePath != "" {
		return ConfigSource{File: filePath}, nil
	}
	return ConfigSource{Dir: dirPath}, nil
}

// LoadSnapshot loads and validates configuration from one source.
// Params: source selects file or directory mode.
// Returns: validated config or load/validation error.
func LoadSnapshot(src ConfigSource) (Config, error) {
	var cfg Config
	var err error
	if src.File != "" {
		cfg, err = loadFile(src.File)
	} else {
		cfg, err = loadDir(src.Dir)
	}
	if err != nil {
		return Config{}, err
	}
	applyDefaults(&cfg)
	if err := validateConfig(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// ResolveTimeout returns rule-specific resolve timeout.
// Params: rule to evaluate timeout mapping.
// Returns: timeout duration for tick refresh and resolve checks.
func ResolveTimeout(rule RuleConfig) time.Duration {
	switch rule.AlertType {
	case "count_total", "count_window":
		return time.Duration(rule.Resolve.SilenceSec+rule.Resolve.HysteresisSec) * time.Second
	case "missing_heartbeat":
		return time.Duration(rule.Raise.MissingSec+rule.Resolve.HysteresisSec) * time.Second
	default:
		return 0
	}
}

// configMergeHints carries explicit bool-presence markers used for directory overlays.
// Params: sparse fields decoded from one TOML fragment.
// Returns: merge behavior hints for zero-value bool overrides.
type configMergeHints struct {
	Notify notifyMergeHints `toml:"notify"`
}

// notifyMergeHints tracks explicit bool fields in notify section.
// Params: sparse notify values decoded from one TOML fragment.
// Returns: bool-presence markers for merge logic.
type notifyMergeHints struct {
	Repeat           *bool             `toml:"repeat"`
	RepeatPerChannel *bool             `toml:"repeat_per_channel"`
	OnPending        *bool             `toml:"on_pending"`
	Queue            queueMergeHints   `toml:"queue"`
	Telegram         channelMergeHints `toml:"telegram"`
	HTTP             channelMergeHints `toml:"http"`
	Mattermost       channelMergeHints `toml:"mattermost"`
	Jira             channelMergeHints `toml:"jira"`
	YouTrack         channelMergeHints `toml:"youtrack"`
}

// queueMergeHints tracks explicit bool fields in notify.queue section.
// Params: sparse queue fields decoded from one TOML fragment.
// Returns: bool-presence markers for queue merge logic.
type queueMergeHints struct {
	Enabled *bool `toml:"enabled"`
	DLQ     *bool `toml:"dlq"`
}

// channelMergeHints tracks explicit enabled flags in channel sections.
// Params: sparse channel fields decoded from one TOML fragment.
// Returns: channel bool-presence markers for merge logic.
type channelMergeHints struct {
	Enabled *bool `toml:"enabled"`
}

// hasExplicitBool reports whether notify fragment contains explicit bool keys.
// Params: notify merge hints from one TOML fragment.
// Returns: true when at least one bool was explicitly set.
func (h notifyMergeHints) hasExplicitBool() bool {
	return h.Repeat != nil ||
		h.RepeatPerChannel != nil ||
		h.OnPending != nil ||
		h.Queue.Enabled != nil ||
		h.Queue.DLQ != nil ||
		h.Telegram.Enabled != nil ||
		h.HTTP.Enabled != nil ||
		h.Mattermost.Enabled != nil ||
		h.Jira.Enabled != nil ||
		h.YouTrack.Enabled != nil
}

// normalizeRawConfig converts raw TOML model to runtime config.
// Params: decoded raw config from file fragment.
// Returns: normalized config snapshot.
func normalizeRawConfig(raw rawConfig) (Config, error) {
	cfg := Config{
		Service: raw.Service,
		Log:     raw.Log,
		Ingest:  raw.Ingest,
		Notify:  raw.Notify,
	}
	if len(raw.Rule) == 0 {
		return cfg, nil
	}

	names := make([]string, 0, len(raw.Rule))
	for name := range raw.Rule {
		names = append(names, name)
	}
	sort.Strings(names)
	cfg.Rule = make([]RuleConfig, 0, len(names))
	for _, name := range names {
		body := raw.Rule[name]
		if strings.TrimSpace(body.Name) != "" {
			return Config{}, fmt.Errorf("rule.%s.name is not supported; use [rule.%s] key as rule name", name, name)
		}
		cfg.Rule = append(cfg.Rule, RuleConfig{
			Name:       name,
			AlertType:  body.AlertType,
			Match:      body.Match,
			Key:        body.Key,
			Raise:      body.Raise,
			Resolve:    body.Resolve,
			Pending:    body.Pending,
			Notify:     body.Notify,
			OutOfOrder: body.OutOfOrder,
		})
	}

	return cfg, nil
}

// rejectUnsupportedSyntax checks deprecated/forbidden TOML syntax and returns explicit error.
// Params: raw TOML file body.
// Returns: error when unsupported syntax is detected.
func rejectUnsupportedSyntax(body []byte) error {
	if legacyRuleArrayPattern.Match(body) {
		return errors.New("legacy [[rule]] format is not supported; use [rule.<rule_name>] tables")
	}
	if unsupportedStatePattern.Match(body) {
		return errors.New("state configuration is not supported; state backend settings are fixed and derived from ingest.nats.url")
	}
	if unsupportedIngestNATSFixedKeysPattern.Match(body) {
		return errors.New("ingest.nats.subject/stream/consumer_name/deliver_group are fixed in runtime and must not be configured")
	}
	if unsupportedNotifyQueueURLPattern.Match(body) {
		return errors.New("notify.queue.url is not supported; notify queue NATS URL is derived from ingest.nats.url")
	}
	if unsupportedNotifyQueueDLQTablePattern.Match(body) {
		return errors.New("[notify.queue.dlq] section is not supported; use notify.queue.dlq = true|false in [notify.queue]")
	}
	return nil
}

// loadFile reads one TOML configuration file.
// Params: file path to config snapshot.
// Returns: decoded config or read/decode error.
func loadFile(path string) (Config, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config file %q: %w", path, err)
	}
	if err := rejectUnsupportedSyntax(body); err != nil {
		return Config{}, fmt.Errorf("decode config file %q: %w", path, err)
	}
	var raw rawConfig
	if err := toml.Unmarshal(body, &raw); err != nil {
		return Config{}, fmt.Errorf("decode config file %q: %w", path, err)
	}
	cfg, err := normalizeRawConfig(raw)
	if err != nil {
		return Config{}, fmt.Errorf("decode config file %q: %w", path, err)
	}
	return cfg, nil
}

// loadFileForMerge reads one TOML file with merge hints.
// Params: file path to config fragment.
// Returns: decoded config plus explicit-bool hints for overlay merge.
func loadFileForMerge(path string) (Config, configMergeHints, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return Config{}, configMergeHints{}, fmt.Errorf("read config file %q: %w", path, err)
	}
	if err := rejectUnsupportedSyntax(body); err != nil {
		return Config{}, configMergeHints{}, fmt.Errorf("decode config file %q: %w", path, err)
	}
	var raw rawConfig
	if err := toml.Unmarshal(body, &raw); err != nil {
		return Config{}, configMergeHints{}, fmt.Errorf("decode config file %q: %w", path, err)
	}
	cfg, err := normalizeRawConfig(raw)
	if err != nil {
		return Config{}, configMergeHints{}, fmt.Errorf("decode config file %q: %w", path, err)
	}
	var hints configMergeHints
	if err := toml.Unmarshal(body, &hints); err != nil {
		return Config{}, configMergeHints{}, fmt.Errorf("decode merge hints %q: %w", path, err)
	}
	return cfg, hints, nil
}

// loadDir reads and merges TOML files from one directory.
// Params: directory containing config fragments.
// Returns: merged config snapshot or load/decode error.
func loadDir(dir string) (Config, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Config{}, fmt.Errorf("read config dir %q: %w", dir, err)
	}

	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".toml" {
			continue
		}
		files = append(files, filepath.Join(dir, name))
	}
	if len(files) == 0 {
		return Config{}, fmt.Errorf("no .toml files found in %q", dir)
	}
	sort.Strings(files)

	var merged Config
	for _, file := range files {
		fragment, hints, err := loadFileForMerge(file)
		if err != nil {
			return Config{}, err
		}
		mergeConfig(&merged, fragment, hints)
	}
	return merged, nil
}

// mergeConfig overlays source onto destination.
// Params: destination config and next fragment.
// Returns: merged configuration side-effect in dst.
func mergeConfig(dst *Config, src Config, hints configMergeHints) {
	if src.Service != (ServiceConfig{}) {
		dst.Service = src.Service
	}
	if src.Log != (LogConfig{}) {
		dst.Log = src.Log
	}
	if hasIngestConfig(src.Ingest) {
		dst.Ingest = src.Ingest
	}
	if hasNotifyConfig(src.Notify) || hints.Notify.hasExplicitBool() {
		mergeNotifyConfig(&dst.Notify, src.Notify, hints.Notify)
	}
	if len(src.Rule) > 0 {
		dst.Rule = append(dst.Rule, src.Rule...)
	}
}

// mergeNotifyConfig overlays notify fragment into destination preserving existing sibling fields.
// Params: destination notify config and fragment from one source file.
// Returns: merged notify configuration side-effect in dst.
func mergeNotifyConfig(dst *NotifyConfig, src NotifyConfig, hints notifyMergeHints) {
	applyBoolMerge(&dst.Repeat, src.Repeat, hints.Repeat)
	if src.RepeatEverySec != 0 {
		dst.RepeatEverySec = src.RepeatEverySec
	}
	if len(src.RepeatOn) > 0 {
		dst.RepeatOn = append([]string(nil), src.RepeatOn...)
	}
	applyBoolMerge(&dst.RepeatPerChannel, src.RepeatPerChannel, hints.RepeatPerChannel)
	applyBoolMerge(&dst.OnPending, src.OnPending, hints.OnPending)
	mergeNotifyQueue(&dst.Queue, src.Queue, hints.Queue)
	mergeTelegramNotifier(&dst.Telegram, src.Telegram, hints.Telegram)
	mergeHTTPNotifier(&dst.HTTP, src.HTTP, hints.HTTP)
	mergeMattermostNotifier(&dst.Mattermost, src.Mattermost, hints.Mattermost)
	mergeTrackerNotifier(&dst.Jira, src.Jira, hints.Jira)
	mergeTrackerNotifier(&dst.YouTrack, src.YouTrack, hints.YouTrack)
}

// mergeNotifyQueue overlays async queue config preserving other notify fields.
// Params: destination queue config and source fragment.
// Returns: merged queue config side-effect in dst.
func mergeNotifyQueue(dst *NotifyQueue, src NotifyQueue, hints queueMergeHints) {
	applyBoolMerge(&dst.Enabled, src.Enabled, hints.Enabled)
	if src.AckWaitSec != 0 {
		dst.AckWaitSec = src.AckWaitSec
	}
	if src.NackDelayMS != 0 {
		dst.NackDelayMS = src.NackDelayMS
	}
	if src.MaxDeliver != 0 {
		dst.MaxDeliver = src.MaxDeliver
	}
	if src.MaxAckPending != 0 {
		dst.MaxAckPending = src.MaxAckPending
	}
	applyBoolMerge(&dst.DLQ, src.DLQ, hints.DLQ)
}

// mergeTelegramNotifier overlays telegram transport config preserving other notify fields.
// Params: destination telegram config and source fragment.
// Returns: merged telegram configuration side-effect in dst.
func mergeTelegramNotifier(dst *TelegramNotifier, src TelegramNotifier, hints channelMergeHints) {
	applyBoolMerge(&dst.Enabled, src.Enabled, hints.Enabled)
	if strings.TrimSpace(src.BotToken) != "" {
		dst.BotToken = src.BotToken
	}
	if strings.TrimSpace(src.ChatID) != "" {
		dst.ChatID = src.ChatID
	}
	if strings.TrimSpace(src.ActiveChatID) != "" {
		dst.ActiveChatID = src.ActiveChatID
	}
	if strings.TrimSpace(src.APIBase) != "" {
		dst.APIBase = src.APIBase
	}
	if src.Retry != (NotifyRetry{}) {
		dst.Retry = src.Retry
	}
	if len(src.NameTemplate) > 0 {
		dst.NameTemplate = append(dst.NameTemplate, src.NameTemplate...)
	}
}

// mergeHTTPNotifier overlays HTTP transport config preserving other notify fields.
// Params: destination http config and source fragment.
// Returns: merged http configuration side-effect in dst.
func mergeHTTPNotifier(dst *HTTPNotifier, src HTTPNotifier, hints channelMergeHints) {
	applyBoolMerge(&dst.Enabled, src.Enabled, hints.Enabled)
	if strings.TrimSpace(src.URL) != "" {
		dst.URL = src.URL
	}
	if strings.TrimSpace(src.Method) != "" {
		dst.Method = src.Method
	}
	if src.TimeoutSec != 0 {
		dst.TimeoutSec = src.TimeoutSec
	}
	if len(src.Headers) > 0 {
		if dst.Headers == nil {
			dst.Headers = make(map[string]string, len(src.Headers))
		}
		for key, value := range src.Headers {
			dst.Headers[key] = value
		}
	}
	if src.Retry != (NotifyRetry{}) {
		dst.Retry = src.Retry
	}
	if len(src.NameTemplate) > 0 {
		dst.NameTemplate = append(dst.NameTemplate, src.NameTemplate...)
	}
}

// mergeMattermostNotifier overlays mattermost transport config preserving other notify fields.
// Params: destination mattermost config and source fragment.
// Returns: merged mattermost configuration side-effect in dst.
func mergeMattermostNotifier(dst *MattermostConfig, src MattermostConfig, hints channelMergeHints) {
	applyBoolMerge(&dst.Enabled, src.Enabled, hints.Enabled)
	if strings.TrimSpace(src.BaseURL) != "" {
		dst.BaseURL = src.BaseURL
	}
	if strings.TrimSpace(src.BotToken) != "" {
		dst.BotToken = src.BotToken
	}
	if strings.TrimSpace(src.ChannelID) != "" {
		dst.ChannelID = src.ChannelID
	}
	if strings.TrimSpace(src.ActiveChannelID) != "" {
		dst.ActiveChannelID = src.ActiveChannelID
	}
	if src.TimeoutSec != 0 {
		dst.TimeoutSec = src.TimeoutSec
	}
	if src.Retry != (NotifyRetry{}) {
		dst.Retry = src.Retry
	}
	if len(src.NameTemplate) > 0 {
		dst.NameTemplate = append(dst.NameTemplate, src.NameTemplate...)
	}
}

// mergeTrackerNotifier overlays tracker transport config preserving other notify fields.
// Params: destination tracker config and source fragment.
// Returns: merged tracker configuration side-effect in dst.
func mergeTrackerNotifier(dst *TrackerNotifier, src TrackerNotifier, hints channelMergeHints) {
	applyBoolMerge(&dst.Enabled, src.Enabled, hints.Enabled)
	if strings.TrimSpace(src.BaseURL) != "" {
		dst.BaseURL = src.BaseURL
	}
	if src.TimeoutSec != 0 {
		dst.TimeoutSec = src.TimeoutSec
	}
	if src.Auth != (TrackerAuthConfig{}) {
		dst.Auth = src.Auth
	}
	mergeTrackerActionConfig(&dst.Create, src.Create)
	mergeTrackerActionConfig(&dst.Resolve, src.Resolve)
	if src.Retry != (NotifyRetry{}) {
		dst.Retry = src.Retry
	}
	if len(src.NameTemplate) > 0 {
		dst.NameTemplate = append(dst.NameTemplate, src.NameTemplate...)
	}
}

// applyBoolMerge merges bool with explicit-value awareness for directory overlays.
// Params: destination bool pointer, source decoded bool, and explicit source marker.
// Returns: merged bool side-effect in dst.
func applyBoolMerge(dst *bool, value bool, explicit *bool) {
	if explicit != nil {
		*dst = *explicit
		return
	}
	if value {
		*dst = true
	}
}

// mergeTrackerActionConfig overlays one tracker action fragment.
// Params: destination action and source fragment.
// Returns: merged action settings side-effect in dst.
func mergeTrackerActionConfig(dst *TrackerActionConfig, src TrackerActionConfig) {
	if strings.TrimSpace(src.Method) != "" {
		dst.Method = src.Method
	}
	if strings.TrimSpace(src.Path) != "" {
		dst.Path = src.Path
	}
	if len(src.Headers) > 0 {
		if dst.Headers == nil {
			dst.Headers = make(map[string]string, len(src.Headers))
		}
		for key, value := range src.Headers {
			dst.Headers[key] = value
		}
	}
	if strings.TrimSpace(src.BodyTemplate) != "" {
		dst.BodyTemplate = src.BodyTemplate
	}
	if len(src.SuccessStatus) > 0 {
		dst.SuccessStatus = append([]int(nil), src.SuccessStatus...)
	}
	if strings.TrimSpace(src.RefJSONPath) != "" {
		dst.RefJSONPath = src.RefJSONPath
	}
}

// hasNotifyConfig checks whether notify section contains any explicit values.
// Params: notify configuration fragment.
// Returns: true when section should be merged into destination snapshot.
func hasNotifyConfig(cfg NotifyConfig) bool {
	if cfg.Repeat || cfg.RepeatEverySec != 0 || len(cfg.RepeatOn) > 0 || cfg.RepeatPerChannel || cfg.OnPending {
		return true
	}
	if cfg.Queue.Enabled ||
		cfg.Queue.AckWaitSec != 0 ||
		cfg.Queue.NackDelayMS != 0 ||
		cfg.Queue.MaxDeliver != 0 ||
		cfg.Queue.MaxAckPending != 0 ||
		cfg.Queue.DLQ {
		return true
	}
	if cfg.Telegram.Enabled ||
		strings.TrimSpace(cfg.Telegram.BotToken) != "" ||
		strings.TrimSpace(cfg.Telegram.ChatID) != "" ||
		strings.TrimSpace(cfg.Telegram.ActiveChatID) != "" ||
		strings.TrimSpace(cfg.Telegram.APIBase) != "" ||
		cfg.Telegram.Retry != (NotifyRetry{}) ||
		len(cfg.Telegram.NameTemplate) > 0 {
		return true
	}
	if cfg.HTTP.Enabled || strings.TrimSpace(cfg.HTTP.URL) != "" || strings.TrimSpace(cfg.HTTP.Method) != "" || cfg.HTTP.TimeoutSec != 0 || len(cfg.HTTP.Headers) > 0 || cfg.HTTP.Retry != (NotifyRetry{}) || len(cfg.HTTP.NameTemplate) > 0 {
		return true
	}
	if cfg.Mattermost.Enabled ||
		strings.TrimSpace(cfg.Mattermost.BaseURL) != "" ||
		strings.TrimSpace(cfg.Mattermost.BotToken) != "" ||
		strings.TrimSpace(cfg.Mattermost.ChannelID) != "" ||
		strings.TrimSpace(cfg.Mattermost.ActiveChannelID) != "" ||
		cfg.Mattermost.TimeoutSec != 0 ||
		cfg.Mattermost.Retry != (NotifyRetry{}) ||
		len(cfg.Mattermost.NameTemplate) > 0 {
		return true
	}
	if hasTrackerNotifierConfig(cfg.Jira) || hasTrackerNotifierConfig(cfg.YouTrack) {
		return true
	}
	return false
}

// hasTrackerNotifierConfig checks whether tracker section contains explicit values.
// Params: tracker notifier fragment.
// Returns: true when tracker section should be merged.
func hasTrackerNotifierConfig(cfg TrackerNotifier) bool {
	if cfg.Enabled || strings.TrimSpace(cfg.BaseURL) != "" || cfg.TimeoutSec != 0 || cfg.Auth != (TrackerAuthConfig{}) || cfg.Retry != (NotifyRetry{}) || len(cfg.NameTemplate) > 0 {
		return true
	}
	return hasTrackerActionConfig(cfg.Create) || hasTrackerActionConfig(cfg.Resolve)
}

// hasTrackerActionConfig checks whether tracker action section contains explicit values.
// Params: tracker action fragment.
// Returns: true when action section should be merged.
func hasTrackerActionConfig(cfg TrackerActionConfig) bool {
	return strings.TrimSpace(cfg.Method) != "" ||
		strings.TrimSpace(cfg.Path) != "" ||
		len(cfg.Headers) > 0 ||
		strings.TrimSpace(cfg.BodyTemplate) != "" ||
		len(cfg.SuccessStatus) > 0 ||
		strings.TrimSpace(cfg.RefJSONPath) != ""
}

// applyDefaults fills omitted config fields with safe defaults.
// Params: cfg pointer to decoded snapshot.
// Returns: defaults applied in place.
func applyDefaults(cfg *Config) {
	if strings.TrimSpace(cfg.Service.Name) == "" {
		cfg.Service.Name = "alerting"
	}
	cfg.Service.Mode = NormalizeServiceMode(cfg.Service.Mode)
	if cfg.Service.ReloadIntervalSec <= 0 {
		cfg.Service.ReloadIntervalSec = defaultReloadSeconds
	}
	if cfg.Service.ResolveScanInterval <= 0 {
		cfg.Service.ResolveScanInterval = defaultResolveScanSeconds
	}

	if cfg.Log.Console.Level == "" {
		cfg.Log.Console.Level = "info"
	}
	if cfg.Log.Console.Format == "" {
		cfg.Log.Console.Format = "line"
	}
	if cfg.Log.File.Level == "" {
		cfg.Log.File.Level = "info"
	}
	if cfg.Log.File.Format == "" {
		cfg.Log.File.Format = "json"
	}
	if !cfg.Log.Console.Enabled && !cfg.Log.File.Enabled {
		cfg.Log.Console.Enabled = true
	}

	if strings.TrimSpace(cfg.Ingest.HTTP.Listen) == "" {
		cfg.Ingest.HTTP.Listen = defaultHTTPListen
	}
	if strings.TrimSpace(cfg.Ingest.HTTP.HealthPath) == "" {
		cfg.Ingest.HTTP.HealthPath = defaultHealthPath
	}
	if strings.TrimSpace(cfg.Ingest.HTTP.ReadyPath) == "" {
		cfg.Ingest.HTTP.ReadyPath = defaultReadyPath
	}
	if strings.TrimSpace(cfg.Ingest.HTTP.IngestPath) == "" {
		cfg.Ingest.HTTP.IngestPath = defaultIngestPath
	}
	if cfg.Ingest.HTTP.MaxBodyBytes <= 0 {
		cfg.Ingest.HTTP.MaxBodyBytes = 2 << 20
	}
	if cfg.Service.Mode == ServiceModeSingle {
		// Single mode always disables NATS-dependent paths regardless of user flags.
		cfg.Ingest.NATS.Enabled = false
		cfg.Notify.Queue.Enabled = false
		cfg.Notify.Queue.DLQ = false
	} else {
		cfg.Ingest.NATS.URL = normalizeNATSURLs(cfg.Ingest.NATS.URL)
		if len(cfg.Ingest.NATS.URL) == 0 {
			cfg.Ingest.NATS.URL = []string{defaultNATSURL}
		}
		cfg.Ingest.NATS.Subject = defaultNATSSubject
		cfg.Ingest.NATS.Stream = defaultNATSIngestStream
		cfg.Ingest.NATS.ConsumerName = defaultNATSIngestConsumer
		cfg.Ingest.NATS.DeliverGroup = defaultNATSIngestGroup
		if cfg.Ingest.NATS.Workers == 0 {
			cfg.Ingest.NATS.Workers = defaultNATSIngestWorkers
		}
		if cfg.Ingest.NATS.AckWaitSec <= 0 {
			cfg.Ingest.NATS.AckWaitSec = defaultNATSAckWaitSec
		}
		if cfg.Ingest.NATS.NackDelayMS < 0 {
			cfg.Ingest.NATS.NackDelayMS = 0
		}
		if cfg.Ingest.NATS.NackDelayMS == 0 {
			cfg.Ingest.NATS.NackDelayMS = defaultNATSNackDelayMS
		}
		if cfg.Ingest.NATS.MaxDeliver == 0 {
			cfg.Ingest.NATS.MaxDeliver = defaultNATSMaxDeliver
		}
		if cfg.Ingest.NATS.MaxAckPending <= 0 {
			cfg.Ingest.NATS.MaxAckPending = defaultNATSMaxAckPending
		}
		if !cfg.Ingest.HTTP.Enabled && !cfg.Ingest.NATS.Enabled {
			cfg.Ingest.HTTP.Enabled = true
		}
	}

	if cfg.Notify.RepeatEverySec <= 0 {
		cfg.Notify.RepeatEverySec = defaultNotifyRepeatSeconds
	}
	if len(cfg.Notify.RepeatOn) == 0 {
		cfg.Notify.RepeatOn = []string{"firing"}
	}
	if cfg.Service.Mode == ServiceModeNATS {
		// Queue uses the same NATS URL list as ingest/state in multi-instance mode.
		cfg.Notify.Queue.URL = append([]string(nil), cfg.Ingest.NATS.URL...)
		if cfg.Notify.Queue.AckWaitSec <= 0 {
			cfg.Notify.Queue.AckWaitSec = defaultNATSAckWaitSec
		}
		if cfg.Notify.Queue.NackDelayMS < 0 {
			cfg.Notify.Queue.NackDelayMS = 0
		}
		if cfg.Notify.Queue.NackDelayMS == 0 {
			cfg.Notify.Queue.NackDelayMS = defaultNATSNackDelayMS
		}
		if cfg.Notify.Queue.MaxDeliver == 0 {
			cfg.Notify.Queue.MaxDeliver = defaultNATSMaxDeliver
		}
		if cfg.Notify.Queue.MaxAckPending <= 0 {
			cfg.Notify.Queue.MaxAckPending = defaultNATSMaxAckPending
		}
	} else {
		cfg.Notify.Queue.URL = nil
	}
	if cfg.Notify.Telegram.APIBase == "" {
		cfg.Notify.Telegram.APIBase = "https://api.telegram.org"
	}
	fillNotifyRetryDefaults(&cfg.Notify.Telegram.Retry)
	if cfg.Notify.HTTP.Method == "" {
		cfg.Notify.HTTP.Method = "POST"
	}
	if cfg.Notify.HTTP.TimeoutSec <= 0 {
		cfg.Notify.HTTP.TimeoutSec = 10
	}
	fillNotifyRetryDefaults(&cfg.Notify.HTTP.Retry)
	if cfg.Notify.Mattermost.TimeoutSec <= 0 {
		cfg.Notify.Mattermost.TimeoutSec = 10
	}
	fillNotifyRetryDefaults(&cfg.Notify.Mattermost.Retry)
	fillTrackerNotifierDefaults(&cfg.Notify.Jira)
	fillTrackerNotifierDefaults(&cfg.Notify.YouTrack)

	for i := range cfg.Rule {
		rule := &cfg.Rule[i]
		if rule.Pending.DelaySec <= 0 {
			rule.Pending.DelaySec = defaultPendingDelaySeconds
		}
		if rule.Notify.RepeatEverySec <= 0 {
			rule.Notify.RepeatEverySec = cfg.Notify.RepeatEverySec
		}
	}
}

// fillTrackerNotifierDefaults normalizes tracker transport defaults.
// Params: tracker notifier config pointer.
// Returns: defaults applied in place.
func fillTrackerNotifierDefaults(cfg *TrackerNotifier) {
	if cfg == nil {
		return
	}
	if cfg.TimeoutSec <= 0 {
		cfg.TimeoutSec = 10
	}
	fillNotifyRetryDefaults(&cfg.Retry)
	fillTrackerActionDefaults(&cfg.Create, true)
	fillTrackerActionDefaults(&cfg.Resolve, false)
}

// fillTrackerActionDefaults normalizes one tracker action defaults.
// Params: action pointer and create/resolve selector.
// Returns: defaults applied in place.
func fillTrackerActionDefaults(action *TrackerActionConfig, isCreate bool) {
	if action == nil {
		return
	}
	if strings.TrimSpace(action.Method) == "" {
		action.Method = "POST"
	}
	if len(action.SuccessStatus) == 0 {
		if isCreate {
			action.SuccessStatus = []int{200, 201}
		} else {
			action.SuccessStatus = []int{200, 204}
		}
	}
}

// fillNotifyRetryDefaults normalizes retry policy fields for one channel.
// Params: retry policy pointer.
// Returns: policy defaults applied in place.
func fillNotifyRetryDefaults(retry *NotifyRetry) {
	if retry == nil {
		return
	}
	if retry.Backoff == "" {
		retry.Backoff = "exponential"
	}
	if retry.InitialMS <= 0 {
		retry.InitialMS = 500
	}
	if retry.MaxMS <= 0 {
		retry.MaxMS = 60000
	}
}

// validateConfig validates full runtime configuration.
// Params: cfg snapshot to validate.
// Returns: validation error list with first failing rule.
func validateConfig(cfg Config) error {
	if len(cfg.Rule) == 0 {
		return errors.New("at least one rule is required")
	}
	mode := NormalizeServiceMode(cfg.Service.Mode)
	if !IsSupportedServiceMode(mode) {
		return fmt.Errorf("service.mode has unsupported value %q", cfg.Service.Mode)
	}
	if cfg.Service.RuntimeStateIdleSec < 0 {
		return errors.New("service.runtime_state_idle_sec must be >=0")
	}
	if cfg.Service.RuntimeStateMax < 0 {
		return errors.New("service.runtime_state_max must be >=0")
	}
	if strings.TrimSpace(cfg.Ingest.HTTP.Listen) == "" {
		return errors.New("ingest.http.listen is required")
	}
	if strings.TrimSpace(cfg.Ingest.HTTP.HealthPath) == "" {
		return errors.New("ingest.http.health_path is required")
	}
	if strings.TrimSpace(cfg.Ingest.HTTP.ReadyPath) == "" {
		return errors.New("ingest.http.ready_path is required")
	}
	if strings.TrimSpace(cfg.Ingest.HTTP.IngestPath) == "" {
		return errors.New("ingest.http.ingest_path is required")
	}
	if mode == ServiceModeSingle {
		if !cfg.Ingest.HTTP.Enabled {
			return errors.New("ingest.http.enabled must be true when service.mode=single")
		}
	}
	if mode == ServiceModeNATS {
		if len(cfg.Ingest.NATS.URL) == 0 {
			return errors.New("ingest.nats.url is required")
		}
		for i, url := range cfg.Ingest.NATS.URL {
			if strings.TrimSpace(url) == "" {
				return fmt.Errorf("ingest.nats.url[%d] is empty", i)
			}
		}
		if cfg.Ingest.NATS.Enabled {
			if cfg.Ingest.NATS.Workers <= 0 {
				return errors.New("ingest.nats.workers must be >0 when ingest.nats.enabled=true")
			}
			if cfg.Ingest.NATS.AckWaitSec <= 0 {
				return errors.New("ingest.nats.ack_wait_sec must be >0 when ingest.nats.enabled=true")
			}
			if cfg.Ingest.NATS.NackDelayMS < 0 {
				return errors.New("ingest.nats.nack_delay_ms must be >=0")
			}
			if cfg.Ingest.NATS.MaxDeliver == 0 || cfg.Ingest.NATS.MaxDeliver < -1 {
				return errors.New("ingest.nats.max_deliver must be -1 or >0")
			}
			if cfg.Ingest.NATS.MaxAckPending <= 0 {
				return errors.New("ingest.nats.max_ack_pending must be >0 when ingest.nats.enabled=true")
			}
		}
	}

	if err := validateLogSink("log.console", cfg.Log.Console, false); err != nil {
		return err
	}
	if err := validateLogSink("log.file", cfg.Log.File, true); err != nil {
		return err
	}

	if cfg.Notify.Telegram.Enabled {
		if strings.TrimSpace(cfg.Notify.Telegram.BotToken) == "" {
			return errors.New("notify.telegram.bot_token is required when notify.telegram.enabled=true")
		}
		if strings.TrimSpace(cfg.Notify.Telegram.ChatID) == "" {
			return errors.New("notify.telegram.chat_id is required when notify.telegram.enabled=true")
		}
	}
	if cfg.Notify.HTTP.Enabled && strings.TrimSpace(cfg.Notify.HTTP.URL) == "" {
		return errors.New("notify.http.url is required when notify.http.enabled=true")
	}
	if cfg.Notify.Queue.Enabled {
		if cfg.Notify.Queue.AckWaitSec <= 0 {
			return errors.New("notify.queue.ack_wait_sec must be >0 when notify.queue.enabled=true")
		}
		if cfg.Notify.Queue.NackDelayMS < 0 {
			return errors.New("notify.queue.nack_delay_ms must be >=0")
		}
		if cfg.Notify.Queue.MaxDeliver == 0 || cfg.Notify.Queue.MaxDeliver < -1 {
			return errors.New("notify.queue.max_deliver must be -1 or >0")
		}
		if cfg.Notify.Queue.MaxAckPending <= 0 {
			return errors.New("notify.queue.max_ack_pending must be >0 when notify.queue.enabled=true")
		}
	}
	if cfg.Notify.Queue.DLQ {
		if !cfg.Notify.Queue.Enabled {
			return errors.New("notify.queue.dlq requires notify.queue.enabled=true")
		}
	}
	if cfg.Notify.Mattermost.Enabled && strings.TrimSpace(cfg.Notify.Mattermost.BaseURL) == "" {
		return errors.New("notify.mattermost.base_url is required when notify.mattermost.enabled=true")
	}
	if cfg.Notify.Mattermost.Enabled && strings.TrimSpace(cfg.Notify.Mattermost.BotToken) == "" {
		return errors.New("notify.mattermost.bot_token is required when notify.mattermost.enabled=true")
	}
	if cfg.Notify.Mattermost.Enabled && strings.TrimSpace(cfg.Notify.Mattermost.ChannelID) == "" {
		return errors.New("notify.mattermost.channel_id is required when notify.mattermost.enabled=true")
	}
	if cfg.Notify.Mattermost.Enabled && cfg.Notify.Mattermost.TimeoutSec <= 0 {
		return errors.New("notify.mattermost.timeout_sec must be >0 when notify.mattermost.enabled=true")
	}
	if err := validateTrackerNotifier("jira", cfg.Notify.Jira); err != nil {
		return err
	}
	if err := validateTrackerNotifier("youtrack", cfg.Notify.YouTrack); err != nil {
		return err
	}
	templateByChannel, err := validateNotifyTemplates(cfg.Notify)
	if err != nil {
		return err
	}

	ruleNames := make(map[string]struct{}, len(cfg.Rule))
	for i, rule := range cfg.Rule {
		if err := validateRule(rule); err != nil {
			return fmt.Errorf("rule[%d] %q: %w", i, rule.Name, err)
		}
		if _, exists := ruleNames[rule.Name]; exists {
			return fmt.Errorf("duplicate rule name %q", rule.Name)
		}
		ruleNames[rule.Name] = struct{}{}

		if err := validateRuleNotifyRoutes(cfg.Notify, rule, templateByChannel); err != nil {
			return fmt.Errorf("rule[%d] %q: %w", i, rule.Name, err)
		}
	}

	return nil
}

// validateRule validates one alert rule against schema constraints.
// Params: one decoded rule.
// Returns: rule-level validation error.
func validateRule(rule RuleConfig) error {
	if strings.TrimSpace(rule.Name) == "" {
		return errors.New("name is required")
	}

	switch rule.AlertType {
	case "count_total", "count_window", "missing_heartbeat":
	default:
		return fmt.Errorf("unsupported alert_type %q", rule.AlertType)
	}

	if len(rule.Match.Type) == 0 {
		return errors.New("match.type is required")
	}
	for _, t := range rule.Match.Type {
		if t != "event" && t != "agg" {
			return fmt.Errorf("unsupported match.type value %q", t)
		}
	}
	if len(rule.Match.Var) == 0 {
		return errors.New("match.var is required")
	}
	if len(rule.Key.FromTags) == 0 {
		return errors.New("key.from_tags is required")
	}

	if rule.Match.Value != nil {
		if err := validateValuePredicate(rule.Match.Value); err != nil {
			return fmt.Errorf("match.value: %w", err)
		}
	}

	if rule.Pending.Enabled && rule.Pending.DelaySec <= 0 {
		return errors.New("pending.delay_sec must be >0 when pending.enabled=true")
	}
	if len(rule.Notify.Route) == 0 {
		return errors.New("notify.route is required")
	}

	switch rule.AlertType {
	case "count_total":
		if rule.Raise.N < 1 {
			return errors.New("raise.n must be >=1")
		}
		if rule.Resolve.SilenceSec < 0 {
			return errors.New("resolve.silence_sec must be >=0")
		}
		if rule.Resolve.HysteresisSec < 0 {
			return errors.New("resolve.hysteresis_sec must be >=0")
		}
		if rule.Raise.TaggSec > 0 || rule.Raise.MissingSec > 0 {
			return errors.New("count_total forbids raise.tagg_sec and raise.missing_sec")
		}
	case "count_window":
		if rule.Raise.N < 1 {
			return errors.New("raise.n must be >=1")
		}
		if rule.Raise.TaggSec <= 0 {
			return errors.New("raise.tagg_sec must be >0")
		}
		if rule.Resolve.SilenceSec < 0 {
			return errors.New("resolve.silence_sec must be >=0")
		}
		if rule.Resolve.HysteresisSec < 0 {
			return errors.New("resolve.hysteresis_sec must be >=0")
		}
		if rule.Raise.MissingSec > 0 {
			return errors.New("count_window forbids raise.missing_sec")
		}
	case "missing_heartbeat":
		if rule.Raise.MissingSec <= 0 {
			return errors.New("raise.missing_sec must be >0")
		}
		if rule.Resolve.HysteresisSec < 0 {
			return errors.New("resolve.hysteresis_sec must be >=0")
		}
		if rule.Raise.N > 0 || rule.Raise.TaggSec > 0 || rule.Resolve.SilenceSec != 0 {
			return errors.New("missing_heartbeat forbids raise.n, raise.tagg_sec, resolve.silence_sec")
		}
	}

	return nil
}

// validateValuePredicate validates typed value predicate operators.
// Params: predicate definition.
// Returns: predicate validation error.
func validateValuePredicate(predicate *RuleValuePredicate) error {
	if predicate == nil {
		return errors.New("predicate is nil")
	}
	predicate.CompiledMatchRE = nil
	predicate.CompiledWildcardRE = nil

	switch predicate.Type {
	case "n":
		if !IsSupportedValuePredicateOp(predicate.Type, predicate.Op) {
			return fmt.Errorf("unsupported numeric op %q", predicate.Op)
		}
		if predicate.N == nil {
			return errors.New("n operand is required for type=n")
		}
	case "s":
		if !IsSupportedValuePredicateOp(predicate.Type, predicate.Op) {
			return fmt.Errorf("unsupported string op %q", predicate.Op)
		}
		if predicate.Op == "in" {
			if len(predicate.In) == 0 {
				return errors.New("in operand requires non-empty list")
			}
		} else if predicate.S == nil {
			return errors.New("s operand is required for string op")
		}
		if predicate.Op == "match" {
			compiled, err := regexp.Compile(*predicate.S)
			if err != nil {
				return fmt.Errorf("invalid match regex: %w", err)
			}
			predicate.CompiledMatchRE = compiled
		}
		if predicate.Op == "*" {
			compiled, err := CompileWildcardPattern(*predicate.S)
			if err != nil {
				return fmt.Errorf("invalid wildcard pattern: %w", err)
			}
			predicate.CompiledWildcardRE = compiled
		}
	case "b":
		if !IsSupportedValuePredicateOp(predicate.Type, predicate.Op) {
			return fmt.Errorf("unsupported bool op %q", predicate.Op)
		}
		if predicate.B == nil {
			return errors.New("b operand is required for type=b")
		}
	default:
		return fmt.Errorf("unsupported value type %q", predicate.Type)
	}
	return nil
}

// IsSupportedValuePredicateOp reports whether op is allowed for a given typed predicate.
// Params: predicate value type and operator.
// Returns: true when operator is supported for the type.
func IsSupportedValuePredicateOp(valueType, op string) bool {
	ops, ok := supportedValuePredicateOps[valueType]
	if !ok {
		return false
	}
	_, exists := ops[op]
	return exists
}

// hasIngestConfig reports whether ingest section has explicit values.
// Params: ingest configuration fragment.
// Returns: true when section should be merged into destination snapshot.
func hasIngestConfig(cfg IngestConfig) bool {
	return hasHTTPIngestConfig(cfg.HTTP) || hasNATSIngestConfig(cfg.NATS)
}

// hasHTTPIngestConfig reports whether HTTP ingest section has explicit values.
// Params: HTTP ingest configuration fragment.
// Returns: true when section should be merged.
func hasHTTPIngestConfig(cfg HTTPIngestConfig) bool {
	return cfg.Enabled ||
		strings.TrimSpace(cfg.Listen) != "" ||
		strings.TrimSpace(cfg.HealthPath) != "" ||
		strings.TrimSpace(cfg.ReadyPath) != "" ||
		strings.TrimSpace(cfg.IngestPath) != "" ||
		cfg.MaxBodyBytes != 0
}

// hasNATSIngestConfig reports whether NATS ingest section has explicit values.
// Params: NATS ingest configuration fragment.
// Returns: true when section should be merged.
func hasNATSIngestConfig(cfg NATSIngestConfig) bool {
	return cfg.Enabled ||
		len(cfg.URL) > 0 ||
		cfg.Workers != 0 ||
		cfg.AckWaitSec != 0 ||
		cfg.NackDelayMS != 0 ||
		cfg.MaxDeliver != 0 ||
		cfg.MaxAckPending != 0
}

// normalizeNATSURLs trims spaces around each configured NATS URL.
// Params: raw URL list from config.
// Returns: normalized URL list preserving element count for validation.
func normalizeNATSURLs(urls []string) []string {
	if len(urls) == 0 {
		return nil
	}
	out := make([]string, len(urls))
	for i := range urls {
		out[i] = strings.TrimSpace(urls[i])
	}
	return out
}

// CompileWildcardPattern converts wildcard syntax (*, ?) into regex and compiles it.
// Params: wildcard expression from rule config.
// Returns: compiled case-insensitive regex.
func CompileWildcardPattern(pattern string) (*regexp.Regexp, error) {
	replacer := strings.NewReplacer(
		".", "\\.",
		"+", "\\+",
		"(", "\\(",
		")", "\\)",
		"[", "\\[",
		"]", "\\]",
		"{", "\\{",
		"}", "\\}",
		"^", "\\^",
		"$", "\\$",
		"|", "\\|",
	)
	normalized := replacer.Replace(strings.ToLower(pattern))
	normalized = strings.ReplaceAll(normalized, "*", ".*")
	normalized = strings.ReplaceAll(normalized, "?", ".")
	return regexp.Compile("^" + normalized + "$")
}

// validateNotifyTemplates validates channel-scoped notify templates and returns lookup by channel+name.
// Params: notify section from config snapshot.
// Returns: normalized template map by channel and template name.
func validateNotifyTemplates(notifyCfg NotifyConfig) (map[string]map[string]NamedTemplateConfig, error) {
	byChannel := make(map[string]map[string]NamedTemplateConfig)
	for _, channel := range NotifyChannelNames() {
		pathPrefix := "notify." + channel + ".name-template"
		if err := collectChannelTemplates(byChannel, channel, pathPrefix, NotifyChannelTemplates(notifyCfg, channel)); err != nil {
			return nil, err
		}
	}
	return byChannel, nil
}

// collectChannelTemplates validates one channel template list and stores normalized entries.
// Params: destination index, channel name, path prefix, and raw template list.
// Returns: validation error when one template entry is invalid.
func collectChannelTemplates(index map[string]map[string]NamedTemplateConfig, channel, pathPrefix string, templates []NamedTemplateConfig) error {
	if len(templates) == 0 {
		return nil
	}
	byName := make(map[string]NamedTemplateConfig, len(templates))
	for i, templateConfig := range templates {
		name := strings.TrimSpace(templateConfig.Name)
		if name == "" {
			return fmt.Errorf("%s[%d].name is required", pathPrefix, i)
		}
		nameKey := strings.ToLower(name)
		if _, exists := byName[nameKey]; exists {
			return fmt.Errorf("duplicate %s name %q", pathPrefix, name)
		}
		if err := validateMessageTemplate(fmt.Sprintf("%s[%d].message", pathPrefix, i), templateConfig.Message); err != nil {
			return err
		}
		templateConfig.Name = name
		byName[nameKey] = templateConfig
	}
	index[channel] = byName
	return nil
}

// validateRuleNotifyRoutes validates rule-level channel/template bindings.
// Params: global notify config, one rule, and template lookup.
// Returns: validation error on unknown template/channel mismatch/disabled channel.
func validateRuleNotifyRoutes(notifyCfg NotifyConfig, rule RuleConfig, templates map[string]map[string]NamedTemplateConfig) error {
	usedRouteKeys := make(map[string]struct{}, len(rule.Notify.Route))
	for index, route := range rule.Notify.Route {
		channel := NormalizeNotifyChannel(route.Channel)
		if !IsSupportedNotifyChannel(channel) {
			return fmt.Errorf("notify.route[%d].channel has unsupported value %q", index, route.Channel)
		}
		if !NotifyChannelEnabled(notifyCfg, channel) {
			return fmt.Errorf("notify.route[%d].channel %q is disabled in [notify.%s]", index, route.Channel, channel)
		}
		routeKey := strings.ToLower(strings.TrimSpace(route.Name))
		if routeKey == "" {
			routeKey = channel
		}
		if _, exists := usedRouteKeys[routeKey]; exists {
			return fmt.Errorf("notify.route has duplicate key %q", routeKey)
		}
		usedRouteKeys[routeKey] = struct{}{}

		templateName := strings.TrimSpace(route.Template)
		if templateName == "" {
			return fmt.Errorf("notify.route[%d].template is required", index)
		}
		templatesByName, exists := templates[channel]
		if !exists {
			return fmt.Errorf("notify.route[%d].template %q is not defined in [[notify.%s.name-template]]", index, route.Template, channel)
		}
		if _, exists := templatesByName[strings.ToLower(templateName)]; !exists {
			return fmt.Errorf("notify.route[%d].template %q is not defined in [[notify.%s.name-template]]", index, route.Template, channel)
		}

		mode := NormalizeNotifyRouteMode(route.Mode)
		if !IsSupportedNotifyRouteMode(mode) {
			return fmt.Errorf("notify.route[%d].mode has unsupported value %q", index, route.Mode)
		}
		if mode != NotifyRouteModeActiveOnly {
			continue
		}
		switch channel {
		case NotifyChannelTelegram:
			if strings.TrimSpace(notifyCfg.Telegram.ActiveChatID) == "" {
				return fmt.Errorf("notify.route[%d] active_only requires notify.telegram.active_chat_id", index)
			}
		case NotifyChannelMattermost:
			if strings.TrimSpace(notifyCfg.Mattermost.ActiveChannelID) == "" {
				return fmt.Errorf("notify.route[%d] active_only requires notify.mattermost.active_channel_id", index)
			}
		default:
			return fmt.Errorf("notify.route[%d] active_only is supported only for telegram and mattermost", index)
		}
	}
	return nil
}

// validateTrackerNotifier validates one tracker transport section.
// Params: tracker channel name and tracker config.
// Returns: validation error for invalid tracker settings.
func validateTrackerNotifier(channel string, cfg TrackerNotifier) error {
	prefix := "notify." + channel
	if !cfg.Enabled {
		return nil
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return fmt.Errorf("%s.base_url is required when %s.enabled=true", prefix, prefix)
	}
	if cfg.TimeoutSec <= 0 {
		return fmt.Errorf("%s.timeout_sec must be >0 when %s.enabled=true", prefix, prefix)
	}
	if err := validateTrackerAction(prefix+".create", cfg.Create, true); err != nil {
		return err
	}
	if err := validateTrackerAction(prefix+".resolve", cfg.Resolve, false); err != nil {
		return err
	}
	if err := validateTrackerAuth(prefix+".auth", cfg.Auth); err != nil {
		return err
	}
	return nil
}

// validateTrackerAction validates one tracker action definition.
// Params: action path prefix, action config, and create/resolve selector.
// Returns: action validation error.
func validateTrackerAction(pathPrefix string, cfg TrackerActionConfig, isCreate bool) error {
	if strings.TrimSpace(cfg.Path) == "" {
		return fmt.Errorf("%s.path is required", pathPrefix)
	}
	if strings.TrimSpace(cfg.Method) == "" {
		return fmt.Errorf("%s.method is required", pathPrefix)
	}
	if len(cfg.SuccessStatus) == 0 {
		return fmt.Errorf("%s.success_status is required", pathPrefix)
	}
	for i, statusCode := range cfg.SuccessStatus {
		if statusCode < 100 || statusCode > 599 {
			return fmt.Errorf("%s.success_status[%d] must be valid HTTP status code", pathPrefix, i)
		}
	}
	if isCreate && strings.TrimSpace(cfg.RefJSONPath) == "" {
		return fmt.Errorf("%s.ref_json_path is required for create action", pathPrefix)
	}
	if err := validateMessageTemplate(pathPrefix+".path", cfg.Path); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.BodyTemplate) != "" {
		if err := validateMessageTemplate(pathPrefix+".body_template", cfg.BodyTemplate); err != nil {
			return err
		}
	}
	for header, value := range cfg.Headers {
		if strings.TrimSpace(header) == "" {
			return fmt.Errorf("%s.headers contains empty key", pathPrefix)
		}
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s.headers[%q] is empty", pathPrefix, header)
		}
		if err := validateMessageTemplate(pathPrefix+".headers."+header, value); err != nil {
			return err
		}
	}
	return nil
}

// validateTrackerAuth validates tracker auth section.
// Params: auth path prefix and auth config.
// Returns: auth validation error.
func validateTrackerAuth(pathPrefix string, cfg TrackerAuthConfig) error {
	authType := strings.ToLower(strings.TrimSpace(cfg.Type))
	if authType == "" {
		return nil
	}
	switch authType {
	case "none":
		return nil
	case "bearer":
		if strings.TrimSpace(cfg.Token) == "" {
			return fmt.Errorf("%s.token is required when %s.type=bearer", pathPrefix, pathPrefix)
		}
		return nil
	case "basic":
		if strings.TrimSpace(cfg.Username) == "" {
			return fmt.Errorf("%s.username is required when %s.type=basic", pathPrefix, pathPrefix)
		}
		if strings.TrimSpace(cfg.Password) == "" {
			return fmt.Errorf("%s.password is required when %s.type=basic", pathPrefix, pathPrefix)
		}
		return nil
	case "header":
		if strings.TrimSpace(cfg.Header) == "" {
			return fmt.Errorf("%s.header is required when %s.type=header", pathPrefix, pathPrefix)
		}
		if strings.TrimSpace(cfg.Token) == "" {
			return fmt.Errorf("%s.token is required when %s.type=header", pathPrefix, pathPrefix)
		}
		return nil
	default:
		return fmt.Errorf("%s.type has unsupported value %q", pathPrefix, cfg.Type)
	}
}

// NormalizeNotifyChannel canonicalizes notify channel keys.
// Params: raw channel name from config.
// Returns: normalized lowercase channel key.
func NormalizeNotifyChannel(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// NormalizeNotifyRouteMode canonicalizes notify route mode and applies default.
// Params: raw mode value from config.
// Returns: normalized route mode (`history` by default).
func NormalizeNotifyRouteMode(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return NotifyRouteModeHistory
	}
	return normalized
}

// NormalizeServiceMode canonicalizes service mode and applies default.
// Params: raw mode value from config.
// Returns: normalized mode (`nats` by default).
func NormalizeServiceMode(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return ServiceModeNATS
	}
	return normalized
}

// IsSupportedServiceMode reports whether mode value is supported.
// Params: normalized mode value.
// Returns: true for known modes.
func IsSupportedServiceMode(mode string) bool {
	switch NormalizeServiceMode(mode) {
	case ServiceModeNATS, ServiceModeSingle:
		return true
	default:
		return false
	}
}

// IsSupportedNotifyRouteMode reports whether route mode value is supported.
// Params: normalized route mode.
// Returns: true for known modes.
func IsSupportedNotifyRouteMode(mode string) bool {
	switch NormalizeNotifyRouteMode(mode) {
	case NotifyRouteModeHistory, NotifyRouteModeActiveOnly:
		return true
	default:
		return false
	}
}

// NotifyChannelNames returns deterministic list of supported channel keys.
// Params: none.
// Returns: ordered channel key list.
func NotifyChannelNames() []string {
	out := make([]string, len(notifyChannelOrder))
	copy(out, notifyChannelOrder)
	return out
}

// IsSupportedNotifyChannel reports whether channel key is supported.
// Params: normalized channel key.
// Returns: true when channel is one of known transports.
func IsSupportedNotifyChannel(channel string) bool {
	_, exists := notifyChannelRegistry[NormalizeNotifyChannel(channel)]
	return exists
}

// NotifyChannelEnabled checks if channel transport is enabled globally.
// Params: global notify config and normalized channel key.
// Returns: true when corresponding transport section is enabled.
func NotifyChannelEnabled(cfg NotifyConfig, channel string) bool {
	descriptor, ok := notifyChannelDescriptorByName(channel)
	if !ok || descriptor.enabled == nil {
		return false
	}
	return descriptor.enabled(cfg)
}

// NotifyChannelRetry returns retry policy for one channel.
// Params: global notify config and channel key.
// Returns: retry policy for channel transport.
func NotifyChannelRetry(cfg NotifyConfig, channel string) NotifyRetry {
	descriptor, ok := notifyChannelDescriptorByName(channel)
	if !ok || descriptor.retry == nil {
		return NotifyRetry{}
	}
	return descriptor.retry(cfg)
}

// NotifyChannelTemplates returns template catalog for one channel.
// Params: global notify config and channel key.
// Returns: channel template list copy.
func NotifyChannelTemplates(cfg NotifyConfig, channel string) []NamedTemplateConfig {
	descriptor, ok := notifyChannelDescriptorByName(channel)
	if !ok || descriptor.templates == nil {
		return nil
	}
	return append([]NamedTemplateConfig(nil), descriptor.templates(cfg)...)
}

// notifyChannelDescriptorByName returns channel metadata descriptor by key.
// Params: raw or normalized channel key.
// Returns: descriptor and existence flag.
func notifyChannelDescriptorByName(channel string) (notifyChannelDescriptor, bool) {
	descriptor, exists := notifyChannelRegistry[NormalizeNotifyChannel(channel)]
	return descriptor, exists
}

// validateMessageTemplate parses one text template and checks it is non-empty.
// Params: field path and template body.
// Returns: parse/empty error.
func validateMessageTemplate(path, body string) error {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return fmt.Errorf("%s is required", path)
	}
	if _, err := templatefmt.ParseNotificationTemplate(path, trimmed); err != nil {
		return fmt.Errorf("%s is invalid: %w", path, err)
	}
	return nil
}

// validateLogSink validates one log sink configuration.
// Params: sink name, sink values, and whether path is required.
// Returns: sink validation error.
func validateLogSink(name string, sink LogSinkConfig, requirePath bool) error {
	if !sink.Enabled {
		return nil
	}

	switch strings.ToLower(strings.TrimSpace(sink.Level)) {
	case "debug", "info", "warn", "error", "panic":
	default:
		return fmt.Errorf("%s.level has unsupported value %q", name, sink.Level)
	}

	switch strings.ToLower(strings.TrimSpace(sink.Format)) {
	case "line", "json":
	default:
		return fmt.Errorf("%s.format has unsupported value %q", name, sink.Format)
	}

	if requirePath && strings.TrimSpace(sink.Path) == "" {
		return fmt.Errorf("%s.path is required", name)
	}

	return nil
}
