package notifyqueue

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"time"

	"alerting/internal/domain"
)

// Job is one outbound notification task in async delivery queue.
// Params: per-channel route metadata and rendered notification payload.
// Returns: queue unit consumed by delivery workers.
type Job struct {
	ID           string              `json:"id"`
	Channel      string              `json:"channel"`
	Template     string              `json:"template"`
	Notification domain.Notification `json:"notification"`
	CreatedAt    time.Time           `json:"created_at"`
}

// BuildJobID creates deterministic id for one notification queue task.
// Params: channel route metadata and notification payload.
// Returns: stable SHA1-based id string.
func BuildJobID(channel, templateName string, notification domain.Notification) string {
	raw := fmt.Sprintf(
		"%s|%s|%s|%s|%s|%d|%s",
		channel,
		templateName,
		notification.AlertID,
		notification.State,
		notification.Message,
		notification.Timestamp.UnixNano(),
		notification.ExternalRef,
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

// Worker consumes queued jobs and acknowledges delivery status.
// Params: close hook for shutdown lifecycle.
// Returns: queue worker lifecycle.
type Worker interface {
	Close() error
}
