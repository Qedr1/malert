package e2e

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

const deliveryBenchmarkRuleName = "bench_delivery"

var deliveryBenchmarkBatchSizes = []int{1, 100, 1000}

type deliveryBenchmarkTarget struct {
	baseURL string
	count   *atomic.Int64
	close   func()
}

type deliveryBenchmarkChannel struct {
	name         string
	templateName string
	notifyConfig func(baseURL string) string
	setupTarget  func(tb testing.TB) deliveryBenchmarkTarget
}

// BenchmarkHTTPDeliveryE2E measures real HTTP batch ingest -> manager -> notify delivery.
// Params: benchmark handle.
// Returns: per-channel delivery throughput metrics under batch sizes 1/100/1000.
func BenchmarkHTTPDeliveryE2E(b *testing.B) {
	for _, channel := range deliveryBenchmarkChannels() {
		channel := channel
		b.Run(channel.name, func(b *testing.B) {
			for _, batchSize := range deliveryBenchmarkBatchSizes {
				batchSize := batchSize
				b.Run(fmt.Sprintf("batch_%d", batchSize), func(b *testing.B) {
					runDeliveryBenchmark(b, "http", batchSize, channel)
				})
			}
		})
	}
}

// BenchmarkNATSDeliveryE2E measures real NATS batch ingest -> manager -> notify delivery.
// Params: benchmark handle.
// Returns: per-channel delivery throughput metrics under batch sizes 1/100/1000.
func BenchmarkNATSDeliveryE2E(b *testing.B) {
	for _, channel := range deliveryBenchmarkChannels() {
		channel := channel
		b.Run(channel.name, func(b *testing.B) {
			for _, batchSize := range deliveryBenchmarkBatchSizes {
				batchSize := batchSize
				b.Run(fmt.Sprintf("batch_%d", batchSize), func(b *testing.B) {
					runDeliveryBenchmark(b, "nats", batchSize, channel)
				})
			}
		})
	}
}

func runDeliveryBenchmark(b *testing.B, ingestKind string, batchSize int, channel deliveryBenchmarkChannel) {
	b.Helper()

	port, err := freePort()
	if err != nil {
		b.Fatalf("free port: %v", err)
	}

	natsURL, stopNATS := startLocalNATSServer(b)
	defer stopNATS()

	if ingestKind == "nats" {
		ensureEventStream(b, natsURL, e2eEventsStream, e2eEventsSubj)
	}

	target := channel.setupTarget(b)
	defer target.close()

	configPath := filepath.Join(b.TempDir(), "config.toml")
	configBody := deliveryBenchmarkConfig(port, natsURL, ingestKind, channel.notifyConfig(target.baseURL), channel.name, channel.templateName)
	if err := os.WriteFile(configPath, []byte(configBody), 0o644); err != nil {
		b.Fatalf("write config: %v", err)
	}

	service := newServiceFromConfig(b, configPath)
	cancel, done := runService(b, service)
	defer func() {
		cancel()
		waitServiceStop(b, done)
	}()

	waitReady(b, port)

	totalBatches := b.N * deliveryBenchmarkWorkBatches()
	expected := int64(totalBatches * batchSize)
	payloads := make([][]byte, totalBatches)
	for index := 0; index < totalBatches; index++ {
		payloads[index] = buildUniqueBatchPayload(batchSize, index*batchSize)
	}

	var send func([]byte) error
	var natsPublisher *nats.Conn
	switch ingestKind {
	case "http":
		httpClient := &http.Client{Timeout: 30 * time.Second}
		url := fmt.Sprintf("http://127.0.0.1:%d/ingest/batch", port)
		send = func(payload []byte) error {
			request, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
			if err != nil {
				return err
			}
			request.Header.Set("Content-Type", "application/json")
			response, err := httpClient.Do(request)
			if err != nil {
				return err
			}
			_ = response.Body.Close()
			if response.StatusCode != http.StatusAccepted {
				return fmt.Errorf("unexpected ingest status: %d", response.StatusCode)
			}
			return nil
		}
	case "nats":
		natsPublisher, err = nats.Connect(natsURL)
		if err != nil {
			b.Fatalf("connect nats publisher: %v", err)
		}
		defer natsPublisher.Close()
		send = func(payload []byte) error {
			return natsPublisher.Publish(e2eEventsSubj, payload)
		}
	default:
		b.Fatalf("unsupported ingest kind %q", ingestKind)
	}

	b.ReportAllocs()
	b.ResetTimer()
	startedAt := time.Now()
	for _, payload := range payloads {
		if err := send(payload); err != nil {
			b.Fatalf("send payload: %v", err)
		}
	}
	if natsPublisher != nil {
		if err := natsPublisher.FlushTimeout(30 * time.Second); err != nil {
			b.Fatalf("flush nats publisher: %v", err)
		}
	}

	waitForDeliveryCount(b, 60*time.Second, expected, target.count)

	delivered := target.count.Load()
	elapsed := time.Since(startedAt)
	backlog := expected - delivered
	if backlog < 0 {
		backlog = 0
	}
	b.ReportMetric(float64(delivered)/elapsed.Seconds(), "deliveries/sec")
	b.ReportMetric(float64(expected)/elapsed.Seconds(), "events/sec")
	b.ReportMetric(float64(totalBatches)/elapsed.Seconds(), "batches/sec")
	b.ReportMetric(float64(backlog), "backlog_events")
	if backlog > 0 {
		b.Fatalf("delivery backlog: delivered=%d expected=%d", delivered, expected)
	}
}

