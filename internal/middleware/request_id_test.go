package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestIDMiddleware_Generated(t *testing.T) {
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := GetRequestID(r)
		if reqID == "" {
			t.Error("expected non-empty request ID from context")
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/v1/models", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	headerReqID := rec.Header().Get("X-Request-ID")
	if headerReqID == "" {
		t.Error("expected X-Request-ID header in response")
	}
}

func TestRequestIDMiddleware_Preserved(t *testing.T) {
	incomingID := "custom-req-id-12345"
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := GetRequestID(r)
		if reqID != incomingID {
			t.Errorf("expected request ID %s, got %s", incomingID, reqID)
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("X-Request-ID", incomingID)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	headerReqID := rec.Header().Get("X-Request-ID")
	if headerReqID != incomingID {
		t.Errorf("expected X-Request-ID header %s, got %s", incomingID, headerReqID)
	}
}
