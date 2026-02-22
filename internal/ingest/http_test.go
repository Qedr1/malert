package ingest

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"alerting/internal/domain"
)

type httpTestSink struct {
	pushCalls  int
	batchCalls int
	events     []domain.Event
	err        error
}

func (s *httpTestSink) Push(event domain.Event) error {
	s.pushCalls++
	if s.err != nil {
		return s.err
	}
	s.events = append(s.events, event)
	return nil
}

func (s *httpTestSink) PushBatch(events []domain.Event) error {
	s.batchCalls++
	if s.err != nil {
		return s.err
	}
	s.events = append(s.events, events...)
	return nil
}

func TestHTTPHandlerAcceptsSingleEvent(t *testing.T) {
	t.Parallel()

	sink := &httpTestSink{}
	handler := NewHTTPHandler(sink, 1<<20)
	request := httptest.NewRequest(http.MethodPost, "/ingest", strings.NewReader(testEventJSON("h1")))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d", http.StatusAccepted, response.Code)
	}
	if sink.pushCalls != 1 || sink.batchCalls != 0 {
		t.Fatalf("unexpected sink calls push=%d batch=%d", sink.pushCalls, sink.batchCalls)
	}
	if len(sink.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(sink.events))
	}
}

func TestHTTPHandlerAcceptsBatchEvents(t *testing.T) {
	t.Parallel()

	sink := &httpTestSink{}
	handler := NewHTTPHandler(sink, 1<<20)
	payload := fmt.Sprintf("[%s,%s]", testEventJSON("h1"), testEventJSON("h2"))
	request := httptest.NewRequest(http.MethodPost, "/ingest/batch", strings.NewReader(payload))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d", http.StatusAccepted, response.Code)
	}
	if sink.pushCalls != 0 || sink.batchCalls != 1 {
		t.Fatalf("unexpected sink calls push=%d batch=%d", sink.pushCalls, sink.batchCalls)
	}
	if len(sink.events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(sink.events))
	}
}

func TestHTTPHandlerRejectsInvalidBatch(t *testing.T) {
	t.Parallel()

	sink := &httpTestSink{}
	handler := NewHTTPHandler(sink, 1<<20)
	request := httptest.NewRequest(http.MethodPost, "/ingest/batch", strings.NewReader("[]"))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, response.Code)
	}
	if sink.pushCalls != 0 || sink.batchCalls != 0 {
		t.Fatalf("unexpected sink calls push=%d batch=%d", sink.pushCalls, sink.batchCalls)
	}
}

func TestHTTPHandlerReturnsServiceUnavailableOnPushError(t *testing.T) {
	t.Parallel()

	sink := &httpTestSink{err: errors.New("sink unavailable")}
	handler := NewHTTPHandler(sink, 1<<20)
	request := httptest.NewRequest(http.MethodPost, "/ingest", strings.NewReader(testEventJSON("h1")))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d", http.StatusServiceUnavailable, response.Code)
	}
}

func testEventJSON(host string) string {
	return fmt.Sprintf(`{"dt":1739876543210,"type":"event","tags":{"dc":"dc1","service":"api","host":"%s"},"var":"errors","value":{"t":"n","n":1},"agg_cnt":1,"win":0}`, host)
}
