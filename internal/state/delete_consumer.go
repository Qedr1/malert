package state

import (
	"context"
	"strings"

	"alerting/internal/config"

	"github.com/nats-io/nats.go"
)

// DeleteMarkerConsumer consumes KV delete-marker events from tick stream.
// Params: NATS connection, subscription, and callback handler.
// Returns: queue consumer lifecycle handle.
type DeleteMarkerConsumer struct {
	nc  *nats.Conn
	sub *nats.Subscription
}

// NewDeleteMarkerConsumer starts queue consumer for tick delete markers.
// Params: NATS settings and callback for resolved alert IDs.
// Returns: running consumer or setup error.
func NewDeleteMarkerConsumer(cfg config.NATSStateConfig, handler func(ctx context.Context, alertID, reason string) error) (*DeleteMarkerConsumer, error) {
	nc, err := nats.Connect(strings.Join(cfg.URL, ","))
	if err != nil {
		return nil, err
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, err
	}

	consumer := &DeleteMarkerConsumer{nc: nc}
	stream := "KV_" + cfg.TickBucket
	subject := cfg.DeleteSubjectWildcard

	sub, err := js.QueueSubscribe(subject, cfg.DeleteDeliverGroup, func(message *nats.Msg) {
		reason := message.Header.Get("Nats-Marker-Reason")
		if len(message.Data) != 0 {
			_ = message.Ack()
			return
		}
		alertID := extractKVKeyFromSubject(cfg.TickBucket, message.Subject)
		if alertID != "" && handler != nil {
			if err := handler(context.Background(), alertID, reason); err != nil {
				_ = message.Nak()
				return
			}
		}
		_ = message.Ack()
	},
		nats.BindStream(stream),
		nats.Durable(cfg.DeleteConsumerName),
		nats.ManualAck(),
		nats.DeliverNew(),
		nats.AckExplicit(),
	)
	if err != nil {
		nc.Close()
		return nil, err
	}

	consumer.sub = sub
	return consumer, nil
}

// Close drains subscription and closes NATS connection.
// Params: none.
// Returns: close error when drain fails.
func (c *DeleteMarkerConsumer) Close() error {
	if c.sub != nil {
		if err := c.sub.Drain(); err != nil {
			c.nc.Close()
			return err
		}
	}
	c.nc.Close()
	return nil
}

// extractKVKeyFromSubject extracts key from $KV.<bucket>.<key> subject.
// Params: bucket name and full subject.
// Returns: decoded key or empty on mismatch.
func extractKVKeyFromSubject(bucket, subject string) string {
	prefix := "$KV." + bucket + "."
	if !strings.HasPrefix(subject, prefix) {
		return ""
	}
	return strings.TrimPrefix(subject, prefix)
}
