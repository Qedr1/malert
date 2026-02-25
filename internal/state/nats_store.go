package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"alerting/internal/config"
	"alerting/internal/domain"

	"github.com/nats-io/nats.go"
)

// NATSStore persists alert state in JetStream KV buckets.
// Params: NATS connection, JetStream context, and KV bucket handles.
// Returns: KV-backed state store implementation.
type NATSStore struct {
	nc                *nats.Conn
	js                nats.JetStreamContext
	tickKV            nats.KeyValue
	dataKV            nats.KeyValue
	settings          config.NATSStateConfig
	tickSubjectPrefix string
}

// NewNATSStore creates KV buckets and returns NATS state backend.
// Params: NATS/JetStream settings from config.
// Returns: initialized NATS store or setup error.
func NewNATSStore(settings config.NATSStateConfig) (*NATSStore, error) {
	nc, err := nats.Connect(strings.Join(settings.URL, ","))
	if err != nil {
		return nil, fmt.Errorf("connect nats: %w", err)
	}

	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("jetstream init: %w", err)
	}

	tickKV, err := js.KeyValue(settings.TickBucket)
	if err != nil {
		if !settings.AllowCreateBuckets {
			nc.Close()
			return nil, fmt.Errorf("open tick bucket %q: %w", settings.TickBucket, err)
		}
		tickKV, err = js.CreateKeyValue(&nats.KeyValueConfig{
			Bucket: settings.TickBucket,
		})
		if err != nil {
			nc.Close()
			return nil, fmt.Errorf("create tick bucket %q: %w", settings.TickBucket, err)
		}
	}
	if err := enableBucketPerMessageTTL(js, settings.TickBucket); err != nil {
		nc.Close()
		return nil, fmt.Errorf("enable per-message ttl on tick bucket: %w", err)
	}

	dataKV, err := js.KeyValue(settings.DataBucket)
	if err != nil {
		if !settings.AllowCreateBuckets {
			nc.Close()
			return nil, fmt.Errorf("open data bucket %q: %w", settings.DataBucket, err)
		}
		dataKV, err = js.CreateKeyValue(&nats.KeyValueConfig{
			Bucket: settings.DataBucket,
		})
		if err != nil {
			nc.Close()
			return nil, fmt.Errorf("create data bucket %q: %w", settings.DataBucket, err)
		}
	}

	return &NATSStore{
		nc:                nc,
		js:                js,
		tickKV:            tickKV,
		dataKV:            dataKV,
		settings:          settings,
		tickSubjectPrefix: "$KV." + settings.TickBucket + ".",
	}, nil
}

// enableBucketPerMessageTTL ensures underlying KV stream allows Nats-TTL header.
// Params: JetStream context and KV bucket name.
// Returns: stream update error when config cannot be applied.
func enableBucketPerMessageTTL(js nats.JetStreamContext, bucket string) error {
	streamName := "KV_" + bucket
	info, err := js.StreamInfo(streamName)
	if err != nil {
		return err
	}
	if info.Config.AllowMsgTTL {
		return nil
	}
	cfg := info.Config
	cfg.AllowMsgTTL = true
	if cfg.SubjectDeleteMarkerTTL == 0 {
		cfg.SubjectDeleteMarkerTTL = 5 * time.Minute
	}
	_, err = js.UpdateStream(&cfg)
	return err
}

// RefreshTick creates or updates tick entry for alert.
// Params: alert ID key, last-seen timestamp, and resolve TTL.
// Returns: publish error.
func (s *NATSStore) RefreshTick(_ context.Context, alertID string, lastSeen time.Time, ttl time.Duration) error {
	ttlMS := ttl.Milliseconds()
	payload := buildTickPayload(lastSeen.UnixMilli(), ttlMS)
	msg := nats.NewMsg(s.tickSubjectPrefix + alertID)
	msg.Data = payload
	if ttl > 0 {
		msg.Header = nats.Header{
			"Nats-TTL": []string{strconv.FormatInt(ttlMS, 10) + "ms"},
		}
	}
	if _, err := s.js.PublishMsg(msg); err != nil {
		return fmt.Errorf("publish tick: %w", err)
	}
	return nil
}

