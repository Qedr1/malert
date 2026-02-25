package e2e

import (
	"fmt"
	"testing"
)

func TestJiraTrackerCreateAndResolveE2E(t *testing.T) {
	runTrackerCreateAndResolveE2E(t, trackerE2EScenario{
		RouteChannel:  "jira",
		RouteTemplate: "jira_default",
		CreatePath:    "/rest/api/3/issue",
		ResolvePath:   "/rest/api/3/issue/OPS-777/transitions",
		CreateStatus:  201,
		CreateBody:    `{"key":"OPS-777"}`,
		ResolveStatus: 204,
		RulePrefix:    "jira_tracker_",
		NotifyConfig: func(baseURL string) string {
			return fmt.Sprintf(`
[notify.telegram]
	enabled = false

[notify.jira]
	enabled = true
	base_url = "%s"
	timeout_sec = 2

[notify.jira.auth]
	type = "none"

[notify.jira.retry]
	enabled = false

[notify.jira.create]
	method = "POST"
	path = "/rest/api/3/issue"
	headers = { Content-Type = "application/json" }
	body_template = "{\"summary\": {{ json .Message }}, \"alert_id\": {{ json .AlertID }}}"
	success_status = [201]
	ref_json_path = "key"

[notify.jira.resolve]
	method = "POST"
	path = "/rest/api/3/issue/{{ .ExternalRef }}/transitions"
	headers = { Content-Type = "application/json" }
	body_template = "{\"transition\":{\"id\":\"31\"}}"
	success_status = [204]

[[notify.jira.name-template]]
	name = "jira_default"
	message = "{{ .RuleName }} {{ .State }} {{ .Var }}={{ .MetricValue }} id={{ .ShortID }}"
`, baseURL)
		},
	})
}
