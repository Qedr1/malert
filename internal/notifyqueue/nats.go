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
	nc, err := nats.Connect(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("connect notify queue nats: %w", err)
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("jetstream init for notify queue: %w", err)
	}
	if err := ensureNotifyQueueStream(js, cfg.Stream, cfg.Subject); err != nil {
		nc.Close()
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
	sub    *nats.Subscription
	logger *slog.Logger
}

// NewNATSWorker starts queue consumer for notification delivery jobs.
// Params: queue config, logger, and per-job handler callback.
// Returns: running worker or setup error.
func NewNATSWorker(cfg config.NotifyQueue, logger *slog.Logger, handler func(ctx context.Context, job Job) error) (*NATSWorker, error) {
	nc, err := nats.Connect(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("connect notify queue nats: %w", err)
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("jetstream init for notify queue worker: %w", err)
	}
	if err := ensureNotifyQueueStream(js, cfg.Stream, cfg.Subject); err != nil {
		nc.Close()
		return nil, err
	}

	worker := &NATSWorker{nc: nc, logger: logger}
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

// ensureNotifyQueueStream ensures queue stream exists with configured subject.
// Params: JetStream context, stream name, and subject.
// Returns: stream create/lookup error.
func ensureNotifyQueueStream(js nats.JetStreamContext, streamName, subject string) error {
	if _, err := js.StreamInfo(streamName); err == nil {
		return nil
	} else if err != nil && err != nats.ErrStreamNotFound && !strings.Contains(strings.ToLower(err.Error()), "stream not found") {
		return fmt.Errorf("stream info %q: %w", streamName, err)
	}

	_, err := js.AddStream(&nats.StreamConfig{
		Name:      streamName,
		Subjects:  []string{subject},
		Retention: nats.WorkQueuePolicy,
		Storage:   nats.FileStorage,
		MaxAge:    notifyStreamMaxAge,
	})
	if err != nil {
		return fmt.Errorf("create notify queue stream %q: %w", streamName, err)
	}
	return nil
}
