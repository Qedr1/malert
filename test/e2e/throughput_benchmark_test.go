package e2e

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"alerting/internal/app"
	"alerting/internal/clock"
	"alerting/internal/config"
	"alerting/internal/ingest"
	"alerting/internal/notify"
	"alerting/internal/state"
)

func benchmarkServiceMode() string {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("E2E_SERVICE_MODE")))
	if mode == "" {
		return "nats"
	}
	return mode
}

// BenchmarkIngestThroughput measures real HTTP ingest path throughput:
// producer -> HTTP /ingest -> manager pipeline.
func BenchmarkIngestThroughput(b *testing.B) {
	for _, metric := range allE2EMetricCases() {
		metric := metric
		b.Run(metric.Name, func(b *testing.B) {
			mode := benchmarkServiceMode()
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
					benchmarkRuleForMetric(metric, "bench_"+metric.Name),
				},
			}

			var store state.Store
			if mode == "single" {
				store = state.NewMemoryStore(time.Now)
			} else {
				natsURL, stopNATS := startLocalNATSServer(b)
				defer stopNATS()

				tickBucket := "tick_bench_ingest_" + metric.Name
				dataBucket := "data_bench_ingest_" + metric.Name
				ensureStateBuckets(b, natsURL, tickBucket, dataBucket)

				natsStore, err := state.NewNATSStore(config.NATSStateConfig{
					URL:                []string{natsURL},
					TickBucket:         tickBucket,
					DataBucket:         dataBucket,
					AllowCreateBuckets: true,
				})
				if err != nil {
					b.Fatalf("new nats store: %v", err)
				}
				store = natsStore
				defer func() {
					_ = natsStore.Close()
				}()
			}

			dispatcher := notify.NewDispatcher(config.NotifyConfig{}, logger)
			manager := app.NewManager(cfg, logger, store, dispatcher, clock.RealClock{})
			sink := &countingSink{manager: manager}
			httpHandler := ingest.NewHTTPHandler(sink, 1<<20)
			httpServer := httptest.NewServer(httpHandler)
			defer httpServer.Close()

			httpClient := &http.Client{
				Timeout: 10 * time.Second,
			}

			eventJSON := []byte(`{"dt":1739876543210,"type":"event","tags":{"dc":"dc1","service":"api","host":"bench-host"},"var":"errors","value":{"t":"n","n":1},"agg_cnt":1,"win":0}`)

			b.ReportAllocs()
			b.ResetTimer()
			startedAt := time.Now()
			for i := 0; i < b.N; i++ {
				request, err := http.NewRequest(http.MethodPost, httpServer.URL, bytes.NewReader(eventJSON))
				if err != nil {
					b.Fatalf("new request: %v", err)
				}
				request.Header.Set("Content-Type", "application/json")
				response, err := httpClient.Do(request)
				if err != nil {
					b.Fatalf("http ingest event %d: %v", i, err)
				}
				_ = response.Body.Close()
				if response.StatusCode != http.StatusAccepted {
					b.Fatalf("http ingest status: got=%d want=%d", response.StatusCode, http.StatusAccepted)
				}
			}

			processed := sink.count.Load()
			elapsed := time.Since(startedAt)
			eventsPerSecond := float64(processed) / elapsed.Seconds()
			backlog := int64(b.N) - processed
			if backlog < 0 {
				backlog = 0
			}
			b.ReportMetric(eventsPerSecond, "events/sec")
			b.ReportMetric(float64(processed), "processed_events")
			b.ReportMetric(float64(backlog), "backlog_events")
		})
	}
}

