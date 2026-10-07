package middleware

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMaxBodyMiddleware(t *testing.T) {
	middleware := MaxBody(100)
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	// Case 1: Body within limit
	reqSmall := httptest.NewRequest("POST", "/test", bytes.NewReader(make([]byte, 50)))
	recSmall := httptest.NewRecorder()
	handler.ServeHTTP(recSmall, reqSmall)
	if recSmall.Code != http.StatusOK {
		t.Errorf("expected 200 for small body, got %d", recSmall.Code)
	}

	// Case 2: Body exceeds limit
	reqLarge := httptest.NewRequest("POST", "/test", bytes.NewReader(make([]byte, 200)))
	recLarge := httptest.NewRecorder()
	handler.ServeHTTP(recLarge, reqLarge)
	if recLarge.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413 for oversized body, got %d", recLarge.Code)
	}
}
