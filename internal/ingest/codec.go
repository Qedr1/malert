package ingest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"alerting/internal/domain"
)

const maxPooledBatchCapacity = 4096

type decodeScratch struct {
	events []domain.Event
}

var decodeScratchPool = sync.Pool{
	New: func() any {
		return &decodeScratch{events: make([]domain.Event, 0, 16)}
	},
}

// decodeSingleEvent decodes one event and rejects trailing JSON tokens.
// Params: json decoder for a single event object.
// Returns: validated event or decode error.
func decodeSingleEvent(decoder *json.Decoder) (domain.Event, error) {
	event, err := domain.DecodeEventReader(decoder)
	if err != nil {
		return domain.Event{}, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return domain.Event{}, err
	}
	return event, nil
}

// decodeBatchEvents decodes one batch and rejects trailing JSON tokens.
// Params: json decoder for a single array payload.
// Returns: validated events or decode error.
func decodeBatchEvents(decoder *json.Decoder) ([]domain.Event, error) {
	events, err := domain.DecodeEventsReader(decoder)
	if err != nil {
		return nil, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	return events, nil
}

// decodeEventPayload auto-detects batch vs single payload.
// Params: raw JSON bytes with one object or array.
// Returns: validated events slice.
func decodeEventPayload(raw []byte) ([]domain.Event, error) {
	scratch := acquireDecodeScratch()
	defer releaseDecodeScratch(scratch)
	return decodeEventPayloadInto(raw, scratch)
}

func decodeEventPayloadInto(raw []byte, scratch *decodeScratch) ([]domain.Event, error) {
	payload := bytes.TrimSpace(raw)
	if len(payload) == 0 {
		return nil, errors.New("empty payload")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if payload[0] == '[' {
		return decodeBatchEventsInto(decoder, scratch)
	}
	event, err := decodeSingleEvent(decoder)
	if err != nil {
		return nil, err
	}
	events := scratch.events[:0]
	events = append(events, event)
	scratch.events = events
	return events, nil
}

func decodeBatchEventsInto(decoder *json.Decoder, scratch *decodeScratch) ([]domain.Event, error) {
	events := scratch.events[:0]
	if err := decoder.Decode(&events); err != nil {
		return nil, fmt.Errorf("decode event batch: %w", err)
	}
	if len(events) == 0 {
		return nil, errors.New("event batch must contain at least one event")
	}
	for i := range events {
		if err := events[i].Validate(); err != nil {
			return nil, fmt.Errorf("event[%d]: %w", i, err)
		}
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	scratch.events = events
	return events, nil
}

func acquireDecodeScratch() *decodeScratch {
	return decodeScratchPool.Get().(*decodeScratch)
}

func releaseDecodeScratch(scratch *decodeScratch) {
	if scratch == nil {
		return
	}
	for i := range scratch.events {
		scratch.events[i] = domain.Event{}
	}
	if cap(scratch.events) > maxPooledBatchCapacity {
		scratch.events = make([]domain.Event, 0, 16)
	} else {
		scratch.events = scratch.events[:0]
	}
	decodeScratchPool.Put(scratch)
}

// ensureJSONEOF rejects trailing tokens after a decoded JSON payload.
// Params: decoder positioned after primary decode.
// Returns: nil on EOF or error on trailing tokens.
func ensureJSONEOF(decoder *json.Decoder) error {
	var extra json.RawMessage
	err := decoder.Decode(&extra)
	if err == io.EOF {
		return nil
	}
	if err != nil {
		return fmt.Errorf("decode trailing json: %w", err)
	}
	return errors.New("unexpected trailing json tokens")
}

// pushEvents sends events to sink with optional batch support.
// Params: event sink and event slice.
// Returns: first push error or nil.
func pushEvents(sink EventSink, events []domain.Event) error {
	if len(events) == 0 {
		return nil
	}
	if batchSink, ok := sink.(batchEventSink); ok {
		return batchSink.PushBatch(events)
	}
	for _, event := range events {
		if err := sink.Push(event); err != nil {
			return err
		}
	}
	return nil
}
