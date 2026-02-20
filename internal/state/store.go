package state

import (
	"context"
	"errors"
	"time"

	"alerting/internal/domain"
)

var (
	// ErrNotFound indicates absent key/card.
	ErrNotFound = errors.New("not found")
	// ErrConflict indicates revision mismatch for CAS update.
	ErrConflict = errors.New("revision conflict")
)

// Store provides alert state persistence operations.
// Params: CRUD operations for tick/card objects and rule-level listing.
// Returns: backend persistence behavior.
type Store interface {
	RefreshTick(ctx context.Context, alertID string, lastSeen time.Time, ttl time.Duration) error
	HasTick(ctx context.Context, alertID string) (bool, error)
	GetCard(ctx context.Context, alertID string) (domain.AlertCard, uint64, error)
	PutCard(ctx context.Context, alertID string, card domain.AlertCard) (uint64, error)
	UpdateCard(ctx context.Context, alertID string, expectedRevision uint64, card domain.AlertCard) (uint64, error)
	DeleteCard(ctx context.Context, alertID string) error
	ListAlertIDsByRule(ctx context.Context, ruleName string) ([]string, error)
	Close() error
}