func deliveryBenchmarkChannels() []deliveryBenchmarkChannel {
	return []deliveryBenchmarkChannel{
		{
			name:         "telegram",
			templateName: "tg_default",
			notifyConfig: func(baseURL string) string {
				return fmt.Sprintf(`
[notify.telegram]
enabled = true
bot_token = "token"
chat_id = "1001"
api_base = "%s"

[notify.telegram.retry]
enabled = false

[[notify.telegram.name-template]]
name = "tg_default"
message = "{{ .RuleName }} {{ .State }} {{ .Var }}={{ .MetricValue }} id={{ .ShortID }}"
`, baseURL)
			},
			setupTarget: newDeliveryTelegramTarget,
		},
		{
			name:         "http",
			templateName: "http_default",
			notifyConfig: func(baseURL string) string {
				return fmt.Sprintf(`
[notify.http]
enabled = true
url = "%s"
method = "POST"
timeout_sec = 2

[notify.http.retry]
enabled = false

[[notify.http.name-template]]
name = "http_default"
message = "{{ .RuleName }} {{ .State }} {{ .Var }}={{ .MetricValue }} id={{ .ShortID }}"
`, baseURL)
			},
			setupTarget: newDeliveryHTTPTarget,
		},
		{
			name:         "mattermost",
			templateName: "mm_default",
			notifyConfig: func(baseURL string) string {
				return fmt.Sprintf(`
[notify.mattermost]
enabled = true
base_url = "%s"
bot_token = "mm-token"
channel_id = "channel-1"
timeout_sec = 2

[notify.mattermost.retry]
enabled = false

[[notify.mattermost.name-template]]
name = "mm_default"
message = "{{ .RuleName }} {{ .State }} {{ .Var }}={{ .MetricValue }} id={{ .ShortID }}"
`, baseURL)
			},
			setupTarget: newDeliveryMattermostTarget,
		},
		{
			name:         "jira",
			templateName: "jira_default",
			notifyConfig: func(baseURL string) string {
				return fmt.Sprintf(`
[notify.jira]
enabled = true
base_url = "%s"
timeout_sec = 2

[notify.jira.auth]
type = "none"

[notify.jira.retry]
enabled = false

[notify.jira.create]
method = "POST"
path = "/rest/api/3/issue"
headers = { Content-Type = "application/json" }
body_template = "{\"summary\": {{ json .Message }}, \"alert_id\": {{ json .AlertID }}}"
success_status = [201]
ref_json_path = "key"

[notify.jira.resolve]
method = "POST"
path = "/rest/api/3/issue/{{ .ExternalRef }}/transitions"
headers = { Content-Type = "application/json" }
body_template = "{\"transition\":{\"id\":\"31\"}}"
success_status = [204]

[[notify.jira.name-template]]
name = "jira_default"
message = "{{ .RuleName }} {{ .State }} {{ .Var }}={{ .MetricValue }} id={{ .ShortID }}"
`, baseURL)
			},
			setupTarget: func(tb testing.TB) deliveryBenchmarkTarget {
				return newDeliveryTrackerTarget(tb, "/rest/api/3/issue", "/rest/api/3/issue/OPS-777/transitions", http.StatusCreated, `{"key":"OPS-777"}`, http.StatusNoContent)
			},
		},
		{
			name:         "youtrack",
			templateName: "youtrack_default",
			notifyConfig: func(baseURL string) string {
				return fmt.Sprintf(`
[notify.youtrack]
enabled = true
base_url = "%s"
timeout_sec = 2

[notify.youtrack.auth]
type = "none"

[notify.youtrack.retry]
enabled = false

[notify.youtrack.create]
method = "POST"
path = "/api/issues"
headers = { Content-Type = "application/json" }
body_template = "{\"summary\": {{ json .Message }}, \"alert_id\": {{ json .AlertID }}}"
success_status = [201]
ref_json_path = "idReadable"

[notify.youtrack.resolve]
method = "POST"
path = "/api/issues/{{ .ExternalRef }}/commands"
headers = { Content-Type = "application/json" }
body_template = "{\"query\":\"Fixed\"}"
success_status = [200]

[[notify.youtrack.name-template]]
name = "youtrack_default"
message = "{{ .RuleName }} {{ .State }} {{ .Var }}={{ .MetricValue }} id={{ .ShortID }}"
`, baseURL)
			},
			setupTarget: func(tb testing.TB) deliveryBenchmarkTarget {
				return newDeliveryTrackerTarget(tb, "/api/issues", "/api/issues/OPS-888/commands", http.StatusCreated, `{"idReadable":"OPS-888"}`, http.StatusOK)
			},
		},
	}
}

