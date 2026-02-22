package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// EventType identifies incoming event shape.
// Params: constants "event" or "agg".
// Returns: normalized event type usage across pipeline.
type EventType string

const (
	// EventTypeEvent marks one raw event.
	EventTypeEvent EventType = "event"
	// EventTypeAgg marks aggregated event window.
	EventTypeAgg EventType = "agg"
)

// TypedValue stores one typed metric value.
// Params: Type selects one payload among N/S/B.
// Returns: strict typed value for matching predicates.
type TypedValue struct {
	Type string   `json:"t"`
	N    *float64 `json:"n,omitempty"`
	S    *string  `json:"s,omitempty"`
	B    *bool    `json:"b,omitempty"`
}

// Event is normalized incoming alert event.
// Params: event timestamp, type, tags, metric var, typed value, and aggregation metadata.
// Returns: validated event payload for rule processing.
type Event struct {
	DT     int64             `json:"dt"`
	Type   EventType         `json:"type"`
	Tags   map[string]string `json:"tags"`
	Var    string            `json:"var"`
	Value  TypedValue        `json:"value"`
	AggCnt int64             `json:"agg_cnt"`
	Win    int64             `json:"win"`
}

// EventTime converts milliseconds unix timestamp into UTC time.
// Params: event timestamp in unix milliseconds.
// Returns: converted UTC time.
func (e Event) EventTime() time.Time {
	return time.UnixMilli(e.DT).UTC()
}

// DecodeEvent decodes and validates one event payload.
// Params: JSON document bytes.
// Returns: validated event or decode/validation error.
func DecodeEvent(raw []byte) (Event, error) {
	var event Event
	if err := json.Unmarshal(raw, &event); err != nil {
		return Event{}, fmt.Errorf("decode event: %w", err)
	}
	if err := event.Validate(); err != nil {
		return Event{}, err
	}
	return event, nil
}

// DecodeEventReader decodes and validates one event payload from stream.
// Params: reader with one JSON object.
// Returns: validated event or decode/validation error.
func DecodeEventReader(reader *json.Decoder) (Event, error) {
	var event Event
	if err := reader.Decode(&event); err != nil {
		return Event{}, fmt.Errorf("decode event: %w", err)
	}
	if err := event.Validate(); err != nil {
		return Event{}, err
	}
	return event, nil
}

// DecodeEventsReader decodes and validates one batch of events from stream.
// Params: reader with one JSON array of events.
// Returns: validated events slice or decode/validation error.
func DecodeEventsReader(reader *json.Decoder) ([]Event, error) {
	var events []Event
	if err := reader.Decode(&events); err != nil {
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
	return events, nil
}

// Validate validates one event against the contract.
// Params: event fields parsed from transport.
// Returns: validation error when schema is violated.
func (e Event) Validate() error {
	if e.DT <= 0 {
		return errors.New("dt must be >0")
	}

	switch e.Type {
	case EventTypeEvent, EventTypeAgg:
	default:
		return fmt.Errorf("unsupported type %q", e.Type)
	}

	if strings.TrimSpace(e.Var) == "" {
		return errors.New("var is required")
	}
	if len(e.Tags) == 0 {
		return errors.New("tags are required")
	}
	if e.AggCnt < 1 {
		return errors.New("agg_cnt must be >=1")
	}

	if e.Type == EventTypeEvent {
		if e.AggCnt != 1 {
			return errors.New("agg_cnt must be 1 for type=event")
		}
		if e.Win != 0 {
			return errors.New("win must be 0 for type=event")
		}
	}
	if e.Type == EventTypeAgg && e.Win <= 0 {
		return errors.New("win must be >0 for type=agg")
	}

	if err := e.Value.Validate(); err != nil {
		return fmt.Errorf("value: %w", err)
	}

	return nil
}

// Validate validates typed value contract.
// Params: explicit type marker and one value payload.
// Returns: validation error when value is inconsistent.
func (v TypedValue) Validate() error {
	switch v.Type {
	case "n":
		if v.N == nil {
			return errors.New("n value is required for t=n")
		}
		if v.S != nil || v.B != nil {
			return errors.New("only n must be set for t=n")
		}
	case "s":
		if v.S == nil {
			return errors.New("s value is required for t=s")
		}
		if v.N != nil || v.B != nil {
			return errors.New("only s must be set for t=s")
		}
	case "b":
		if v.B == nil {
			return errors.New("b value is required for t=b")
		}
		if v.N != nil || v.S != nil {
			return errors.New("only b must be set for t=b")
		}
	default:
		return fmt.Errorf("unsupported value type %q", v.Type)
	}
	return nil
}
