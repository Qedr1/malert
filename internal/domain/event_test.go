package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeEventReader(t *testing.T) {
	t.Parallel()

	event, err := DecodeEventReader(json.NewDecoder(strings.NewReader(validEventJSON("h1"))))
	if err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if event.Var != "errors" {
		t.Fatalf("unexpected var %q", event.Var)
	}
}

func TestDecodeEventsReader(t *testing.T) {
	t.Parallel()

	payload := "[" + validEventJSON("h1") + "," + validEventJSON("h2") + "]"
	events, err := DecodeEventsReader(json.NewDecoder(strings.NewReader(payload)))
	if err != nil {
		t.Fatalf("decode batch: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
}

func TestDecodeEventsReaderRejectsEmptyBatch(t *testing.T) {
	t.Parallel()

	if _, err := DecodeEventsReader(json.NewDecoder(strings.NewReader("[]"))); err == nil {
		t.Fatalf("expected error for empty batch")
	}
}

func TestDecodeEventReaderAcceptsFirstJSONValue(t *testing.T) {
	t.Parallel()

	if _, err := DecodeEventReader(json.NewDecoder(strings.NewReader(validEventJSON("h1") + "{}"))); err != nil {
		t.Fatalf("unexpected decode error: %v", err)
	}
}

func validEventJSON(host string) string {
	return `{"dt":1739876543210,"type":"event","tags":{"dc":"dc1","service":"api","host":"` + host + `"},"var":"errors","value":{"t":"n","n":1},"agg_cnt":1,"win":0}`
}
