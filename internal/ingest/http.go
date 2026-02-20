package ingest

import (
	"io"
	"net/http"

	"alerting/internal/domain"
)

// EventSink receives decoded events from ingest interfaces.
// Params: decoded event payload.
// Returns: processing error.
type EventSink interface {
	Push(event domain.Event) error
}

// HTTPHandler decodes JSON events and forwards them to sink.
// Params: sink receives validated events, max body limits payload size.
// Returns: HTTP handler for ingest endpoint.
type HTTPHandler struct {
	sink        EventSink
	maxBodySize int64
}

// NewHTTPHandler creates ingest HTTP handler.
// Params: sink and max request body size in bytes.
// Returns: configured handler.
func NewHTTPHandler(sink EventSink, maxBodySize int64) *HTTPHandler {
	return &HTTPHandler{sink: sink, maxBodySize: maxBodySize}
}

// ServeHTTP handles one incoming event request.
// Params: HTTP request/response writer pair.
// Returns: writes status code according to decode/push result.
func (h *HTTPHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	request.Body = http.MaxBytesReader(writer, request.Body, h.maxBodySize)
	defer request.Body.Close()
	body, err := io.ReadAll(request.Body)
	if err != nil {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}

	event, err := domain.DecodeEvent(body)
	if err != nil {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}

	if err := h.sink.Push(event); err != nil {
		writer.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	writer.WriteHeader(http.StatusAccepted)
}
