package notifyqueue

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"alerting/internal/config"

	"github.com/nats-io/nats.go"
)

const notifyStreamMaxAge = 24 * time.Hour
const notifyDLQStreamMaxAge = 7 * 24 * time.Hour

// NATSProducer publishes notification jobs into JetStream stream.
// Params: NATS connection and publish subject settings.
// Returns: queue producer implementation.
type NATSProducer struct {
	nc      *nats.Conn
	js      nats.JetStreamContext
	subject string
}

// NewNATSProducer creates JetStream producer for notification queue.
// Params: queue config from notify section.
// Returns: initialized producer or setup error.
func NewNATSProducer(cfg config.NotifyQueue) (*NATSProducer, error) {
	nc, js, err := openNotifyQueueJetStream(cfg)
	if err != nil {
		return nil, err
	}
	return &NATSProducer{nc: nc, js: js, subject: cfg.Subject}, nil
}

// Enqueue publishes one notification job into queue stream.
// Params: context and queue job payload.
// Returns: publish error.
func (p *NATSProducer) Enqueue(ctx context.Context, job Job) error {
	body, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("marshal notify queue job: %w", err)
	}
	msg := nats.NewMsg(p.subject)
	msg.Data = body
	if strings.TrimSpace(job.ID) != "" {
		msg.Header.Set("Nats-Msg-Id", strings.TrimSpace(job.ID))
	}
	if _, err := p.js.PublishMsg(msg, nats.Context(ctx)); err != nil {
		return fmt.Errorf("publish notify queue job: %w", err)
	}
	return nil
}

// Close closes producer NATS connection.
// Params: none.
// Returns: nil after connection close.
func (p *NATSProducer) Close() error {
	if p == nil || p.nc == nil {
		return nil
	}
	p.nc.Close()
	return nil
}

// NATSWorker consumes notification queue jobs via queue group consumer.
// Params: NATS connection and queue subscription.
// Returns: worker lifecycle handle.
type NATSWorker struct {
	nc     *nats.Conn
	js     nats.JetStreamContext
	sub    *nats.Subscription
	logger *slog.Logger
	dlq    config.NotifyQueueDLQ
}

// NewNATSWorker starts queue consumer for notification delivery jobs.
// Params: queue config, logger, and per-job handler callback.
// Returns: running worker or setup error.
func NewNATSWorker(cfg config.NotifyQueue, logger *slog.Logger, handler func(ctx context.Context, job Job) error) (*NATSWorker, error) {
	nc, js, err := openNotifyQueueJetStream(cfg)
	if err != nil {
		return nil, err
	}

	worker := &NATSWorker{nc: nc, js: js, logger: logger, dlq: cfg.DLQ}
	ackWait := time.Duration(cfg.AckWaitSec) * time.Second
	nackDelay := time.Duration(cfg.NackDelayMS) * time.Millisecond
	subOpts := []nats.SubOpt{
		nats.BindStream(cfg.Stream),
		nats.Durable(cfg.ConsumerName),
		nats.ManualAck(),
		nats.AckExplicit(),
		nats.AckWait(ackWait),
		nats.MaxDeliver(cfg.MaxDeliver),
		nats.MaxAckPending(cfg.MaxAckPending),
		nats.DeliverAll(),
	}
	sub, err := js.QueueSubscribe(cfg.Subject, cfg.DeliverGroup, func(message *nats.Msg) {
		if message == nil {
			return
		}
		var job Job
		if err := json.Unmarshal(message.Data, &job); err != nil {
			if logger != nil {
				logger.Warn("notify queue decode failed", "subject", message.Subject, "error", err.Error())
			}
			_ = message.Ack()
			return
		}
		if handler != nil {
			if err := handler(context.Background(), job); err != nil {
				if logger != nil {
					logger.Error("notify queue handle failed", "job_id", job.ID, "channel", job.Channel, "error", err.Error())
				}
				attempts := deliveryAttempts(message)
				reason := DLQReason("")
				if IsPermanent(err) {
					reason = DLQReasonPermanentError
				} else if isMaxDeliverExceeded(attempts, cfg.MaxDeliver) {
					reason = DLQReasonMaxDeliverExceeded
				}
				if reason != "" {
					if worker.dlq.Enabled {
						if dlqErr := worker.publishDLQ(context.Background(), message, job, reason, err, attempts, cfg.MaxDeliver); dlqErr != nil {
							if logger != nil {
								logger.Error("notify queue dlq publish failed", "job_id", job.ID, "channel", job.Channel, "reason", reason, "error", dlqErr.Error())
							}
							if nackDelay > 0 {
								_ = message.NakWithDelay(nackDelay)
							} else {
								_ = message.Nak()
							}
							return
						}
					}
					_ = message.Ack()
					return
				}
				if nackDelay > 0 {
					_ = message.NakWithDelay(nackDelay)
				} else {
					_ = message.Nak()
				}
				return
			}
		}
		_ = message.Ack()
	}, subOpts...)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("queue subscribe notify %q/%q: %w", cfg.Subject, cfg.DeliverGroup, err)
	}
	worker.sub = sub
	return worker, nil
}

