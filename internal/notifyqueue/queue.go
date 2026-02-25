package notifyqueue

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"time"

	"alerting/internal/domain"
	"alerting/internal/permanent"
)

// Job is one outbound notification task in async delivery queue.
// Params: per-channel route metadata and rendered notification payload.
// Returns: queue unit consumed by delivery workers.
type Job struct {
	ID           string              `json:"id"`
	RouteKey     string              `json:"route_key,omitempty"`
	RouteMode    string              `json:"route_mode,omitempty"`
	Channel      string              `json:"channel"`
	Template     string              `json:"template"`
	Notification domain.Notification `json:"notification"`
	CreatedAt    time.Time           `json:"created_at"`
}

// DLQReason identifies reason why notify job was moved to dead-letter queue.
// Params: categorized failure reason.
// Returns: machine-readable DLQ classification.
type DLQReason string

const (
	// DLQReasonPermanentError marks non-retryable processing failures.
	DLQReasonPermanentError DLQReason = "permanent_error"
	// DLQReasonMaxDeliverExceeded marks retries exhausted by queue max deliver policy.
	DLQReasonMaxDeliverExceeded DLQReason = "max_deliver_exceeded"
)

// DLQEntry is dead-letter payload for notify queue failures.
// Params: original job, failure metadata, and delivery counters.
// Returns: persisted DLQ record.
type DLQEntry struct {
	Job           Job       `json:"job"`
	Reason        DLQReason `json:"reason"`
	Error         string    `json:"error"`
	Attempts      uint64    `json:"attempts"`
	MaxDeliver    int       `json:"max_deliver"`
	Subject       string    `json:"subject"`
	FailedAt      time.Time `json:"failed_at"`
	OriginalMsgID string    `json:"original_msg_id,omitempty"`
}

// BuildJobID creates deterministic id for one notification queue task.
// Params: channel route metadata and notification payload.
// Returns: stable SHA1-based id string.
func BuildJobID(channel, templateName string, notification domain.Notification) string {
	raw := fmt.Sprintf(
		"%s|%s|%s|%s|%s|%d|%s|%s|%s",
		channel,
		templateName,
		notification.AlertID,
		notification.State,
		notification.Message,
		notification.Timestamp.UnixNano(),
		notification.ExternalRef,
		notification.RouteKey,
		notification.RouteMode,
	)
	sum := sha1.Sum([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// Producer enqueues notification delivery jobs.
// Params: context and queue job payload.
// Returns: enqueue error.
type Producer interface {
	Enqueue(ctx context.Context, job Job) error
	Close() error
}

// PermanentError marks processing errors that must not be retried.
// Params: wrapped root cause error.
// Returns: error with permanent retry classification.
type PermanentError = permanent.Error

// MarkPermanent wraps error as permanent processing failure.
// Params: source error.
// Returns: wrapped permanent error (or nil when input is nil).
func MarkPermanent(err error) error {
	return permanent.Mark(err)
}

// IsPermanent reports whether error is marked as non-retryable.
// Params: processing error.
// Returns: true when worker must not retry.
func IsPermanent(err error) bool {
	return permanent.Is(err)
}

// Worker consumes queued jobs and acknowledges delivery status.
// Params: close hook for shutdown lifecycle.
// Returns: queue worker lifecycle.
type Worker interface {
	Close() error
}
