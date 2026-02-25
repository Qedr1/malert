package e2e

import (
	"fmt"
	"testing"
)

func TestYouTrackTrackerCreateAndResolveE2E(t *testing.T) {
	runTrackerCreateAndResolveE2E(t, trackerE2EScenario{
		RouteChannel:  "youtrack",
		RouteTemplate: "youtrack_default",
		CreatePath:    "/api/issues",
		ResolvePath:   "/api/issues/OPS-888/commands",
		CreateStatus:  201,
		CreateBody:    `{"idReadable":"OPS-888"}`,
		ResolveStatus: 200,
		RulePrefix:    "youtrack_tracker_",
		NotifyConfig: func(baseURL string) string {
			return fmt.Sprintf(`
[notify.telegram]
	enabled = false

[notify.youtrack]
	enabled = true
	base_url = "%s"
	timeout_sec = 2

[notify.youtrack.auth]
	type = "none"

[notify.youtrack.retry]
	enabled = false

[notify.youtrack.create]
	method = "POST"
	path = "/api/issues"
	headers = { Content-Type = "application/json" }
	body_template = "{\"summary\": {{ json .Message }}, \"alert_id\": {{ json .AlertID }}}"
	success_status = [201]
	ref_json_path = "idReadable"

[notify.youtrack.resolve]
	method = "POST"
	path = "/api/issues/{{ .ExternalRef }}/commands"
	headers = { Content-Type = "application/json" }
	body_template = "{\"query\":\"Fixed\"}"
	success_status = [200]

[[notify.youtrack.name-template]]
	name = "youtrack_default"
	message = "{{ .RuleName }} {{ .State }} {{ .Var }}={{ .MetricValue }} id={{ .ShortID }}"
`, baseURL)
		},
	})
}
