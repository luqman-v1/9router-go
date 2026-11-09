package executor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/proxy"
)

// A Claude client reported `HTTP 200 … body is JSON but not a Message` on the
// non-streaming retry of a streaming codex request. An upstream that ignores
// `stream:false` answers 200 with an event stream whose error frame
// sseToOpenAIJSON folds into {"error":{...}} with ok=true. jsonResponse must
// fail that over instead of serving a non-Message body under a 200.
func TestJSONResponse_UpstreamErrorBodyFailsOver(t *testing.T) {
	cases := []struct{ name, body string }{
		{"event stream", "data: {\"error\":{\"type\":\"FreeTierError\",\"message\":\"free tier only\"}}\n\n"},
		{"error object", `{"error":{"type":"FreeTierError","message":"free tier only"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			err := jsonResponse(context.Background(), rec, strings.NewReader(tc.body), true, nil)

			var ue *proxy.UpstreamError
			if !errors.As(err, &ue) {
				t.Fatalf("expected *proxy.UpstreamError, got %T: %v", err, err)
			}
			if ue.StatusCode != http.StatusBadGateway {
				t.Errorf("expected 502, got %d", ue.StatusCode)
			}
			if !strings.Contains(string(ue.Body), "free tier") {
				t.Errorf("expected the upstream message preserved, got %s", string(ue.Body))
			}
			if rec.Body.Len() != 0 {
				t.Errorf("must not write a non-Message body, got %s", rec.Body.String())
			}
		})
	}
}

// A body with neither choices nor error cannot serve a Claude client, and the
// old fallback wrote it under a 200.
func TestJSONResponse_UntranslatableBodyIsUpstreamFailure(t *testing.T) {
	rec := httptest.NewRecorder()
	err := jsonResponse(context.Background(), rec, strings.NewReader(`{"choices":[]}`), true, nil)

	var ue *proxy.UpstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *proxy.UpstreamError, got %T: %v", err, err)
	}
	if ue.StatusCode != http.StatusBadGateway {
		t.Errorf("expected 502, got %d", ue.StatusCode)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("must not write the untranslated body, got %s", rec.Body.String())
	}
}