// buildTickPayload encodes lightweight tick metadata without reflective map encoding.
// Params: event last-seen unix ms and ttl ms.
// Returns: compact JSON payload for KV value.
func buildTickPayload(lastSeenUnixMS, ttlMS int64) []byte {
	payload := make([]byte, 0, 64)
	payload = append(payload, `{"last_seen_unix_ms":`...)
	payload = strconv.AppendInt(payload, lastSeenUnixMS, 10)
	payload = append(payload, `,"ttl_ms":`...)
	payload = strconv.AppendInt(payload, ttlMS, 10)
	payload = append(payload, '}')
	return payload
}

// HasTick checks whether tick key currently exists.
// Params: alert ID key.
// Returns: true when tick key exists.
func (s *NATSStore) HasTick(_ context.Context, alertID string) (bool, error) {
	if _, err := s.tickKV.Get(alertID); err != nil {
		if err == nats.ErrKeyNotFound {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// GetCard reads one card and its KV revision.
// Params: alert ID key.
// Returns: card payload, revision, or ErrNotFound.
func (s *NATSStore) GetCard(_ context.Context, alertID string) (domain.AlertCard, uint64, error) {
	entry, err := s.dataKV.Get(alertID)
	if err != nil {
		if err == nats.ErrKeyNotFound {
			return domain.AlertCard{}, 0, ErrNotFound
		}
		return domain.AlertCard{}, 0, fmt.Errorf("get card: %w", err)
	}

	var card domain.AlertCard
	if err := json.Unmarshal(entry.Value(), &card); err != nil {
		return domain.AlertCard{}, 0, fmt.Errorf("decode card: %w", err)
	}
	return card, entry.Revision(), nil
}

// PutCard writes card payload unconditionally.
// Params: alert ID key and card payload.
// Returns: new KV revision.
func (s *NATSStore) PutCard(_ context.Context, alertID string, card domain.AlertCard) (uint64, error) {
	body, err := json.Marshal(card)
	if err != nil {
		return 0, fmt.Errorf("encode card: %w", err)
	}
	rev, err := s.dataKV.Put(alertID, body)
	if err != nil {
		return 0, fmt.Errorf("put card: %w", err)
	}
	return rev, nil
}

// UpdateCard updates card payload using expected revision CAS.
// Params: alert ID key, expected revision, and replacement payload.
// Returns: new KV revision or ErrConflict.
func (s *NATSStore) UpdateCard(_ context.Context, alertID string, expectedRevision uint64, card domain.AlertCard) (uint64, error) {
	body, err := json.Marshal(card)
	if err != nil {
		return 0, fmt.Errorf("encode card: %w", err)
	}
	rev, err := s.dataKV.Update(alertID, body, expectedRevision)
	if err != nil {
		if errors.Is(err, nats.ErrKeyExists) || strings.Contains(strings.ToLower(err.Error()), "wrong last sequence") {
			return 0, ErrConflict
		}
		return 0, fmt.Errorf("update card: %w", err)
	}
	return rev, nil
}

// DeleteCard deletes card and corresponding tick key.
// Params: alert ID key.
// Returns: delete error.
func (s *NATSStore) DeleteCard(_ context.Context, alertID string) error {
	if err := s.dataKV.Delete(alertID); err != nil && err != nats.ErrKeyNotFound {
		return fmt.Errorf("delete card: %w", err)
	}
	if err := s.tickKV.Delete(alertID); err != nil && err != nats.ErrKeyNotFound {
		return fmt.Errorf("delete tick: %w", err)
	}
	return nil
}

// ListAlertIDsByRule lists keys by rule namespace prefix.
// Params: rule name namespace.
// Returns: matching IDs from data bucket.
func (s *NATSStore) ListAlertIDsByRule(_ context.Context, ruleName string) ([]string, error) {
	keys, err := s.dataKV.Keys()
	if err != nil {
		if errors.Is(err, nats.ErrNoKeysFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("list keys: %w", err)
	}
	prefix := "rule/" + strings.ToLower(strings.TrimSpace(ruleName)) + "/"
	ids := make([]string, 0)
	for _, key := range keys {
		if strings.HasPrefix(key, prefix) {
			ids = append(ids, key)
		}
	}
	return ids, nil
}

// Close closes underlying NATS connection.
// Params: none.
// Returns: nil after connection close.
func (s *NATSStore) Close() error {
	s.nc.Close()
	return nil
}
