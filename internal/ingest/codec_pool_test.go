package ingest

import (
	"testing"

	"alerting/internal/domain"
)

func TestDecodeEventPayloadIntoSingle(t *testing.T) {
	t.Parallel()

	scratch := acquireDecodeScratch()
	defer releaseDecodeScratch(scratch)

	payload := []byte(`{"dt":1739876543210,"type":"event","tags":{"dc":"dc1","service":"api","host":"h1"},"var":"errors","value":{"t":"n","n":1},"agg_cnt":1,"win":0}`)
	events, err := decodeEventPayloadInto(payload, scratch)
	if err != nil {
		t.Fatalf("decode single payload: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected one event, got %d", len(events))
	}
	if events[0].Tags["host"] != "h1" {
		t.Fatalf("unexpected host tag: %q", events[0].Tags["host"])
	}
}

func TestDecodeEventPayloadIntoBatch(t *testing.T) {
	t.Parallel()

	scratch := acquireDecodeScratch()
	defer releaseDecodeScratch(scratch)

	payload := []byte(`[{"dt":1739876543210,"type":"event","tags":{"dc":"dc1","service":"api","host":"h1"},"var":"errors","value":{"t":"n","n":1},"agg_cnt":1,"win":0},{"dt":1739876543211,"type":"event","tags":{"dc":"dc1","service":"api","host":"h2"},"var":"errors","value":{"t":"n","n":2},"agg_cnt":1,"win":0}]`)
	events, err := decodeEventPayloadInto(payload, scratch)
	if err != nil {
		t.Fatalf("decode batch payload: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected two events, got %d", len(events))
	}
	if events[1].Tags["host"] != "h2" {
		t.Fatalf("unexpected second host tag: %q", events[1].Tags["host"])
	}
}

func TestReleaseDecodeScratchDropsOversizedBuffer(t *testing.T) {
	t.Parallel()

	scratch := &decodeScratch{
		events: make([]domain.Event, 0, maxPooledBatchCapacity+1),
	}
	releaseDecodeScratch(scratch)
	if cap(scratch.events) > maxPooledBatchCapacity {
		t.Fatalf("expected capped pooled capacity, got %d", cap(scratch.events))
	}
}
