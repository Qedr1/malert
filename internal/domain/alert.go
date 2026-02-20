package domain

import "time"

// AlertState is runtime alert lifecycle state.
// Params: pending/firing/resolved state constants.
// Returns: state transitions for notifications and storage.
type AlertState string

const (
	// AlertStatePending indicates condition matched but delay window is active.
	AlertStatePending AlertState = "pending"
	// AlertStateFiring indicates active alert.
	AlertStateFiring AlertState = "firing"
	// AlertStateResolved indicates alert was closed.
	AlertStateResolved AlertState = "resolved"
)

// AlertCard stores persisted alert metadata.
// Params: identity, state timestamps, rule and tags context.
// Returns: card for state backend and outbound payload.
type AlertCard struct {
	AlertID      string            `json:"alert_id"`
	RuleName     string            `json:"rule_name"`
	Var          string            `json:"var"`
	Tags         map[string]string `json:"tags"`
	MetricValue  string            `json:"metric_value,omitempty"`
	ExternalRefs map[string]string `json:"external_refs,omitempty"`
	State        AlertState        `json:"state"`
	RaisedAt     time.Time         `json:"raised_at"`
	PendingSince *time.Time        `json:"pending_since,omitempty"`
	LastSeenAt   time.Time         `json:"last_seen_at"`
	ClosedAt     *time.Time        `json:"closed_at,omitempty"`
	ResolveCause string            `json:"resolve_cause,omitempty"`
}

// Notification contains outbound channel payload.
// Params: mandatory alert ID and state transition metadata.
// Returns: one notification request for notifier layer.
type Notification struct {
	Channel          string            `json:"channel"`
	RouteKey         string            `json:"route_key,omitempty"`
	RouteMode        string            `json:"route_mode,omitempty"`
	AlertID          string            `json:"alert_id"`
	ShortID          string            `json:"short_id,omitempty"`
	RuleName         string            `json:"rule_name"`
	State            AlertState        `json:"state"`
	Message          string            `json:"message"`
	ReplyToMessageID *int              `json:"reply_to_message_id,omitempty"`
	ExternalRef      string            `json:"external_ref,omitempty"`
	Tags             map[string]string `json:"tags"`
	Var              string            `json:"var"`
	MetricValue      string            `json:"metric_value,omitempty"`
	StartedAt        *time.Time        `json:"started_at,omitempty"`
	Duration         time.Duration     `json:"duration,omitempty"`
	Timestamp        time.Time         `json:"timestamp"`
}

// Decision is one rule evaluation result for event processing.
// Params: evaluated state and flags for storage/notification side effects.
// Returns: deterministic rule processing output.
type Decision struct {
	Matched          bool
	AlertID          string
	State            AlertState
	StateChanged     bool
	ShouldStore      bool
	ShouldNotify     bool
	ResolveImmediate bool
	ResolveCause     string
}