// BenchmarkHTTPBatchIngestThroughput measures HTTP batch ingest throughput:
// producer -> HTTP /ingest/batch -> manager pipeline.
func BenchmarkHTTPBatchIngestThroughput(b *testing.B) {
	batchSize := benchmarkBatchSizeFromEnv("E2E_HTTP_BATCH_SIZE", 10)

	for _, metric := range allE2EMetricCases() {
		metric := metric
		b.Run(metric.Name, func(b *testing.B) {
			mode := benchmarkServiceMode()
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
					benchmarkRuleForMetric(metric, "bench_http_batch_"+metric.Name),
				},
			}

			var store state.Store
			if mode == "single" {
				store = state.NewMemoryStore(time.Now)
			} else {
				natsURL, stopNATS := startLocalNATSServer(b)
				defer stopNATS()

				tickBucket := "tick_bench_http_batch_" + metric.Name
				dataBucket := "data_bench_http_batch_" + metric.Name
				ensureStateBuckets(b, natsURL, tickBucket, dataBucket)

				natsStore, err := state.NewNATSStore(config.NATSStateConfig{
					URL:                []string{natsURL},
					TickBucket:         tickBucket,
					DataBucket:         dataBucket,
					AllowCreateBuckets: true,
				})
				if err != nil {
					b.Fatalf("new nats store: %v", err)
				}
				store = natsStore
				defer func() {
					_ = natsStore.Close()
				}()
			}

			dispatcher := notify.NewDispatcher(config.NotifyConfig{}, logger)
			manager := app.NewManager(cfg, logger, store, dispatcher, clock.RealClock{})
			sink := &countingSink{manager: manager}
			httpHandler := ingest.NewHTTPHandler(sink, 1<<20)
			httpServer := httptest.NewServer(httpHandler)
			defer httpServer.Close()

			httpClient := &http.Client{
				Timeout: 10 * time.Second,
			}
			batchJSON := buildBatchPayload(batchSize)

			b.ReportAllocs()
			b.ResetTimer()
			startedAt := time.Now()
			for i := 0; i < b.N; i++ {
				request, err := http.NewRequest(http.MethodPost, httpServer.URL+"/batch", bytes.NewReader(batchJSON))
				if err != nil {
					b.Fatalf("new request: %v", err)
				}
				request.Header.Set("Content-Type", "application/json")
				response, err := httpClient.Do(request)
				if err != nil {
					b.Fatalf("http batch ingest request %d: %v", i, err)
				}
				_ = response.Body.Close()
				if response.StatusCode != http.StatusAccepted {
					b.Fatalf("http batch ingest status: got=%d want=%d", response.StatusCode, http.StatusAccepted)
				}
			}

			processed := sink.count.Load()
			elapsed := time.Since(startedAt)
			expected := int64(b.N * batchSize)
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
		})
	}
}

func buildBatchPayload(size int) []byte {
	if size <= 0 {
		return []byte("[]")
	}
	payload := bytes.Repeat([]byte(`{"dt":1739876543210,"type":"event","tags":{"dc":"dc1","service":"api","host":"bench-host"},"var":"errors","value":{"t":"n","n":1},"agg_cnt":1,"win":0},`), size)
	payload[len(payload)-1] = ']'
	payload = append([]byte{'['}, payload...)
	return payload
}

func benchmarkBatchSizeFromEnv(envName string, defaultValue int) int {
	raw := strings.TrimSpace(os.Getenv(envName))
	if raw == "" {
		return defaultValue
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 {
		return defaultValue
	}
	return parsed
}

func benchmarkRuleForMetric(metric e2eMetricCase, name string) config.RuleConfig {
	rule := config.RuleConfig{
		Name:      name,
		AlertType: metric.AlertType,
		Match: config.RuleMatch{
			Type: []string{"event"},
			Var:  []string{e2eMetricVar},
			Tags: map[string]config.StringList{
				"dc":      {"dc1"},
				"service": {"api"},
			},
		},
		Key: config.RuleKey{
			FromTags: []string{"dc", "service", "host"},
		},
	}

	switch metric.AlertType {
	case "count_total":
		rule.Raise.N = 1_000_000_000
		rule.Resolve.SilenceSec = 3600
	case "count_window":
		rule.Raise.N = 1_000_000_000
		rule.Raise.TaggSec = 60
		rule.Resolve.SilenceSec = 3600
	default:
		rule.Raise.MissingSec = 3600
	}
	return rule
}
