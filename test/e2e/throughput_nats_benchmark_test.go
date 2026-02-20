package e2e

import (
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"alerting/internal/app"
	"alerting/internal/clock"
	"alerting/internal/config"
	"alerting/internal/domain"
	"alerting/internal/ingest"
	"alerting/internal/notify"
	"alerting/internal/state"

	"github.com/nats-io/nats.go"
)

type countingSink struct {
	manager *app.Manager
	count   atomic.Int64
}

func (sink *countingSink) Push(event domain.Event) error {
	if err := sink.manager.Push(event); err != nil {
		return err
	}
	sink.count.Add(1)
	return nil
}

// BenchmarkNATSQueueIngestThroughput measures queue path throughput:
// producer -> NATS stream -> JetStream queue consumer -> manager pipeline.
func BenchmarkNATSQueueIngestThroughput(b *testing.B) {
	natsURL, stopNATS := startLocalNATSServer(b)
	defer stopNATS()

	ensureEventStream(b, natsURL, e2eEventsStream, e2eEventsSubj)
	const (
		tickBucket = "tick_bench_queue"
		dataBucket = "data_bench_queue"
	)
	ensureStateBuckets(b, natsURL, tickBucket, dataBucket)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Config{
		Ingest: config.IngestConfig{
			NATS: config.NATSIngestConfig{
				Enabled:       true,
				URL:           []string{natsURL},
				Subject:       e2eEventsSubj,
				Stream:        e2eEventsStream,
				ConsumerName:  "bench-ingest",
				DeliverGroup:  "bench-workers",
				AckWaitSec:    10,
				NackDelayMS:   5,
				MaxDeliver:    -1,
				MaxAckPending: 8192,
			},
		},
		Notify: config.NotifyConfig{
			Repeat:           false,
			RepeatEverySec:   300,
			RepeatOn:         []string{"firing"},
			RepeatPerChannel: true,
		},
		Rule: []config.RuleConfig{
			{
				Name:      "bench_ct_nats",
				AlertType: "count_total",
				Match: config.RuleMatch{
					Type: []string{"event"},
					Var:  []string{"errors"},
					Tags: map[string]config.StringList{
						"dc":      {"dc1"},
						"service": {"api"},
					},
				},
				Key: config.RuleKey{
					FromTags: []string{"dc", "service", "host"},
				},
				Raise: config.RuleRaise{
					N: 1_000_000_000,
				},
				Resolve: config.RuleResolve{
					SilenceSec: 3600,
				},
			},
		},
	}

	store, err := state.NewNATSStore(config.NATSStateConfig{
		URL:                []string{natsURL},
		TickBucket:         tickBucket,
		DataBucket:         dataBucket,
		AllowCreateBuckets: true,
	})
	if err != nil {
		b.Fatalf("new nats store: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	dispatcher := notify.NewDispatcher(config.NotifyConfig{}, logger)
	manager := app.NewManager(cfg, logger, store, dispatcher, clock.RealClock{})
	sink := &countingSink{manager: manager}

	subscriber, err := ingest.NewNATSSubscriber(cfg.Ingest.NATS, sink, logger)
	if err != nil {
		b.Fatalf("new nats subscriber: %v", err)
	}
	defer func() {
		_ = subscriber.Close()
	}()

	nc, err := nats.Connect(natsURL)
	if err != nil {
		b.Fatalf("connect nats publisher: %v", err)
	}
	defer nc.Close()

	eventJSON := []byte(`{"dt":1739876543210,"type":"event","tags":{"dc":"dc1","service":"api","host":"bench-host"},"var":"errors","value":{"t":"n","n":1},"agg_cnt":1,"win":0}`)

	b.ReportAllocs()
	b.ResetTimer()
	startedAt := time.Now()
	for i := 0; i < b.N; i++ {
		if err := nc.Publish(e2eEventsSubj, eventJSON); err != nil {
			b.Fatalf("publish event %d: %v", i, err)
		}
	}
	if err := nc.FlushTimeout(10 * time.Second); err != nil {
		b.Fatalf("publisher flush: %v", err)
	}

	deadline := time.Now().Add(45 * time.Second)
	lastCount := sink.count.Load()
	lastProgressAt := time.Now()
	for {
		current := sink.count.Load()
		if current >= int64(b.N) {
			break
		}
		if current > lastCount {
			lastCount = current
			lastProgressAt = time.Now()
		}
		if time.Now().After(deadline) || time.Since(lastProgressAt) > 2*time.Second {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	processed := sink.count.Load()
	if processed == 0 {
		b.Fatalf("queue path did not process any events")
	}
	elapsed := time.Since(startedAt)
	eventsPerSecond := float64(processed) / elapsed.Seconds()
	backlog := int64(b.N) - processed
	if backlog < 0 {
		backlog = 0
	}
	b.ReportMetric(eventsPerSecond, "events/sec")
	b.ReportMetric(float64(processed), "processed_events")
	b.ReportMetric(float64(backlog), "backlog_events")
}