// Close drains worker subscription and closes NATS connection.
// Params: none.
// Returns: close error from subscription drain.
func (w *NATSWorker) Close() error {
	if w == nil || w.nc == nil {
		return nil
	}
	if w.sub != nil {
		if err := w.sub.Drain(); err != nil {
			w.nc.Close()
			return err
		}
	}
	w.nc.Close()
	return nil
}

// ensureStream ensures one JetStream stream exists with provided options.
// Params: JetStream context and stream settings.
// Returns: stream create/lookup error.
func ensureStream(
	js nats.JetStreamContext,
	streamName string,
	subject string,
	retention nats.RetentionPolicy,
	maxAge time.Duration,
) error {
	if _, err := js.StreamInfo(streamName); err == nil {
		return nil
	} else if err != nil && err != nats.ErrStreamNotFound && !strings.Contains(strings.ToLower(err.Error()), "stream not found") {
		return fmt.Errorf("stream info %q: %w", streamName, err)
	}

	_, err := js.AddStream(&nats.StreamConfig{
		Name:      streamName,
		Subjects:  []string{subject},
		Retention: retention,
		Storage:   nats.FileStorage,
		MaxAge:    maxAge,
	})
	if err != nil {
		return fmt.Errorf("create stream %q: %w", streamName, err)
	}
	return nil
}

// openNotifyQueueJetStream opens connection/JetStream and ensures notify queue stream exists.
// Params: queue config with URL and stream/subject names.
// Returns: opened NATS connection, JetStream context, and setup error.
func openNotifyQueueJetStream(cfg config.NotifyQueue) (*nats.Conn, nats.JetStreamContext, error) {
	nc, err := nats.Connect(cfg.URL)
	if err != nil {
		return nil, nil, fmt.Errorf("connect notify queue nats: %w", err)
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, nil, fmt.Errorf("jetstream init for notify queue: %w", err)
	}
	if err := ensureStream(js, cfg.Stream, cfg.Subject, nats.WorkQueuePolicy, notifyStreamMaxAge); err != nil {
		nc.Close()
		return nil, nil, err
	}
	if cfg.DLQ.Enabled {
		if err := ensureStream(js, cfg.DLQ.Stream, cfg.DLQ.Subject, nats.LimitsPolicy, notifyDLQStreamMaxAge); err != nil {
			nc.Close()
			return nil, nil, err
		}
	}
	return nc, js, nil
}

// deliveryAttempts returns number of delivery attempts from JetStream metadata.
// Params: delivered NATS message.
// Returns: delivered-attempt count (at least 1 when message is non-nil).
func deliveryAttempts(message *nats.Msg) uint64 {
	if message == nil {
		return 0
	}
	metadata, err := message.Metadata()
	if err != nil || metadata == nil || metadata.NumDelivered <= 0 {
		return 1
	}
	return metadata.NumDelivered
}

// isMaxDeliverExceeded reports if current attempt reached configured max deliver.
// Params: attempt counter and max deliver config.
// Returns: true when current attempt is final allowed delivery.
func isMaxDeliverExceeded(attempts uint64, maxDeliver int) bool {
	if maxDeliver <= 0 {
		return false
	}
	return attempts >= uint64(maxDeliver)
}

// publishDLQ publishes failed notify job metadata to configured dead-letter subject.
// Params: message, decoded job, failure reason/cause, and attempt counters.
// Returns: publish error when DLQ publish fails.
func (w *NATSWorker) publishDLQ(
	ctx context.Context,
	message *nats.Msg,
	job Job,
	reason DLQReason,
	cause error,
	attempts uint64,
	maxDeliver int,
) error {
	if w == nil || w.js == nil || !w.dlq.Enabled {
		return nil
	}
	entry := DLQEntry{
		Job:        job,
		Reason:     reason,
		Error:      strings.TrimSpace(errorString(cause)),
		Attempts:   attempts,
		MaxDeliver: maxDeliver,
		FailedAt:   time.Now().UTC(),
	}
	if message != nil {
		entry.Subject = message.Subject
		entry.OriginalMsgID = strings.TrimSpace(message.Header.Get("Nats-Msg-Id"))
	}
	body, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal notify dlq entry: %w", err)
	}
	msg := nats.NewMsg(w.dlq.Subject)
	msg.Data = body
	if strings.TrimSpace(job.ID) != "" {
		msg.Header.Set("Nats-Msg-Id", strings.TrimSpace(job.ID)+":dlq:"+string(reason)+":"+fmt.Sprintf("%d", attempts))
	}
	if _, err := w.js.PublishMsg(msg, nats.Context(ctx)); err != nil {
		return fmt.Errorf("publish notify dlq entry: %w", err)
	}
	return nil
}

// errorString returns safe textual representation for optional error value.
// Params: optional error.
// Returns: non-empty error string.
func errorString(err error) string {
	if err == nil {
		return "unknown error"
	}
	return err.Error()
}
