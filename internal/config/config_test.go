package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	ingestHTTPEnabled = `[ingest.http]
enabled = true`
	ingestHTTPListen = `[ingest.http]
enabled = true
listen = "127.0.0.1:18081"`
	ingestHTTPDisabled = `[ingest.http]
enabled = false`
	ingestNATSEnabled = `[ingest.nats]
enabled = true`
)

func TestLoadSnapshotFromFile(t *testing.T) {
	t.Parallel()

	cfg := mustLoadSnapshot(t, joinSections(
		serviceSection(""),
		ingestHTTPListen,
		`[notify]
repeat = true
repeat_every_sec = 300`,
		telegramNotifySection("token", "chat", "", "tg_default", "[{{ .RuleName }}] {{ .Message }}"),
		`[notify.telegram.retry]
enabled = true
backoff = "exponential"
initial_ms = 10
max_ms = 100
max_attempts = 0
log_each_attempt = true`,
		countTotalRule(routeBlock("telegram", "tg_default")),
	))

	if cfg.Service.Name != "alerting" {
		t.Fatalf("unexpected service name %q", cfg.Service.Name)
	}
	if len(cfg.Rule) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(cfg.Rule))
	}
	if cfg.Rule[0].Name != "ct" {
		t.Fatalf("unexpected rule name %q", cfg.Rule[0].Name)
	}
	if len(cfg.Rule[0].Notify.Route) != 1 {
		t.Fatalf("expected 1 notify route, got %d", len(cfg.Rule[0].Notify.Route))
	}
}

