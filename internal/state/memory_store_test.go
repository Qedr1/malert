package state

import (
	"context"
	"testing"
	"time"

	"alerting/internal/domain"
)

func TestMemoryStoreCardLifecycle(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore(time.Now)
	card := domain.AlertCard{AlertID: "rule/r/var/hash", RuleName: "r"}

	rev, err := store.PutCard(context.Background(), card.AlertID, card)
	if err != nil {
		t.Fatalf("put card: %v", err)
	}
	if rev == 0 {
		t.Fatalf("expected revision >0")
	}

	loaded, loadedRev, err := store.GetCard(context.Background(), card.AlertID)
	if err != nil {
		t.Fatalf("get card: %v", err)
	}
	if loaded.AlertID != card.AlertID || loadedRev != rev {
		t.Fatalf("unexpected card load: %+v rev=%d", loaded, loadedRev)
	}

	loaded.RuleName = "r2"
	rev2, err := store.UpdateCard(context.Background(), card.AlertID, rev, loaded)
	if err != nil {
		t.Fatalf("update card: %v", err)
	}
	if rev2 == rev {
		t.Fatalf("expected revision to change")
	}

	if _, err := store.UpdateCard(context.Background(), card.AlertID, rev, loaded); err != ErrConflict {
		t.Fatalf("expected conflict, got %v", err)
	}

	if err := store.DeleteCard(context.Background(), card.AlertID); err != nil {
		t.Fatalf("delete card: %v", err)
	}
	if _, _, err := store.GetCard(context.Background(), card.AlertID); err != ErrNotFound {
		t.Fatalf("expected not found after delete, got %v", err)
	}
}

func TestMemoryStoreTickExpiry(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 24, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })

	if err := store.RefreshTick(context.Background(), "rule/r/var/hash", now, 2*time.Second); err != nil {
		t.Fatalf("refresh tick: %v", err)
	}
	exists, err := store.HasTick(context.Background(), "rule/r/var/hash")
	if err != nil {
		t.Fatalf("has tick: %v", err)
	}
	if !exists {
		t.Fatalf("expected tick to exist")
	}

	now = now.Add(3 * time.Second)
	exists, err = store.HasTick(context.Background(), "rule/r/var/hash")
	if err != nil {
		t.Fatalf("has tick after expiry: %v", err)
	}
	if exists {
		t.Fatalf("expected tick to expire")
	}
}

func TestMemoryStoreListAlertIDsByRule(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore(time.Now)
	_, _ = store.PutCard(context.Background(), "rule/r/var/hash", domain.AlertCard{AlertID: "rule/r/var/hash"})
	_, _ = store.PutCard(context.Background(), "rule/other/var/hash", domain.AlertCard{AlertID: "rule/other/var/hash"})

	ids, err := store.ListAlertIDsByRule(context.Background(), "r")
	if err != nil {
		t.Fatalf("list ids: %v", err)
	}
	if len(ids) != 1 || ids[0] != "rule/r/var/hash" {
		t.Fatalf("unexpected ids: %#v", ids)
	}
}
