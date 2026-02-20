package state

import (
	"context"
	"testing"
	"time"

	"alerting/internal/config"
	"alerting/test/testutil"
)

func TestDeleteMarkerConsumerReceivesTTLExpiry(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}

	url, stopNATS := testutil.StartLocalNATSServer(t)
	defer stopNATS()

	settings := config.NATSStateConfig{
		URL:                   []string{url},
		TickBucket:            "tick_marker",
		DataBucket:            "data_marker",
		DeleteConsumerName:    "resolve-consumer",
		DeleteDeliverGroup:    "resolve-group",
		DeleteSubjectWildcard: "$KV.tick_marker.>",
		AllowCreateBuckets:    true,
	}

	store, err := NewNATSStore(settings)
	if err != nil {
		t.Fatalf("new nats store: %v", err)
	}
	defer store.Close()

	alerts := make(chan string, 1)
	consumer, err := NewDeleteMarkerConsumer(settings, func(_ context.Context, alertID, _ string) error {
		alerts <- alertID
		return nil
	})
	if err != nil {
		t.Fatalf("new delete consumer: %v", err)
	}
	defer consumer.Close()

	alertID := "rule/test/errors/hash"
	if err := store.RefreshTick(context.Background(), alertID, time.Now().UTC(), 2*time.Second); err != nil {
		t.Fatalf("refresh tick: %v", err)
	}

	select {
	case gotID := <-alerts:
		if gotID != alertID {
			t.Fatalf("unexpected alertID from delete marker: %q", gotID)
		}
	case <-time.After(12 * time.Second):
		t.Fatalf("delete marker was not received")
	}
}