func TestLoadSnapshotFromDirAndDuplicateRuleValidation(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	writeConfigFile(t, filepath.Join(tmpDir, "a.toml"), joinSections(
		telegramNotifySection("token", "chat", "", "tg_default", "{{ .Message }}"),
		countTotalRule(routeBlock("telegram", "tg_default")),
	))
	writeConfigFile(t, filepath.Join(tmpDir, "b.toml"), countTotalRule(routeBlock("telegram", "tg_default")))

	_, err := LoadSnapshot(ConfigSource{Dir: tmpDir})
	if err == nil {
		t.Fatalf("expected duplicate rule validation error")
	}
	if !strings.Contains(err.Error(), "duplicate rule name") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadSnapshotRejectsEnabledTelegramWithoutCredentials(t *testing.T) {
	t.Parallel()

	err := loadSnapshotErr(t, joinSections(
		serviceSection(""),
		ingestHTTPEnabled,
		telegramNotifySection("", "", "", "tg_default", "{{ .Message }}"),
		countTotalRule(routeBlock("telegram", "tg_default")),
	))
	if !strings.Contains(err.Error(), "notify.telegram.bot_token") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadSnapshotTemplateValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{
			name: "reject invalid telegram template",
			content: joinSections(
				serviceSection(""),
				ingestHTTPEnabled,
				telegramNotifySection("token", "chat", "", "tg_default", "{{ .AlertID "),
				countTotalRule(routeBlock("telegram", "tg_default")),
			),
			wantErr: "notify.telegram.name-template[0].message",
		},
		{
			name: "allow fmtDuration template func",
			content: joinSections(
				serviceSection(""),
				ingestHTTPEnabled,
				telegramNotifySection("token", "chat", "", "tg_default", "delta={{ fmtDuration .Duration }}"),
				countTotalRule(routeBlock("telegram", "tg_default")),
			),
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := loadSnapshotFromContent(t, tt.content)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("load snapshot: %v", err)
				}
				if len(cfg.Rule) != 1 {
					t.Fatalf("expected one rule, got %d", len(cfg.Rule))
				}
				return
			}
			if err == nil {
				t.Fatalf("expected validation error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestLoadSnapshotMattermostValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		section string
		wantErr string
		assert  func(*testing.T, Config)
	}{
		{
			name:    "accept enabled mattermost",
			section: mattermostNotifySection("https://mattermost.example", "mm-bot-token", "channel-123", 0, "mm_default", "[{{ .RuleName }}] {{ .State }}"),
			assert: func(t *testing.T, cfg Config) {
				t.Helper()
				if !cfg.Notify.Mattermost.Enabled {
					t.Fatalf("expected mattermost enabled")
				}
				if cfg.Notify.Mattermost.TimeoutSec != 10 {
					t.Fatalf("expected default mattermost timeout=10, got %d", cfg.Notify.Mattermost.TimeoutSec)
				}
			},
		},
		{
			name:    "reject missing base_url",
			section: mattermostNotifySection("", "mm-bot-token", "channel-123", 2, "mm_default", "{{ .Message }}"),
			wantErr: "notify.mattermost.base_url",
		},
		{
			name:    "reject missing bot_token",
			section: mattermostNotifySection("https://mattermost.example", "", "channel-123", 0, "mm_default", "{{ .Message }}"),
			wantErr: "notify.mattermost.bot_token",
		},
		{
			name:    "reject missing channel_id",
			section: mattermostNotifySection("https://mattermost.example", "mm-bot-token", "", 0, "mm_default", "{{ .Message }}"),
			wantErr: "notify.mattermost.channel_id",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := loadSnapshotFromContent(t, joinSections(
				serviceSection(""),
				ingestHTTPEnabled,
				tt.section,
				countTotalRule(routeBlock("mattermost", "mm_default")),
			))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("load snapshot: %v", err)
				}
				if tt.assert != nil {
					tt.assert(t, cfg)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected validation error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestLoadSnapshotNATSIngestValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		ingestNATS string
		wantErr    string
		assert     func(*testing.T, Config)
	}{
		{
			name:       "applies nats ingest defaults",
			ingestNATS: ingestNATSEnabled,
			assert: func(t *testing.T, cfg Config) {
				t.Helper()
				if cfg.Ingest.NATS.Stream == "" || cfg.Ingest.NATS.ConsumerName == "" || cfg.Ingest.NATS.DeliverGroup == "" {
					t.Fatalf("nats ingest defaults were not applied: %+v", cfg.Ingest.NATS)
				}
				if len(cfg.Ingest.NATS.URL) != 1 || cfg.Ingest.NATS.URL[0] != "nats://127.0.0.1:4222" {
					t.Fatalf("unexpected nats ingest urls: %#v", cfg.Ingest.NATS.URL)
				}
				if cfg.Ingest.NATS.MaxDeliver == 0 || cfg.Ingest.NATS.AckWaitSec <= 0 || cfg.Ingest.NATS.MaxAckPending <= 0 {
					t.Fatalf("unexpected nats ingest defaults: %+v", cfg.Ingest.NATS)
				}
				if cfg.Ingest.NATS.Workers != 1 {
					t.Fatalf("unexpected nats ingest workers default: %d", cfg.Ingest.NATS.Workers)
				}
			},
		},
		{
			name: "reject invalid max_deliver",
			ingestNATS: `[ingest.nats]
enabled = true
max_deliver = -2`,
			wantErr: "ingest.nats.max_deliver",
		},
		{
			name: "reject invalid workers",
			ingestNATS: `[ingest.nats]
enabled = true
workers = -1`,
			wantErr: "ingest.nats.workers",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := loadSnapshotFromContent(t, joinSections(
				serviceSection(""),
				ingestHTTPDisabled,
				tt.ingestNATS,
				telegramNotifySection("token", "chat", "", "tg_default", "{{ .Message }}"),
				countTotalRule(routeBlock("telegram", "tg_default")),
			))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("load snapshot: %v", err)
				}
				if tt.assert != nil {
					tt.assert(t, cfg)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected validation error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestLoadSnapshotSingleModeBehavior(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		wantErr string
		assert  func(*testing.T, Config)
	}{
		{
			name: "accept single mode without nats",
			content: joinSections(
				serviceSection("single"),
				ingestHTTPListen,
				httpNotifySection("http://127.0.0.1:1/notify", "http_default", "{{ .Message }}"),
				countTotalRule(routeBlock("http", "http_default")),
			),
			assert: func(t *testing.T, cfg Config) {
				t.Helper()
				if cfg.Service.Mode != "single" {
					t.Fatalf("unexpected service mode %q", cfg.Service.Mode)
				}
			},
		},
		{
			name: "single mode auto-disables ingest nats",
			content: joinSections(
				serviceSection("single"),
				ingestHTTPEnabled,
				ingestNATSEnabled,
				httpNotifySection("http://127.0.0.1:1/notify", "http_default", "{{ .Message }}"),
				countTotalRule(routeBlock("http", "http_default")),
			),
			assert: func(t *testing.T, cfg Config) {
				t.Helper()
				if cfg.Ingest.NATS.Enabled {
					t.Fatalf("expected ingest.nats.enabled=false in single mode")
				}
			},
		},
		{
			name: "single mode auto-disables notify queue",
			content: joinSections(
				serviceSection("single"),
				ingestHTTPEnabled,
				`[notify.queue]
enabled = true`,
				httpNotifySection("http://127.0.0.1:1/notify", "http_default", "{{ .Message }}"),
				countTotalRule(routeBlock("http", "http_default")),
			),
			assert: func(t *testing.T, cfg Config) {
				t.Helper()
				if cfg.Notify.Queue.Enabled {
					t.Fatalf("expected notify.queue.enabled=false in single mode")
				}
			},
		},
		{
			name: "reject single mode without http ingest",
			content: joinSections(
				serviceSection("single"),
				ingestHTTPDisabled,
				httpNotifySection("http://127.0.0.1:1/notify", "http_default", "{{ .Message }}"),
				countTotalRule(routeBlock("http", "http_default")),
			),
			wantErr: "ingest.http.enabled",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := loadSnapshotFromContent(t, tt.content)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("load snapshot: %v", err)
				}
				if tt.assert != nil {
					tt.assert(t, cfg)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected validation error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestLoadSnapshotNotifyQueueValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		notifyQueue  string
		wantErr      string
		assertLoaded func(*testing.T, Config)
	}{
		{
			name: "accept enabled queue",
			notifyQueue: `[notify.queue]
enabled = true
ack_wait_sec = 10
nack_delay_ms = 100
max_deliver = 3
max_ack_pending = 100`,
			assertLoaded: func(t *testing.T, cfg Config) {
				t.Helper()
				if !cfg.Notify.Queue.Enabled {
					t.Fatalf("expected queue enabled")
				}
			},
		},
		{
			name: "apply queue defaults when enabled",
			notifyQueue: `[notify.queue]
enabled = true`,
			assertLoaded: func(t *testing.T, cfg Config) {
				t.Helper()
				if !cfg.Notify.Queue.Enabled {
					t.Fatalf("expected queue enabled")
				}
				if cfg.Notify.Queue.AckWaitSec <= 0 {
					t.Fatalf("expected positive queue ack wait, got %d", cfg.Notify.Queue.AckWaitSec)
				}
				if cfg.Notify.Queue.MaxAckPending <= 0 {
					t.Fatalf("expected positive queue max ack pending, got %d", cfg.Notify.Queue.MaxAckPending)
				}
				if len(cfg.Notify.Queue.URL) != 1 || cfg.Notify.Queue.URL[0] != "nats://127.0.0.1:4222" {
					t.Fatalf("unexpected notify queue urls: %#v", cfg.Notify.Queue.URL)
				}
				if cfg.Notify.Queue.DLQ {
					t.Fatalf("expected queue dlq disabled by default")
				}
			},
		},
		{
			name: "accept queue dlq enabled",
			notifyQueue: `[notify.queue]
enabled = true
dlq = true
ack_wait_sec = 10
nack_delay_ms = 100
max_deliver = 3
max_ack_pending = 100`,
			assertLoaded: func(t *testing.T, cfg Config) {
				t.Helper()
				if !cfg.Notify.Queue.DLQ {
					t.Fatalf("expected queue dlq enabled")
				}
			},
		},
		{
			name: "reject dlq when queue disabled",
			notifyQueue: `[notify.queue]
enabled = false
dlq = true`,
			wantErr: "notify.queue.dlq requires notify.queue.enabled=true",
		},
		{
			name: "reject deprecated notify.queue.dlq section",
			notifyQueue: `[notify.queue]
enabled = true
ack_wait_sec = 10
nack_delay_ms = 100
max_deliver = 3
max_ack_pending = 100

[notify.queue.dlq]
enabled = true`,
			wantErr: "[notify.queue.dlq] section is not supported",
		},
		{
			name: "reject deprecated notify.queue.url",
			notifyQueue: `[notify.queue]
enabled = true
url = ["nats://127.0.0.1:4222"]`,
			wantErr: "notify.queue.url is not supported",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := loadSnapshotFromContent(t, joinSections(
				serviceSection(""),
				ingestHTTPEnabled,
				tt.notifyQueue,
				telegramNotifySection("token", "chat", "", "tg_default", "{{ .Message }}"),
				countTotalRule(routeBlock("telegram", "tg_default")),
			))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("load snapshot: %v", err)
				}
				if tt.assertLoaded != nil {
					tt.assertLoaded(t, cfg)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected validation error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestLoadSnapshotRejectsDeprecatedIngestNATSRoutingKeys(t *testing.T) {
	t.Parallel()

	err := loadSnapshotErr(t, joinSections(
		serviceSection(""),
		ingestHTTPEnabled,
		`[ingest.nats]
enabled = true
url = ["nats://127.0.0.1:4222"]
subject = "alerting.events"`,
		telegramNotifySection("token", "chat", "", "tg_default", "{{ .Message }}"),
		countTotalRule(routeBlock("telegram", "tg_default")),
	))
	if !strings.Contains(err.Error(), "ingest.nats.subject/stream/consumer_name/deliver_group are fixed in runtime") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadSnapshotJiraValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		jira    string
		wantErr string
	}{
		{
			name: "accept enabled jira",
			jira: jiraNotifySection(true),
		},
		{
			name:    "reject jira without create ref path",
			jira:    jiraNotifySection(false),
			wantErr: "notify.jira.create.ref_json_path",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := loadSnapshotFromContent(t, joinSections(
				serviceSection(""),
				ingestHTTPEnabled,
				tt.jira,
				countTotalRule(routeBlock("jira", "jira_default")),
			))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("load snapshot: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected validation error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestMergeNotifyConfigAppliesExplicitFalse(t *testing.T) {
	t.Parallel()

	dst := NotifyConfig{
		Repeat:           true,
		RepeatPerChannel: true,
		OnPending:        true,
		Telegram:         TelegramNotifier{Enabled: true},
		HTTP:             HTTPNotifier{Enabled: true},
		Mattermost:       MattermostConfig{Enabled: true},
	}

	src := NotifyConfig{
		Repeat:           false,
		RepeatPerChannel: false,
		OnPending:        false,
		Telegram:         TelegramNotifier{Enabled: false},
		HTTP:             HTTPNotifier{Enabled: false},
		Mattermost:       MattermostConfig{Enabled: false},
	}
	hints := notifyMergeHints{
		Repeat:           boolPtr(false),
		RepeatPerChannel: boolPtr(false),
		OnPending:        boolPtr(false),
		Telegram:         channelMergeHints{Enabled: boolPtr(false)},
		HTTP:             channelMergeHints{Enabled: boolPtr(false)},
		Mattermost:       channelMergeHints{Enabled: boolPtr(false)},
	}

	mergeNotifyConfig(&dst, src, hints)

	if dst.Repeat {
		t.Fatalf("expected repeat=false after explicit false merge")
	}
	if dst.RepeatPerChannel {
		t.Fatalf("expected repeat_per_channel=false after explicit false merge")
	}
	if dst.OnPending {
		t.Fatalf("expected on_pending=false after explicit false merge")
	}
	if dst.Telegram.Enabled {
		t.Fatalf("expected telegram.enabled=false after explicit false merge")
	}
	if dst.HTTP.Enabled {
		t.Fatalf("expected http.enabled=false after explicit false merge")
	}
	if dst.Mattermost.Enabled {
		t.Fatalf("expected mattermost.enabled=false after explicit false merge")
	}
}

func TestLoadDirNotifyExplicitFalseOverrides(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	writeConfigFile(t, filepath.Join(tmpDir, "a.toml"), joinSections(
		`[notify]
repeat = true
repeat_per_channel = true
on_pending = true`,
		`[notify.telegram]
enabled = true
bot_token = "token-a"
chat_id = "chat-a"`,
		`[notify.mattermost]
enabled = true
base_url = "https://mattermost.example"
bot_token = "token-a"
channel_id = "channel-a"`,
	))
	writeConfigFile(t, filepath.Join(tmpDir, "b.toml"), joinSections(
		`[notify]
repeat = false
repeat_per_channel = false
on_pending = false`,
		`[notify.telegram]
enabled = false`,
		`[notify.mattermost]
enabled = false`,
	))

	cfg, err := loadDir(tmpDir)
	if err != nil {
		t.Fatalf("load dir: %v", err)
	}

	if cfg.Notify.Repeat {
		t.Fatalf("expected repeat=false from explicit override")
	}
	if cfg.Notify.RepeatPerChannel {
		t.Fatalf("expected repeat_per_channel=false from explicit override")
	}
	if cfg.Notify.OnPending {
		t.Fatalf("expected on_pending=false from explicit override")
	}
	if cfg.Notify.Telegram.Enabled {
		t.Fatalf("expected telegram.enabled=false from explicit override")
	}
	if cfg.Notify.Telegram.BotToken != "token-a" || cfg.Notify.Telegram.ChatID != "chat-a" {
		t.Fatalf("expected telegram credentials preserved from previous fragment")
	}
	if cfg.Notify.Mattermost.Enabled {
		t.Fatalf("expected mattermost.enabled=false from explicit override")
	}
	if cfg.Notify.Mattermost.BaseURL != "https://mattermost.example" {
		t.Fatalf("expected mattermost base_url preserved from previous fragment")
	}
	if cfg.Notify.Mattermost.BotToken != "token-a" || cfg.Notify.Mattermost.ChannelID != "channel-a" {
		t.Fatalf("expected mattermost credentials preserved from previous fragment")
	}
}

func TestLoadSnapshotRejectsUnsupportedSyntax(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{
			name: "legacy rule array syntax",
			content: joinSections(
				serviceSection(""),
				ingestHTTPEnabled,
				telegramNotifySection("token", "chat", "", "tg_default", "{{ .Message }}"),
				`[[rule]]
name = "legacy_ct"
alert_type = "count_total"

[rule.match]
type = ["event"]
var = ["errors"]
tags = { dc = ["dc1"] }

[rule.key]
from_tags = ["dc"]

[rule.raise]
n = 1

[rule.resolve]
silence_sec = 5

[[rule.notify.route]]
channel = "telegram"
template = "tg_default"`,
			),
			wantErr: "legacy [[rule]] format is not supported",
		},
		{
			name: "state section syntax",
			content: joinSections(
				serviceSection(""),
				ingestHTTPEnabled,
				`[state.nats]
url = ["nats://127.0.0.1:4222"]
tick_bucket = "tick_custom"`,
				telegramNotifySection("token", "chat", "", "tg_default", "{{ .Message }}"),
				countTotalRule(routeBlock("telegram", "tg_default")),
			),
			wantErr: "state configuration is not supported",
		},
		{
			name: "rule body name field",
			content: joinSections(
				serviceSection(""),
				ingestHTTPEnabled,
				telegramNotifySection("token", "chat", "", "tg_default", "{{ .Message }}"),
				`[rule.ct]
name = "ct"
alert_type = "count_total"

[rule.ct.match]
type = ["event"]
var = ["errors"]
tags = { dc = ["dc1"] }

[rule.ct.key]
from_tags = ["dc"]

[rule.ct.raise]
n = 1

[rule.ct.resolve]
silence_sec = 5

[[rule.ct.notify.route]]
channel = "telegram"
template = "tg_default"`,
			),
			wantErr: "rule.ct.name is not supported",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := loadSnapshotErr(t, tt.content)
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestLoadSnapshotMessengerRoutesValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		wantErr string
		assert  func(*testing.T, Config)
	}{
		{
			name: "accept dual routes with active_only",
			content: joinSections(
				serviceSection(""),
				ingestHTTPEnabled,
				telegramNotifySection("token", "history-chat", "active-chat", "tg_history", "history {{ .State }}"),
				`[[notify.telegram.name-template]]
name = "tg_active"
message = "active {{ .State }}"`,
				countTotalRule(
					routeBlock("telegram", "tg_history", `name = "telegram_history"`, `mode = "history"`),
					routeBlock("telegram", "tg_active", `name = "telegram_active"`, `mode = "active_only"`),
				),
			),
			assert: func(t *testing.T, cfg Config) {
				t.Helper()
				if len(cfg.Rule) != 1 || len(cfg.Rule[0].Notify.Route) != 2 {
					t.Fatalf("unexpected routes: %+v", cfg.Rule)
				}
			},
		},
		{
			name: "reject active_only without active destination",
			content: joinSections(
				serviceSection(""),
				ingestHTTPEnabled,
				telegramNotifySection("token", "history-chat", "", "tg_active", "active {{ .State }}"),
				countTotalRule(routeBlock("telegram", "tg_active", `name = "telegram_active"`, `mode = "active_only"`)),
			),
			wantErr: "active_chat_id",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := loadSnapshotFromContent(t, tt.content)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("load snapshot: %v", err)
				}
				if tt.assert != nil {
					tt.assert(t, cfg)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected validation error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func serviceSection(mode string) string {
	if mode == "" {
		return `[service]
name = "alerting"`
	}
	return fmt.Sprintf(`[service]
name = "alerting"
mode = %q`, mode)
}

func countTotalRule(routeBlocks ...string) string {
	sections := []string{
		`[rule.ct]
alert_type = "count_total"`,
		`[rule.ct.match]
type = ["event"]
var = ["errors"]
tags = { dc = ["dc1"] }`,
		`[rule.ct.key]
from_tags = ["dc"]`,
		`[rule.ct.raise]
n = 1`,
		`[rule.ct.resolve]
silence_sec = 5`,
	}
	sections = append(sections, routeBlocks...)
	return joinSections(sections...)
}

func routeBlock(channel, template string, extras ...string) string {
	lines := []string{
		"[[rule.ct.notify.route]]",
		fmt.Sprintf("channel = %q", channel),
		fmt.Sprintf("template = %q", template),
	}
	lines = append(lines, extras...)
	return strings.Join(lines, "\n")
}

func telegramNotifySection(botToken, chatID, activeChatID, templateName, message string) string {
	lines := []string{
		"[notify.telegram]",
		"enabled = true",
		fmt.Sprintf("bot_token = %q", botToken),
		fmt.Sprintf("chat_id = %q", chatID),
	}
	if activeChatID != "" {
		lines = append(lines, fmt.Sprintf("active_chat_id = %q", activeChatID))
	}
	return joinSections(
		strings.Join(lines, "\n"),
		fmt.Sprintf(`[[notify.telegram.name-template]]
name = %q
message = %q`, templateName, message),
	)
}

func mattermostNotifySection(baseURL, botToken, channelID string, timeoutSec int, templateName, message string) string {
	lines := []string{
		"[notify.mattermost]",
		"enabled = true",
		fmt.Sprintf("base_url = %q", baseURL),
		fmt.Sprintf("bot_token = %q", botToken),
		fmt.Sprintf("channel_id = %q", channelID),
	}
	if timeoutSec > 0 {
		lines = append(lines, fmt.Sprintf("timeout_sec = %d", timeoutSec))
	}
	return joinSections(
		strings.Join(lines, "\n"),
		fmt.Sprintf(`[[notify.mattermost.name-template]]
name = %q
message = %q`, templateName, message),
	)
}

func httpNotifySection(url, templateName, message string) string {
	return joinSections(
		fmt.Sprintf(`[notify.http]
enabled = true
url = %q`, url),
		fmt.Sprintf(`[[notify.http.name-template]]
name = %q
message = %q`, templateName, message),
	)
}

func jiraNotifySection(withRefPath bool) string {
	create := `[notify.jira.create]
method = "POST"
path = "/rest/api/3/issue"
body_template = "{\"summary\": {{ json .Message }}}"
success_status = [201]`
	if withRefPath {
		create += "\nref_json_path = \"key\""
	}
	return joinSections(
		`[notify.jira]
enabled = true
base_url = "https://jira.example.com"
timeout_sec = 10`,
		`[notify.jira.auth]
type = "basic"
username = "user@example.com"
password = "token"`,
		create,
		`[notify.jira.resolve]
method = "POST"
path = "/rest/api/3/issue/{{ .ExternalRef }}/transitions"
body_template = "{\"transition\":{\"id\":\"31\"}}"
success_status = [204]`,
		`[[notify.jira.name-template]]
name = "jira_default"
message = "[{{ .RuleName }}] {{ .Message }}"`,
	)
}

func mustLoadSnapshot(t *testing.T, content string) Config {
	t.Helper()
	cfg, err := loadSnapshotFromContent(t, content)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	return cfg
}

func loadSnapshotErr(t *testing.T, content string) error {
	t.Helper()
	_, err := loadSnapshotFromContent(t, content)
	if err == nil {
		t.Fatalf("expected validation error")
	}
	return err
}

func loadSnapshotFromContent(t *testing.T, content string) (Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfigFile(t, path, content)
	return LoadSnapshot(ConfigSource{File: path})
}

func joinSections(parts ...string) string {
	nonEmpty := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		nonEmpty = append(nonEmpty, trimmed)
	}
	return strings.Join(nonEmpty, "\n\n") + "\n"
}

func boolPtr(value bool) *bool {
	return &value
}

func writeConfigFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config file: %v", err)
	}
}
