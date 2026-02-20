package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSnapshotFromFile(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.toml")
	content := `
[service]
name = "alerting"

[ingest.http]
enabled = true
listen = "127.0.0.1:18081"

[notify]
repeat = true
repeat_every_sec = 300

[notify.telegram]
enabled = true
bot_token = "token"
chat_id = "chat"

[notify.telegram.retry]
enabled = true
backoff = "exponential"
initial_ms = 10
max_ms = 100
max_attempts = 0
log_each_attempt = true

[[notify.telegram.name-template]]
name = "tg_default"
message = "[{{ .RuleName }}] {{ .Message }}"

[rule.ct]
alert_type = "count_total"

[rule.ct.match]
type = ["event"]
var = ["errors"]
tags = { dc = ["dc1"], service = ["api"] }

[rule.ct.key]
from_tags = ["dc", "service"]

[rule.ct.raise]
n = 2

[rule.ct.resolve]
silence_sec = 5

[[rule.ct.notify.route]]
channel = "telegram"
template = "tg_default"
`
	writeConfigFile(t, path, content)

	cfg, err := LoadSnapshot(ConfigSource{File: path})
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}

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
	fileA := filepath.Join(tmpDir, "a.toml")
	fileB := filepath.Join(tmpDir, "b.toml")

	writeConfigFile(t, fileA, `
[notify.telegram]
enabled = true
bot_token = "token"
chat_id = "chat"

[[notify.telegram.name-template]]
name = "tg_default"
message = "{{ .Message }}"

[rule.same]
alert_type = "count_total"

[rule.same.match]
type = ["event"]
var = ["errors"]
tags = { dc = ["dc1"], service = ["api"] }

[rule.same.key]
from_tags = ["dc", "service"]

[rule.same.raise]
n = 2

[rule.same.resolve]
silence_sec = 5

[[rule.same.notify.route]]
channel = "telegram"
template = "tg_default"
`)

	writeConfigFile(t, fileB, `
[rule.same]
alert_type = "count_total"

[rule.same.match]
type = ["event"]
var = ["errors"]
tags = { dc = ["dc1"], service = ["api"] }

[rule.same.key]
from_tags = ["dc", "service"]

[rule.same.raise]
n = 2

[rule.same.resolve]
silence_sec = 5

[[rule.same.notify.route]]
channel = "telegram"
template = "tg_default"
`)

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

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.toml")
	content := `
[service]
name = "alerting"

[ingest.http]
enabled = true

[notify.telegram]
enabled = true
bot_token = ""
chat_id = ""

[[notify.telegram.name-template]]
name = "tg_default"
message = "{{ .Message }}"

[rule.ct]
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
template = "tg_default"
`
	writeConfigFile(t, path, content)

	_, err := LoadSnapshot(ConfigSource{File: path})
	if err == nil {
		t.Fatalf("expected validation error")
	}
	if !strings.Contains(err.Error(), "notify.telegram.bot_token") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadSnapshotAcceptsEnabledMattermost(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.toml")
	content := `
[service]
name = "alerting"

[ingest.http]
enabled = true

[notify.mattermost]
enabled = true
base_url = "https://mattermost.example"
bot_token = "mm-bot-token"
channel_id = "channel-123"

[[notify.mattermost.name-template]]
name = "mm_default"
message = "[{{ .RuleName }}] {{ .State }} {{ .Var }}={{ .MetricValue }}"

[rule.mm]
alert_type = "count_total"

[rule.mm.match]
type = ["event"]
var = ["errors"]
tags = { dc = ["dc1"] }

[rule.mm.key]
from_tags = ["dc"]

[rule.mm.raise]
n = 1

[rule.mm.resolve]
silence_sec = 5

[[rule.mm.notify.route]]
channel = "mattermost"
template = "mm_default"
`
	writeConfigFile(t, path, content)

	cfg, err := LoadSnapshot(ConfigSource{File: path})
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if !cfg.Notify.Mattermost.Enabled {
		t.Fatalf("expected mattermost enabled")
	}
	if cfg.Notify.Mattermost.TimeoutSec != 10 {
		t.Fatalf("expected default mattermost timeout=10, got %d", cfg.Notify.Mattermost.TimeoutSec)
	}
}

