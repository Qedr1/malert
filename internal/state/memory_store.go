package state

import (
	"context"
	"strings"
	"sync"
	"time"

	"alerting/internal/domain"
)

// MemoryStore keeps alert state in process memory for single-instance mode.
// Params: in-memory maps for ticks/cards and injected clock.
// Returns: store implementation without external dependencies.
type MemoryStore struct {
	mu    sync.RWMutex
	now   func() time.Time
	cards map[string]memoryCard
	ticks map[string]memoryTick
}

type memoryCard struct {
	card     domain.AlertCard
	revision uint64
}

type memoryTick struct {
	expiresAt time.Time
}

// NewMemoryStore creates in-memory state store.
// Params: now function (defaults to time.Now when nil).
// Returns: initialized in-memory store.
func NewMemoryStore(now func() time.Time) *MemoryStore {
	if now == nil {
		now = time.Now
	}
	return &MemoryStore{
		now:   now,
		cards: make(map[string]memoryCard),
		ticks: make(map[string]memoryTick),
	}
}

// RefreshTick updates tick metadata for alert ID.
// Params: alert ID, last-seen time, and TTL duration.
// Returns: nil (in-memory update).
func (s *MemoryStore) RefreshTick(_ context.Context, alertID string, lastSeen time.Time, ttl time.Duration) error {
	var expiresAt time.Time
	if ttl > 0 {
		expiresAt = lastSeen.Add(ttl)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ticks[alertID] = memoryTick{expiresAt: expiresAt}
	return nil
}

// HasTick reports whether tick entry exists and is not expired.
// Params: alert ID key.
// Returns: true when tick is present and unexpired.
func (s *MemoryStore) HasTick(_ context.Context, alertID string) (bool, error) {
	s.mu.RLock()
	tick, ok := s.ticks[alertID]
	if !ok {
		s.mu.RUnlock()
		return false, nil
	}
	expiresAt := tick.expiresAt
	if expiresAt.IsZero() || s.now().Before(expiresAt) {
		s.mu.RUnlock()
		return true, nil
	}
	s.mu.RUnlock()

	s.mu.Lock()
	tick, ok = s.ticks[alertID]
	if !ok {
		s.mu.Unlock()
		return false, nil
	}
	expiresAt = tick.expiresAt
	if expiresAt.IsZero() || s.now().Before(expiresAt) {
		s.mu.Unlock()
		return true, nil
	}
	delete(s.ticks, alertID)
	s.mu.Unlock()
	return false, nil
}

// GetCard returns card payload and revision.
// Params: alert ID key.
// Returns: stored card, revision, or ErrNotFound.
func (s *MemoryStore) GetCard(_ context.Context, alertID string) (domain.AlertCard, uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.cards[alertID]
	if !ok {
		return domain.AlertCard{}, 0, ErrNotFound
	}
	return entry.card, entry.revision, nil
}

// PutCard writes card payload unconditionally.
// Params: alert ID key and card payload.
// Returns: new revision.
func (s *MemoryStore) PutCard(_ context.Context, alertID string, card domain.AlertCard) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rev := s.cards[alertID].revision + 1
	s.cards[alertID] = memoryCard{card: card, revision: rev}
	return rev, nil
}

// UpdateCard updates card payload using expected revision CAS.
// Params: alert ID key, expected revision, and replacement payload.
// Returns: new revision or ErrConflict.
func (s *MemoryStore) UpdateCard(_ context.Context, alertID string, expectedRevision uint64, card domain.AlertCard) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.cards[alertID]
	if !ok {
		return 0, ErrNotFound
	}
	if entry.revision != expectedRevision {
		return 0, ErrConflict
	}
	rev := expectedRevision + 1
	s.cards[alertID] = memoryCard{card: card, revision: rev}
	return rev, nil
}

// DeleteCard deletes card and corresponding tick key.
// Params: alert ID key.
// Returns: nil (in-memory delete).
func (s *MemoryStore) DeleteCard(_ context.Context, alertID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cards, alertID)
	delete(s.ticks, alertID)
	return nil
}

// ListAlertIDsByRule lists alert IDs by rule namespace prefix.
// Params: rule name namespace.
// Returns: matching alert IDs.
func (s *MemoryStore) ListAlertIDsByRule(_ context.Context, ruleName string) ([]string, error) {
	prefix := "rule/" + strings.ToLower(strings.TrimSpace(ruleName)) + "/"
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0)
	for key := range s.cards {
		if strings.HasPrefix(key, prefix) {
			ids = append(ids, key)
		}
	}
	return ids, nil
}

// Close releases memory store resources.
// Params: none.
// Returns: nil.
func (s *MemoryStore) Close() error {
	return nil
}