func deliveryBenchmarkConfig(port int, natsURL, ingestKind, notifyBlock, routeChannel, templateName string) string {
	natsEnabled := "false"
	natsExtra := ""
	if ingestKind == "nats" {
		natsEnabled = "true"
		natsExtra = fmt.Sprintf(`
workers = %d
ack_wait_sec = 30
nack_delay_ms = 5
max_deliver = -1
max_ack_pending = 8192
`, deliveryBenchmarkNATSWorkers())
	}
	cfg := fmt.Sprintf(`
[service]
name = "alerting"
mode = "nats"
reload_enabled = false
resolve_scan_interval_sec = 1

[log.console]
enabled = true
level = "error"
format = "line"

[ingest.http]
enabled = true
listen = "127.0.0.1:%d"
health_path = "/healthz"
ready_path = "/readyz"
ingest_path = "/ingest"
max_body_bytes = 1048576

[ingest.nats]
enabled = %s
url = ["%s"]%s

[notify]
repeat = false
repeat_every_sec = 300
repeat_on = ["firing"]
repeat_per_channel = true
on_pending = false
`, port, natsEnabled, natsURL, natsExtra)
	cfg += notifyBlock
	cfg += buildRuleTOML(deliveryBenchmarkRuleName, e2eMetricCase{Name: "count_total", AlertType: "count_total"}, e2eMetricVar, e2eRuleOptions{
		RaiseN:            1,
		ResolveSilenceSec: 3600,
		PendingEnabled:    false,
		PendingDelaySec:   300,
	}, fmt.Sprintf(`
[[rule.%s.notify.route]]
channel = "%s"
template = "%s"
`, deliveryBenchmarkRuleName, routeChannel, templateName))
	return cfg
}