func TestLoadSnapshotRejectsEnabledMattermostWithoutBaseURL(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.toml")
	content := `
[service]
name = "alerting"

[ingest.http]
enabled = true

[notify.mattermost]
enabled = true
base_url = ""
bot_token = "mm-bot-token"
channel_id = "channel-123"
timeout_sec = 2

[[notify.mattermost.name-template]]
name = "mm_default"
message = "{{ .Message }}"

[rule.mm]
alert_type = "count_total"

[rule.mm.match]
type = ["event"]
var = ["errors"]
tags = { dc = ["dc1"] }

[rule.mm.key]
from_tags = ["dc"]

[rule.mm.raise]
n = 1

[rule.mm.resolve]
silence_sec = 5

[[rule.mm.notify.route]]
channel = "mattermost"
template = "mm_default"
`
	writeConfigFile(t, path, content)

	_, err := LoadSnapshot(ConfigSource{File: path})
	if err == nil {
		t.Fatalf("expected validation error")
	}
	if !strings.Contains(err.Error(), "notify.mattermost.base_url") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadSnapshotRejectsEnabledMattermostWithoutBotToken(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.toml")
	content := `
[service]
name = "alerting"

[ingest.http]
enabled = true

[notify.mattermost]
enabled = true
base_url = "https://mattermost.example"
bot_token = ""
channel_id = "channel-123"

[[notify.mattermost.name-template]]
name = "mm_default"
message = "{{ .Message }}"

[rule.mm]
alert_type = "count_total"

[rule.mm.match]
type = ["event"]
var = ["errors"]
tags = { dc = ["dc1"] }

[rule.mm.key]
from_tags = ["dc"]

[rule.mm.raise]
n = 1

[rule.mm.resolve]
silence_sec = 5

[[rule.mm.notify.route]]
channel = "mattermost"
template = "mm_default"
`
	writeConfigFile(t, path, content)

	_, err := LoadSnapshot(ConfigSource{File: path})
	if err == nil {
		t.Fatalf("expected validation error")
	}
	if !strings.Contains(err.Error(), "notify.mattermost.bot_token") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadSnapshotRejectsEnabledMattermostWithoutChannelID(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.toml")
	content := `
[service]
name = "alerting"

[ingest.http]
enabled = true

[notify.mattermost]
enabled = true
base_url = "https://mattermost.example"
bot_token = "mm-bot-token"
channel_id = ""

[[notify.mattermost.name-template]]
name = "mm_default"
message = "{{ .Message }}"

[rule.mm]
alert_type = "count_total"

[rule.mm.match]
type = ["event"]
var = ["errors"]
tags = { dc = ["dc1"] }

[rule.mm.key]
from_tags = ["dc"]

[rule.mm.raise]
n = 1

[rule.mm.resolve]
silence_sec = 5

[[rule.mm.notify.route]]
channel = "mattermost"
template = "mm_default"
`
	writeConfigFile(t, path, content)

	_, err := LoadSnapshot(ConfigSource{File: path})
	if err == nil {
		t.Fatalf("expected validation error")
	}
	if !strings.Contains(err.Error(), "notify.mattermost.channel_id") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadSnapshotRejectsInvalidNotifyTemplate(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.toml")
	content := `
[service]
name = "alerting"

[ingest.http]
enabled = true

[notify.telegram]
enabled = true
bot_token = "token"
chat_id = "chat"

[[notify.telegram.name-template]]
name = "tg_default"
message = "{{ .AlertID "

[rule.ct]
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
template = "tg_default"
`
	writeConfigFile(t, path, content)

	_, err := LoadSnapshot(ConfigSource{File: path})
	if err == nil {
		t.Fatalf("expected validation error")
	}
	if !strings.Contains(err.Error(), "notify.telegram.name-template[0].message") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadSnapshotAllowsFmtDurationTemplateFunc(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.toml")
	content := `
[service]
name = "alerting"

[ingest.http]
enabled = true

[notify.telegram]
enabled = true
bot_token = "token"
chat_id = "chat"

[[notify.telegram.name-template]]
name = "tg_default"
message = "delta={{ fmtDuration .Duration }}"

[rule.ct]
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
template = "tg_default"
`
	writeConfigFile(t, path, content)

	if _, err := LoadSnapshot(ConfigSource{File: path}); err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
}

func TestLoadSnapshotAppliesNATSIngestDefaults(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.toml")
	content := `
[service]
name = "alerting"

[ingest.http]
enabled = false

[ingest.nats]
enabled = true

[notify.telegram]
enabled = true
bot_token = "token"
chat_id = "chat"

[[notify.telegram.name-template]]
name = "tg_default"
message = "{{ .Message }}"

[rule.ct]
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
template = "tg_default"
`
	writeConfigFile(t, path, content)

	cfg, err := LoadSnapshot(ConfigSource{File: path})
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if cfg.Ingest.NATS.Stream == "" || cfg.Ingest.NATS.ConsumerName == "" || cfg.Ingest.NATS.DeliverGroup == "" {
		t.Fatalf("nats ingest defaults were not applied: %+v", cfg.Ingest.NATS)
	}
	if len(cfg.Ingest.NATS.URL) != 1 || cfg.Ingest.NATS.URL[0] != "nats://127.0.0.1:4222" {
		t.Fatalf("unexpected nats ingest urls: %#v", cfg.Ingest.NATS.URL)
	}
	if cfg.Ingest.NATS.MaxDeliver == 0 || cfg.Ingest.NATS.AckWaitSec <= 0 || cfg.Ingest.NATS.MaxAckPending <= 0 {
		t.Fatalf("unexpected nats ingest defaults: %+v", cfg.Ingest.NATS)
	}
}

func TestLoadSnapshotRejectsInvalidNATSIngestMaxDeliver(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.toml")
	content := `
[service]
name = "alerting"

[ingest.http]
enabled = false

[ingest.nats]
enabled = true
max_deliver = -2

[notify.telegram]
enabled = true
bot_token = "token"
chat_id = "chat"

[[notify.telegram.name-template]]
name = "tg_default"
message = "{{ .Message }}"

[rule.ct]
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
template = "tg_default"
`
	writeConfigFile(t, path, content)

	_, err := LoadSnapshot(ConfigSource{File: path})
	if err == nil {
		t.Fatalf("expected validation error")
	}
	if !strings.Contains(err.Error(), "ingest.nats.max_deliver") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadSnapshotAcceptsEnabledNotifyQueue(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.toml")
	content := `
[service]
name = "alerting"

[ingest.http]
enabled = true

[notify.queue]
enabled = true
url = "nats://127.0.0.1:4222"
subject = "alerting.notify.jobs.test"
stream = "ALERTING_NOTIFY_TEST"
consumer_name = "alerting-notify-test"
deliver_group = "alerting-notify-test"
ack_wait_sec = 10
nack_delay_ms = 100
max_deliver = 3
max_ack_pending = 100

[notify.telegram]
enabled = true
bot_token = "token"
chat_id = "chat"

[[notify.telegram.name-template]]
name = "tg_default"
message = "{{ .Message }}"

[rule.ct]
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
template = "tg_default"
`
	writeConfigFile(t, path, content)

	cfg, err := LoadSnapshot(ConfigSource{File: path})
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if !cfg.Notify.Queue.Enabled {
		t.Fatalf("expected queue enabled")
	}
	if cfg.Notify.Queue.Stream != "ALERTING_NOTIFY_TEST" {
		t.Fatalf("unexpected queue stream %q", cfg.Notify.Queue.Stream)
	}
}

func TestLoadSnapshotAppliesNotifyQueueDefaultsWhenEnabled(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.toml")
	content := `
[service]
name = "alerting"

[ingest.http]
enabled = true

[notify.queue]
enabled = true
url = "nats://127.0.0.1:4222"
stream = "ALERTING_NOTIFY_TEST"
consumer_name = "alerting-notify-test"
deliver_group = "alerting-notify-test"

[notify.telegram]
enabled = true
bot_token = "token"
chat_id = "chat"

[[notify.telegram.name-template]]
name = "tg_default"
message = "{{ .Message }}"

[rule.ct]
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
template = "tg_default"
`
	writeConfigFile(t, path, content)

	cfg, err := LoadSnapshot(ConfigSource{File: path})
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if !cfg.Notify.Queue.Enabled {
		t.Fatalf("expected queue enabled")
	}
	if cfg.Notify.Queue.Subject != "alerting.notify.jobs" {
		t.Fatalf("unexpected queue subject %q", cfg.Notify.Queue.Subject)
	}
	if cfg.Notify.Queue.AckWaitSec <= 0 {
		t.Fatalf("expected positive queue ack wait, got %d", cfg.Notify.Queue.AckWaitSec)
	}
	if cfg.Notify.Queue.MaxAckPending <= 0 {
		t.Fatalf("expected positive queue max ack pending, got %d", cfg.Notify.Queue.MaxAckPending)
	}
}

func TestLoadSnapshotAcceptsEnabledJiraTracker(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.toml")
	content := `
[service]
name = "alerting"

[ingest.http]
enabled = true

[notify.jira]
enabled = true
base_url = "https://jira.example.com"
timeout_sec = 10

[notify.jira.auth]
type = "basic"
username = "user@example.com"
password = "token"

[notify.jira.create]
method = "POST"
path = "/rest/api/3/issue"
body_template = "{\"summary\": {{ json .Message }}}"
success_status = [201]
ref_json_path = "key"

[notify.jira.resolve]
method = "POST"
path = "/rest/api/3/issue/{{ .ExternalRef }}/transitions"
body_template = "{\"transition\":{\"id\":\"31\"}}"
success_status = [204]

[[notify.jira.name-template]]
name = "jira_default"
message = "[{{ .RuleName }}] {{ .Message }}"

[rule.ct]
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
channel = "jira"
template = "jira_default"
`
	writeConfigFile(t, path, content)

	if _, err := LoadSnapshot(ConfigSource{File: path}); err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
}

func TestLoadSnapshotRejectsJiraWithoutCreateRefPath(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.toml")
	content := `
[service]
name = "alerting"

[ingest.http]
enabled = true

[notify.jira]
enabled = true
base_url = "https://jira.example.com"
timeout_sec = 10

[notify.jira.create]
method = "POST"
path = "/rest/api/3/issue"
success_status = [201]

[notify.jira.resolve]
method = "POST"
path = "/rest/api/3/issue/{{ .ExternalRef }}/transitions"
success_status = [204]

[[notify.jira.name-template]]
name = "jira_default"
message = "{{ .Message }}"

[rule.ct]
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
channel = "jira"
template = "jira_default"
`
	writeConfigFile(t, path, content)

	_, err := LoadSnapshot(ConfigSource{File: path})
	if err == nil {
		t.Fatalf("expected validation error")
	}
	if !strings.Contains(err.Error(), "notify.jira.create.ref_json_path") {
		t.Fatalf("unexpected error: %v", err)
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
	writeConfigFile(t, filepath.Join(tmpDir, "a.toml"), `
[notify]
repeat = true
repeat_per_channel = true
on_pending = true

[notify.telegram]
enabled = true
bot_token = "token-a"
chat_id = "chat-a"

[notify.mattermost]
enabled = true
base_url = "https://mattermost.example"
bot_token = "token-a"
channel_id = "channel-a"
`)
	writeConfigFile(t, filepath.Join(tmpDir, "b.toml"), `
[notify]
repeat = false
repeat_per_channel = false
on_pending = false

[notify.telegram]
enabled = false

[notify.mattermost]
enabled = false
`)

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

func TestLoadSnapshotRejectsLegacyRuleArraySyntax(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.toml")
	content := `
[service]
name = "alerting"

[ingest.http]
enabled = true

[notify.telegram]
enabled = true
bot_token = "token"
chat_id = "chat"

[[notify.telegram.name-template]]
name = "tg_default"
message = "{{ .Message }}"

[[rule]]
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
template = "tg_default"
`
	writeConfigFile(t, path, content)

	_, err := LoadSnapshot(ConfigSource{File: path})
	if err == nil {
		t.Fatalf("expected legacy rule format error")
	}
	if !strings.Contains(err.Error(), "legacy [[rule]] format is not supported") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadSnapshotRejectsStateSectionSyntax(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.toml")
	content := `
[service]
name = "alerting"

[ingest.http]
enabled = true

[ingest.nats]
enabled = false

[state.nats]
url = ["nats://127.0.0.1:4222"]
tick_bucket = "tick_custom"

[notify.telegram]
enabled = true
bot_token = "token"
chat_id = "chat"

[[notify.telegram.name-template]]
name = "tg_default"
message = "{{ .Message }}"

[rule.ct]
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
template = "tg_default"
`
	writeConfigFile(t, path, content)

	_, err := LoadSnapshot(ConfigSource{File: path})
	if err == nil {
		t.Fatalf("expected unsupported state section error")
	}
	if !strings.Contains(err.Error(), "state configuration is not supported") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadSnapshotRejectsRuleBodyNameField(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.toml")
	content := `
[service]
name = "alerting"

[ingest.http]
enabled = true

[notify.telegram]
enabled = true
bot_token = "token"
chat_id = "chat"

[[notify.telegram.name-template]]
name = "tg_default"
message = "{{ .Message }}"

[rule.ct]
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
template = "tg_default"
`
	writeConfigFile(t, path, content)

	_, err := LoadSnapshot(ConfigSource{File: path})
	if err == nil {
		t.Fatalf("expected name field error")
	}
	if !strings.Contains(err.Error(), "rule.ct.name is not supported") {
		t.Fatalf("unexpected error: %v", err)
	}
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
