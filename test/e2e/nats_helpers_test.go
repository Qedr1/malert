package e2e

import (
	"errors"
	"strings"
	"testing"
	"time"

	"alerting/internal/config"
	"alerting/internal/state"
	"alerting/test/testutil"

	"github.com/nats-io/nats.go"
)

const (
	e2eEventsStream = "ALERTING_EVENTS"
	e2eEventsSubj   = "alerting.events"
	e2eTickBucket   = "tick_e2e"
	e2eDataBucket   = "data_e2e"
)

// startLocalNATSServer starts a local JetStream NATS process for e2e tests.
// Params: testing handle for lifecycle/error reporting.
// Returns: server URL and stop callback.
func startLocalNATSServer(tb testing.TB) (string, func()) {
	return testutil.StartLocalNATSServer(tb)
}

// ensureEventStream creates JetStream stream used by ingest queue if missing.
// Params: test handle, server URL, stream name, and subject.
// Returns: stream exists with required subject.
func ensureEventStream(tb testing.TB, url, streamName, subject string) {
	tb.Helper()

	nc, err := nats.Connect(url)
	if err != nil {
		tb.Fatalf("connect nats: %v", err)
	}
	defer nc.Close()

	js, err := nc.JetStream()
	if err != nil {
		tb.Fatalf("jetstream init: %v", err)
	}

	if _, err := js.StreamInfo(streamName); err == nil {
		return
	} else if !errors.Is(err, nats.ErrStreamNotFound) && !strings.Contains(strings.ToLower(err.Error()), "stream not found") {
		tb.Fatalf("stream info failed: %v", err)
	}

	_, err = js.AddStream(&nats.StreamConfig{
		Name:      streamName,
		Subjects:  []string{subject},
		Retention: nats.WorkQueuePolicy,
		Storage:   nats.FileStorage,
		MaxAge:    24 * time.Hour,
	})
	if err != nil {
		tb.Fatalf("add stream %q failed: %v", streamName, err)
	}
}

// ensureStateBuckets creates/normalizes NATS KV buckets used by alert state.
// Params: test handle, server URL, tick bucket, and data bucket names.
// Returns: KV buckets are ready for store/delete-marker flow.
func ensureStateBuckets(tb testing.TB, url, tickBucket, dataBucket string) {
	tb.Helper()

	store, err := state.NewNATSStore(config.NATSStateConfig{
		URL:                []string{url},
		TickBucket:         tickBucket,
		DataBucket:         dataBucket,
		AllowCreateBuckets: true,
	})
	if err != nil {
		tb.Fatalf("prepare state buckets: %v", err)
	}
	_ = store.Close()
}

// cleanupBenchmarkNATSArtifacts removes benchmark stream and KV buckets.
// Params: test handle, server URL, stream name, and KV buckets.
// Returns: benchmark leaves no JetStream artifacts on successful teardown.
func cleanupBenchmarkNATSArtifacts(tb testing.TB, url, streamName string, buckets ...string) {
	tb.Helper()

	nc, err := nats.Connect(url)
	if err != nil {
		tb.Fatalf("connect nats for cleanup: %v", err)
	}
	defer nc.Close()

	js, err := nc.JetStream()
	if err != nil {
		tb.Fatalf("jetstream init for cleanup: %v", err)
	}

	for _, bucket := range buckets {
		bucket = strings.TrimSpace(bucket)
		if bucket == "" {
			continue
		}
		if err := js.DeleteKeyValue(bucket); err != nil {
			errText := strings.ToLower(err.Error())
			if errors.Is(err, nats.ErrBucketNotFound) || strings.Contains(errText, "bucket not found") || strings.Contains(errText, "stream not found") {
				continue
			}
			tb.Fatalf("delete kv bucket %q: %v", bucket, err)
		}
	}

	streamName = strings.TrimSpace(streamName)
	if streamName == "" {
		return
	}
	if err := js.DeleteStream(streamName); err != nil {
		if errors.Is(err, nats.ErrStreamNotFound) || strings.Contains(strings.ToLower(err.Error()), "stream not found") {
			return
		}
		tb.Fatalf("delete stream %q: %v", streamName, err)
	}
}
