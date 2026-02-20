package templatefmt

import (
	"encoding/json"
	"fmt"
	"text/template"
	"time"
)

// FuncMap returns shared notification template helpers.
// Params: none.
// Returns: deterministic helper map used by config validation and runtime rendering.
func FuncMap() template.FuncMap {
	return template.FuncMap{
		"fmtDuration": FormatDuration,
		"json":        MarshalJSON,
	}
}

// ParseNotificationTemplate parses one notification template with shared helpers.
// Params: template name and body.
// Returns: compiled template or parse error.
func ParseNotificationTemplate(name, body string) (*template.Template, error) {
	return template.New(name).Funcs(FuncMap()).Option("missingkey=error").Parse(body)
}

// FormatDuration renders duration in compact human form with one decimal precision.
// Params: template value expected as time.Duration or *time.Duration.
// Returns: formatted duration string.
func FormatDuration(value any) string {
	var duration time.Duration
	switch typed := value.(type) {
	case time.Duration:
		duration = typed
	case *time.Duration:
		if typed == nil {
			return "0.0s"
		}
		duration = *typed
	default:
		return "0.0s"
	}

	if duration < 0 {
		duration = -duration
	}
	seconds := duration.Seconds()
	switch {
	case seconds >= 3600:
		return fmt.Sprintf("%.1fh", seconds/3600)
	case seconds >= 60:
		return fmt.Sprintf("%.1fm", seconds/60)
	default:
		return fmt.Sprintf("%.1fs", seconds)
	}
}

// MarshalJSON renders value into JSON string for template embedding.
// Params: template value of any type.
// Returns: marshaled JSON string or "null" on marshal failure.
func MarshalJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "null"
	}
	return string(encoded)
}
