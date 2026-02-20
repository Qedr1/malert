package e2e

import "fmt"

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
	repeatEverySec := notify.RepeatEverySec
	if repeatEverySec <= 0 {
		repeatEverySec = 300
	}
	repeatOn := notify.RepeatOn
	if repeatOn == "" {
		repeatOn = "firing"
	}
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
repeat = %t
repeat_every_sec = %d
repeat_on = ["%s"]
repeat_per_channel = %t
on_pending = %t
`, port, natsURL, notify.Repeat, repeatEverySec, repeatOn, notify.RepeatPerChannel, notify.OnPending)
}
