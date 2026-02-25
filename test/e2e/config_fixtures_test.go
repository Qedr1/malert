package e2e

import (
	"fmt"
	"strings"
)

// e2eNotifyOptions defines notify defaults embedded into e2e config prefixes.
// Params: repeat flags and intervals.
// Returns: notify prefix options.
type e2eNotifyOptions struct {
	Repeat           bool
	RepeatEverySec   int
	RepeatOn         string
	RepeatPerChannel bool
	OnPending        bool
}

// e2eStandardConfigPrefix builds common service/ingest/notify config used in e2e tests.
// Params: HTTP port and NATS URL for disabled ingest.nats section.
// Returns: TOML prefix string with stable defaults.
func e2eStandardConfigPrefix(port int, natsURL string) string {
	return e2eConfigPrefix(port, natsURL, e2eNotifyOptions{
		Repeat:           false,
		RepeatEverySec:   300,
		RepeatOn:         "firing",
		RepeatPerChannel: true,
		OnPending:        false,
	})
}

// e2eConfigPrefix builds common service/ingest/notify config with custom notify controls.
// Params: HTTP port, NATS URL, and notify options.
// Returns: TOML prefix string.
func e2eConfigPrefix(port int, natsURL string, notify e2eNotifyOptions) string {
	return e2eConfigPrefixWithMode(port, natsURL, notify, "")
}

func e2eConfigPrefixWithMode(port int, natsURL string, notify e2eNotifyOptions, mode string) string {
	repeatEverySec := notify.RepeatEverySec
	if repeatEverySec <= 0 {
		repeatEverySec = 300
	}
	repeatOn := notify.RepeatOn
	if repeatOn == "" {
		repeatOn = "firing"
	}
	modeLine := ""
	normalizedMode := strings.ToLower(strings.TrimSpace(mode))
	if normalizedMode != "" {
		modeLine = fmt.Sprintf("mode = %q\n", normalizedMode)
	}
	natsBlock := ""
	if normalizedMode == "" || normalizedMode == "nats" {
		natsBlock = fmt.Sprintf(`
[ingest.nats]
enabled = false
url = ["%s"]
`, natsURL)
	}
	return fmt.Sprintf(`
[service]
name = "alerting"
%s
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
%s

[notify]
repeat = %t
repeat_every_sec = %d
repeat_on = ["%s"]
repeat_per_channel = %t
on_pending = %t
`, modeLine, port, natsBlock, notify.Repeat, repeatEverySec, repeatOn, notify.RepeatPerChannel, notify.OnPending)
}

func e2eHTTPNotifyConfig(url string) string {
	return fmt.Sprintf(`
[notify.http]
enabled = true
url = "%s"
method = "POST"
timeout_sec = 1

[notify.http.retry]
enabled = true
backoff = "exponential"
initial_ms = 1
max_ms = 2
max_attempts = 0
log_each_attempt = true

[[notify.http.name-template]]
name = "http_default"
message = "{{ .Message }}"
`, url)
}
