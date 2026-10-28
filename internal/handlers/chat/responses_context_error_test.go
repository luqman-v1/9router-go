package chat

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/translator"
)

func TestResponsesContextLengthExceededStreaming(t *testing.T) {
	t.Run("single model stream emits response.failed", func(t *testing.T) {
		body := []byte(`{"error":{"code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again.","type":"invalid_request_error"}}`)
		ue := &upstreamError{
			StatusCode: http.StatusBadRequest,
			Body:       body,
		}

		var errBody struct {
			Error map[string]any `json:"error"`
		}
		if err := json.Unmarshal(ue.Body, &errBody); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}

		failedEvent := translator.FormatResponsesFailedSSE(errBody.Error)
		if !strings.Contains(failedEvent, "event: response.failed") {
			t.Errorf("expected event: response.failed, got: %s", failedEvent)
		}
		if !strings.Contains(failedEvent, `"code":"context_length_exceeded"`) {
			t.Errorf("expected context_length_exceeded, got: %s", failedEvent)
		}
	})

	t.Run("combo stream with responses client emits response.failed", func(t *testing.T) {
		rec := httptest.NewRecorder()
		cw := newCommittedResponseWriter(rec)
		cw.stream = true
		cw.responsesClient = true

		body := []byte(`{"error":{"code":"context_length_exceeded","message":"Your input exceeds the context window of this model.","type":"invalid_request_error"}}`)
		ue := &upstreamError{
			StatusCode: http.StatusBadRequest,
			Body:       body,
		}

		var retry passRetry
		retry.writeError(cw, ue)

		if rec.Code != http.StatusOK {
			t.Errorf("expected status 200 OK for SSE terminal event, got %d", rec.Code)
		}
		ct := rec.Header().Get(constants.HeaderContentType)
		if !strings.HasPrefix(ct, constants.ContentTypeEventStream) {
			t.Errorf("expected Content-Type event-stream, got %s", ct)
		}

		respBody := rec.Body.String()
		if !strings.Contains(respBody, "event: response.failed") {
			t.Errorf("expected response.failed in body, got: %s", respBody)
		}
		if !strings.Contains(respBody, `"code":"context_length_exceeded"`) {
			t.Errorf("expected code context_length_exceeded, got: %s", respBody)
		}
		if !strings.Contains(respBody, `"status":"failed"`) {
			t.Errorf("expected status failed, got: %s", respBody)
		}
	})

	t.Run("combo non-stream with responses client returns HTTP 400 JSON", func(t *testing.T) {
		rec := httptest.NewRecorder()
		cw := newCommittedResponseWriter(rec)
		cw.stream = false
		cw.responsesClient = true

		body := []byte(`{"error":{"code":"context_length_exceeded","message":"Your input exceeds the context window of this model.","type":"invalid_request_error"}}`)
		ue := &upstreamError{
			StatusCode: http.StatusBadRequest,
			Body:       body,
		}

		var retry passRetry
		retry.writeError(cw, ue)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected status 400 Bad Request, got %d", rec.Code)
		}
		ct := rec.Header().Get(constants.HeaderContentType)
		if !strings.HasPrefix(ct, constants.ContentTypeJSON) {
			t.Errorf("expected Content-Type application/json, got %s", ct)
		}

		respBody := rec.Body.String()
		if strings.Contains(respBody, "event: response.failed") {
			t.Errorf("did not expect SSE frame in non-streaming response: %s", respBody)
		}
		if !strings.Contains(respBody, `"code":"context_length_exceeded"`) {
			t.Errorf("expected code context_length_exceeded, got: %s", respBody)
		}
	})
}
