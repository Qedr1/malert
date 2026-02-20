package ingest

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"alerting/internal/config"
	"alerting/internal/domain"

	"github.com/nats-io/nats.go"
)

// NATSSubscriber consumes events via JetStream queue consumer and forwards to sink.
// Params: NATS connection, JetStream queue subscription, and event sink.
// Returns: NATS ingest lifecycle handle.
type NATSSubscriber struct {
	nc     *nats.Conn
	sub    *nats.Subscription
	logger *slog.Logger
}

// NewNATSSubscriber creates JetStream queue consumer for event ingestion.
// Params: ingest NATS config, sink, and optional logger.
// Returns: started subscriber or initialization error.
func NewNATSSubscriber(cfg config.NATSIngestConfig, sink EventSink, logger *slog.Logger) (*NATSSubscriber, error) {
	nc, err := nats.Connect(strings.Join(cfg.URL, ","))
	if err != nil {
		return nil, fmt.Errorf("connect nats ingest: %w", err)
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("jetstream init for ingest: %w", err)
	}

	subscriber := &NATSSubscriber{
		nc:     nc,
		logger: logger,
	}
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
		event, decodeErr := domain.DecodeEvent(message.Data)
		if decodeErr != nil {
			if logger != nil {
				logger.Warn("nats ingest decode failed", "subject", message.Subject, "error", decodeErr.Error())
			}
			subscriber.ackMessage(message, "decode")
			return
		}
		if pushErr := sink.Push(event); pushErr != nil {
			if logger != nil {
				logger.Error("nats ingest push failed", "subject", message.Subject, "error", pushErr.Error())
			}
			subscriber.nackMessage(message, nackDelay)
			return
		}
		subscriber.ackMessage(message, "processed")
	}, subOpts...)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("queue subscribe %q/%q: %w", cfg.Subject, cfg.DeliverGroup, err)
	}
	subscriber.sub = sub
	return subscriber, nil
}

// ackMessage acknowledges processed/invalid message and logs ack failures.
// Params: JetStream message and short reason.
// Returns: none.
func (s *NATSSubscriber) ackMessage(message *nats.Msg, reason string) {
	if message == nil {
		return
	}
	if err := message.Ack(); err != nil && s.logger != nil {
		s.logger.Warn("nats ingest ack failed", "subject", message.Subject, "reason", reason, "error", err.Error())
	}
}

// nackMessage asks JetStream to redeliver message and logs nack failures.
// Params: JetStream message and optional delay.
// Returns: none.
func (s *NATSSubscriber) nackMessage(message *nats.Msg, delay time.Duration) {
	if message == nil {
		return
	}
	var err error
	if delay > 0 {
		err = message.NakWithDelay(delay)
	} else {
		err = message.Nak()
	}
	if err != nil && s.logger != nil {
		s.logger.Warn("nats ingest nack failed", "subject", message.Subject, "error", err.Error())
	}
}

// Close stops NATS subscription and closes connection.
// Params: none.
// Returns: close error from subscription drain.
func (s *NATSSubscriber) Close() error {
	if s.sub != nil {
		if err := s.sub.Drain(); err != nil {
			s.nc.Close()
			return err
		}
	}
	s.nc.Close()
	return nil
}
