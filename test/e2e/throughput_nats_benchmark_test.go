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

func (sink *countingSink) PushBatch(events []domain.Event) error {
	if len(events) == 0 {
		return nil
	}
	if err := sink.manager.PushBatch(events); err != nil {
		return err
	}
	sink.count.Add(int64(len(events)))
	return nil
}

// BenchmarkNATSQueueIngestThroughput measures queue path throughput:
// producer -> NATS stream -> JetStream queue consumer -> manager pipeline.
func BenchmarkNATSQueueIngestThroughput(b *testing.B) {
	batchSize := benchmarkBatchSizeFromEnv("E2E_NATS_BATCH_SIZE", 1)
	workers := benchmarkBatchSizeFromEnv("E2E_NATS_WORKERS", 1)
	flushEveryBatch := batchSize > 1

	for _, metric := range allE2EMetricCases() {
		metric := metric
		b.Run(metric.Name, func(b *testing.B) {
			natsURL, stopNATS := startLocalNATSServer(b)
			defer stopNATS()

			ensureEventStream(b, natsURL, e2eEventsStream, e2eEventsSubj)
			tickBucket := "tick_bench_queue_" + metric.Name
			dataBucket := "data_bench_queue_" + metric.Name
			ensureStateBuckets(b, natsURL, tickBucket, dataBucket)
			defer cleanupBenchmarkNATSArtifacts(b, natsURL, e2eEventsStream, tickBucket, dataBucket)

			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			cfg := config.Config{
				Ingest: config.IngestConfig{
					NATS: config.NATSIngestConfig{
						Enabled:       true,
						URL:           []string{natsURL},
						Subject:       e2eEventsSubj,
						Stream:        e2eEventsStream,
						ConsumerName:  "bench-ingest-" + metric.Name,
						DeliverGroup:  "bench-workers-" + metric.Name,
						Workers:       workers,
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
					benchmarkRuleForMetric(metric, "bench_nats_"+metric.Name),
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
			payload := eventJSON
			if batchSize > 1 {
				payload = buildBatchPayload(batchSize)
			}

			b.ReportAllocs()
			b.ResetTimer()
			startedAt := time.Now()
			for i := 0; i < b.N; i++ {
				if err := nc.Publish(e2eEventsSubj, payload); err != nil {
					b.Fatalf("publish event batch=%d: %v", i, err)
				}
				if flushEveryBatch {
					if err := nc.FlushTimeout(10 * time.Second); err != nil {
						b.Fatalf("publisher flush batch=%d: %v", i, err)
					}
				}
			}
			if !flushEveryBatch {
				if err := nc.FlushTimeout(10 * time.Second); err != nil {
					b.Fatalf("publisher flush: %v", err)
				}
			}

			expected := int64(b.N * batchSize)
			deadline := time.Now().Add(60 * time.Second)
			for sink.count.Load() < expected {
				if time.Now().After(deadline) {
					break
				}
				time.Sleep(2 * time.Millisecond)
			}

			processed := sink.count.Load()
			elapsed := time.Since(startedAt)
			eventsPerSecond := float64(processed) / elapsed.Seconds()
			requestsPerSecond := float64(b.N) / elapsed.Seconds()
			backlog := expected - processed
			if backlog < 0 {
				backlog = 0
			}
			b.ReportMetric(eventsPerSecond, "events/sec")
			b.ReportMetric(requestsPerSecond, "req/sec")
			b.ReportMetric(float64(processed), "processed_events")
			b.ReportMetric(float64(backlog), "backlog_events")
			if backlog > 0 {
				b.Fatalf("queue path drain timeout: processed=%d sent=%d backlog=%d", processed, expected, backlog)
			}
		})
	}
}
