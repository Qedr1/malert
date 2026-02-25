package notifyqueue

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"alerting/internal/config"
	"alerting/internal/domain"
	"alerting/test/testutil"

	"github.com/nats-io/nats.go"
)

func newTestQueueConfig(natsURL string, maxDeliver int) config.NotifyQueue {
	return config.NotifyQueue{
		Enabled:       true,
		URL:           []string{natsURL},
		AckWaitSec:    2,
		NackDelayMS:   10,
		MaxDeliver:    maxDeliver,
		MaxAckPending: 128,
	}
}

func waitForCallsAtLeast(t *testing.T, timeout time.Duration, counter *int32, min int32) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if atomic.LoadInt32(counter) >= min {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected calls >= %d, got %d", min, atomic.LoadInt32(counter))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

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

	cfg := newTestQueueConfig(natsURL, 3)

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

func TestNATSWorkerPublishesPermanentErrorToDLQ(t *testing.T) {
	natsURL, stopNATS := testutil.StartLocalNATSServer(t)
	defer stopNATS()

	cfg := newTestQueueConfig(natsURL, 3)
	cfg.DLQ = true

	producer, err := NewNATSProducer(cfg)
	if err != nil {
		t.Fatalf("new producer: %v", err)
	}
	defer func() { _ = producer.Close() }()

	var calls int32
	worker, err := NewNATSWorker(cfg, nil, func(_ context.Context, _ Job) error {
		atomic.AddInt32(&calls, 1)
		return MarkPermanent(errors.New("template missing"))
	})
	if err != nil {
		t.Fatalf("new worker: %v", err)
	}
	defer func() { _ = worker.Close() }()

	nc, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatalf("connect nats: %v", err)
	}
	defer nc.Close()
	sub, err := nc.SubscribeSync(notifyQueueDLQSubject)
	if err != nil {
		t.Fatalf("subscribe dlq: %v", err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush subscribe: %v", err)
	}

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

	message, err := sub.NextMsg(5 * time.Second)
	if err != nil {
		t.Fatalf("wait dlq message: %v", err)
	}
	var entry DLQEntry
	if err := json.Unmarshal(message.Data, &entry); err != nil {
		t.Fatalf("decode dlq entry: %v", err)
	}
	if entry.Reason != DLQReasonPermanentError {
		t.Fatalf("unexpected dlq reason: %s", entry.Reason)
	}
	if entry.Job.ID != job.ID {
		t.Fatalf("unexpected dlq job id: %s", entry.Job.ID)
	}
	if entry.Attempts != 1 {
		t.Fatalf("unexpected attempts: %d", entry.Attempts)
	}

	waitForCallsAtLeast(t, time.Second, &calls, 1)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected single handler call, got %d", got)
	}
}

func TestNATSWorkerPublishesMaxDeliverToDLQ(t *testing.T) {
	natsURL, stopNATS := testutil.StartLocalNATSServer(t)
	defer stopNATS()

	cfg := newTestQueueConfig(natsURL, 2)
	cfg.AckWaitSec = 1
	cfg.DLQ = true

	producer, err := NewNATSProducer(cfg)
	if err != nil {
		t.Fatalf("new producer: %v", err)
	}
	defer func() { _ = producer.Close() }()

	var calls int32
	worker, err := NewNATSWorker(cfg, nil, func(_ context.Context, _ Job) error {
		atomic.AddInt32(&calls, 1)
		return context.DeadlineExceeded
	})
	if err != nil {
		t.Fatalf("new worker: %v", err)
	}
	defer func() { _ = worker.Close() }()

	nc, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatalf("connect nats: %v", err)
	}
	defer nc.Close()
	sub, err := nc.SubscribeSync(notifyQueueDLQSubject)
	if err != nil {
		t.Fatalf("subscribe dlq: %v", err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush subscribe: %v", err)
	}

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

	message, err := sub.NextMsg(8 * time.Second)
	if err != nil {
		t.Fatalf("wait dlq message: %v", err)
	}
	var entry DLQEntry
	if err := json.Unmarshal(message.Data, &entry); err != nil {
		t.Fatalf("decode dlq entry: %v", err)
	}
	if entry.Reason != DLQReasonMaxDeliverExceeded {
		t.Fatalf("unexpected dlq reason: %s", entry.Reason)
	}
	if entry.Job.ID != job.ID {
		t.Fatalf("unexpected dlq job id: %s", entry.Job.ID)
	}
	if entry.Attempts < 2 {
		t.Fatalf("expected attempts>=2, got %d", entry.Attempts)
	}
	waitForCallsAtLeast(t, time.Second, &calls, 2)
}
