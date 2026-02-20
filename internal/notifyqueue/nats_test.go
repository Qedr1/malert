package notifyqueue

import (
	"context"
	"sync"
	"testing"
	"time"

	"alerting/internal/config"
	"alerting/internal/domain"
	"alerting/test/testutil"
)

func TestBuildJobIDDeterministic(t *testing.T) {
	t.Parallel()

	now := time.Unix(1700000000, 0).UTC()
	jobA := domain.Notification{AlertID: "rule/r/v/h", State: domain.AlertStateFiring, Message: "firing", Timestamp: now}
	jobB := domain.Notification{AlertID: "rule/r/v/h", State: domain.AlertStateFiring, Message: "firing", Timestamp: now}

	idA := BuildJobID("telegram", "tg_default", jobA)
	idB := BuildJobID("telegram", "tg_default", jobB)
	if idA == "" {
		t.Fatalf("expected non-empty job id")
	}
	if idA != idB {
		t.Fatalf("expected deterministic ids: %q != %q", idA, idB)
	}
}

func TestNATSProducerWorkerRedelivery(t *testing.T) {
	natsURL, stopNATS := testutil.StartLocalNATSServer(t)
	defer stopNATS()

	cfg := config.NotifyQueue{
		Enabled:       true,
		URL:           natsURL,
		Subject:       "alerting.notify.jobs.test",
		Stream:        "ALERTING_NOTIFY_TEST",
		ConsumerName:  "alerting-notify-test",
		DeliverGroup:  "alerting-notify-test",
		AckWaitSec:    2,
		NackDelayMS:   10,
		MaxDeliver:    3,
		MaxAckPending: 128,
	}

	producer, err := NewNATSProducer(cfg)
	if err != nil {
		t.Fatalf("new producer: %v", err)
	}
	defer func() { _ = producer.Close() }()

	var (
		mu       sync.Mutex
		attempts = map[string]int{}
		doneCh   = make(chan struct{}, 1)
	)
	worker, err := NewNATSWorker(cfg, nil, func(_ context.Context, job Job) error {
		mu.Lock()
		attempts[job.ID]++
		current := attempts[job.ID]
		mu.Unlock()
		if current == 1 {
			return context.DeadlineExceeded
		}
		select {
		case doneCh <- struct{}{}:
		default:
		}
		return nil
	})
	if err != nil {
		t.Fatalf("new worker: %v", err)
	}
	defer func() { _ = worker.Close() }()

	notification := domain.Notification{
		AlertID:   "rule/r/v/h",
		RuleName:  "r",
		State:     domain.AlertStateFiring,
		Message:   "firing",
		Timestamp: time.Now().UTC(),
	}
	job := Job{
		ID:           BuildJobID("telegram", "tg_default", notification),
		Channel:      "telegram",
		Template:     "tg_default",
		Notification: notification,
		CreatedAt:    time.Now().UTC(),
	}
	if err := producer.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for redelivery success")
	}

	mu.Lock()
	gotAttempts := attempts[job.ID]
	mu.Unlock()
	if gotAttempts < 2 {
		t.Fatalf("expected at least 2 attempts due redelivery, got %d", gotAttempts)
	}
}