func buildUniqueBatchPayload(batchSize, offset int) []byte {
	if batchSize <= 0 {
		return []byte("[]")
	}

	var builder strings.Builder
	builder.Grow(batchSize * 160)
	builder.WriteByte('[')
	for index := 0; index < batchSize; index++ {
		if index > 0 {
			builder.WriteByte(',')
		}
		host := "bench-host-" + strconv.Itoa(offset+index)
		builder.WriteString(`{"dt":1739876543210,"type":"event","tags":{"dc":"dc1","service":"api","host":"`)
		builder.WriteString(host)
		builder.WriteString(`"},"var":"errors","value":{"t":"n","n":1},"agg_cnt":1,"win":0}`)
	}
	builder.WriteByte(']')
	return []byte(builder.String())
}

func deliveryBenchmarkWorkBatches() int {
	return benchmarkBatchSizeFromEnv("E2E_DELIVERY_BATCHES", 20)
}

func deliveryBenchmarkNATSWorkers() int {
	return benchmarkBatchSizeFromEnv("E2E_DELIVERY_NATS_WORKERS", 1)
}

func waitForDeliveryCount(tb testing.TB, timeout time.Duration, expected int64, count *atomic.Int64) {
	tb.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if count.Load() >= expected {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	tb.Fatalf("timeout waiting for deliveries: got=%d want=%d", count.Load(), expected)
}

func newDeliveryTelegramTarget(tb testing.TB) deliveryBenchmarkTarget {
	tb.Helper()

	count := &atomic.Int64{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if request.URL.Path != "/bottoken/sendMessage" {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		if err := request.ParseMultipartForm(2 << 20); err != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		count.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(writer, `{"ok":true,"result":{"message_id":%d,"date":1,"chat":{"id":1,"type":"private"}}}`, count.Load())
	}))
	return deliveryBenchmarkTarget{
		baseURL: server.URL,
		count:   count,
		close:   server.Close,
	}
}

func newDeliveryHTTPTarget(tb testing.TB) deliveryBenchmarkTarget {
	tb.Helper()

	count := &atomic.Int64{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		defer request.Body.Close()
		_, _ = io.Copy(io.Discard, request.Body)
		count.Add(1)
		writer.WriteHeader(http.StatusOK)
	}))
	return deliveryBenchmarkTarget{
		baseURL: server.URL,
		count:   count,
		close:   server.Close,
	}
}

func newDeliveryMattermostTarget(tb testing.TB) deliveryBenchmarkTarget {
	tb.Helper()

	count := &atomic.Int64{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if request.URL.Path != "/api/v4/posts" {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		defer request.Body.Close()
		_, _ = io.Copy(io.Discard, request.Body)
		id := count.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(writer, `{"id":"post-%d"}`, id)
	}))
	return deliveryBenchmarkTarget{
		baseURL: server.URL,
		count:   count,
		close:   server.Close,
	}
}

func newDeliveryTrackerTarget(tb testing.TB, createPath, resolvePath string, createStatus int, createBody string, resolveStatus int) deliveryBenchmarkTarget {
	tb.Helper()

	count := &atomic.Int64{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		defer request.Body.Close()
		_, _ = io.Copy(io.Discard, request.Body)

		switch request.URL.Path {
		case createPath:
			count.Add(1)
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(createStatus)
			_, _ = writer.Write([]byte(createBody))
		case resolvePath:
			writer.WriteHeader(resolveStatus)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	return deliveryBenchmarkTarget{
		baseURL: server.URL,
		count:   count,
		close:   server.Close,
	}
}
