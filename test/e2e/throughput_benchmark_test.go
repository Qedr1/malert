package e2e

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"alerting/internal/app"
	"alerting/internal/clock"
	"alerting/internal/config"
	"alerting/internal/domain"
	"alerting/internal/notify"
	"alerting/internal/state"
)

// BenchmarkIngestThroughput measures end-to-end ingest path through manager logic.
// Target reference for MVP acceptance is 10k events/sec.
func BenchmarkIngestThroughput(b *testing.B) {
	natsURL, stopNATS := startLocalNATSServer(b)
	defer stopNATS()
	const (
		tickBucket = "tick_bench_ingest"
		dataBucket = "data_bench_ingest"
	)
	ensureStateBuckets(b, natsURL, tickBucket, dataBucket)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Config{
		Notify: config.NotifyConfig{
			Repeat:           false,
			RepeatEverySec:   300,
			RepeatOn:         []string{"firing"},
			RepeatPerChannel: true,
			OnPending:        false,
		},
		Rule: []config.RuleConfig{
			{
				Name:      "bench_ct",
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
					N: 1,
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

	event := domain.Event{
		DT:   time.Now().UnixMilli(),
		Type: "event",
		Tags: map[string]string{
			"dc":      "dc1",
			"service": "api",
			"host":    "bench-host",
		},
		Var: "errors",
		Value: domain.TypedValue{
			Type: "n",
			N:    ptrFloat(1),
		},
		AggCnt: 1,
		Win:    0,
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := manager.Push(event); err != nil {
			b.Fatalf("push failed: %v", err)
		}
	}

	eventsPerSecond := float64(b.N) / b.Elapsed().Seconds()
	b.ReportMetric(eventsPerSecond, "events/sec")
}

func ptrFloat(value float64) *float64 {
	return &value
}
