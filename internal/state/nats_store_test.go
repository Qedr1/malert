package state

import (
	"context"
	"testing"
	"time"

	"alerting/internal/config"
	"alerting/internal/domain"
	"alerting/test/testutil"
)

func TestNATSStoreCRUDIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}

	url, stopNATS := testutil.StartLocalNATSServer(t)
	defer stopNATS()

	store, err := NewNATSStore(config.NATSStateConfig{
		URL:                []string{url},
		TickBucket:         "tick_test",
		DataBucket:         "data_test",
		AllowCreateBuckets: true,
	})
	if err != nil {
		t.Fatalf("new nats store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.RefreshTick(ctx, "rule/r/v/hash", time.Now().UTC(), 5*time.Second); err != nil {
		t.Fatalf("refresh tick: %v", err)
	}
	exists, err := store.HasTick(ctx, "rule/r/v/hash")
	if err != nil {
		t.Fatalf("has tick: %v", err)
	}
	if !exists {
		t.Fatalf("expected tick to exist")
	}

	rev, err := store.PutCard(ctx, "rule/r/v/hash", domain.AlertCard{AlertID: "rule/r/v/hash", RuleName: "r", State: domain.AlertStateFiring})
	if err != nil {
		t.Fatalf("put card: %v", err)
	}
	card, gotRev, err := store.GetCard(ctx, "rule/r/v/hash")
	if err != nil {
		t.Fatalf("get card: %v", err)
	}
	if gotRev != rev || card.RuleName != "r" {
		t.Fatalf("unexpected card/revision: card=%+v rev=%d expected=%d", card, gotRev, rev)
	}

	card.State = domain.AlertStateResolved
	if _, err := store.UpdateCard(ctx, "rule/r/v/hash", gotRev, card); err != nil {
		t.Fatalf("update card: %v", err)
	}

	if err := store.DeleteCard(ctx, "rule/r/v/hash"); err != nil {
		t.Fatalf("delete card: %v", err)
	}
}
