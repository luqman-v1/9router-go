package middleware

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/models"
)

// TestEstimatePromptTokens pins the estimate against the shapes the gateway
// actually receives. The number itself is an estimate; what matters is that it
// scales with the prompt instead of being a constant, and that it never charges
// a trivial request as free.
func TestEstimatePromptTokens(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantZero  bool
		wantAbove int
	}{
		{
			name:      "openai chat",
			body:      `{"model":"gpt-4o","messages":[{"role":"user","content":"hello there"}]}`,
			wantAbove: 1,
		},
		{
			name:      "anthropic messages",
			body:      `{"model":"claude","system":"be brief","messages":[{"role":"user","content":"explain this"}]}`,
			wantAbove: 1,
		},
		{
			name:      "tool schemas count",
			body:      `{"messages":[{"role":"user","content":"go"}],"tools":[{"name":"read_file","description":"reads a file from disk","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}]}`,
			wantAbove: 5,
		},
		{
			name:      "gemini contents",
			body:      `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`,
			wantAbove: 1,
		},
		{
			name:      "responses input",
			body:      `{"model":"gpt-5","input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`,
			wantAbove: 1,
		},
		{
			name:     "empty body",
			body:     "",
			wantZero: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := estimatePromptTokens([]byte(tt.body))
			if tt.wantZero {
				if got != 0 {
					t.Errorf("estimatePromptTokens(empty) = %d, want 0", got)
				}
				return
			}
			if got < tt.wantAbove {
				t.Errorf("estimatePromptTokens = %d, want >= %d", got, tt.wantAbove)
			}
		})
	}
}

// TestEstimatePromptTokensScalesWithContent is the property that matters for a
// TPM budget: a longer prompt must cost more. A fixed per-request charge would
// pass a "non-zero" test while making the token limit meaningless.
func TestEstimatePromptTokensScalesWithContent(t *testing.T) {
	short := estimatePromptTokens([]byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	long := estimatePromptTokens([]byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("word ", 200) + `"}]}`))
	if long <= short {
		t.Errorf("long prompt estimated at %d, not more than short %d", long, short)
	}
}

// TestRequireRateLimitChargesRealTokens proves a TPM limit actually rejects a
// large prompt and admits a small one, rather than charging every request the
// same fixed amount.
func TestRequireRateLimitChargesRealTokens(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	// Room for roughly one small request and nothing for a large one.
	handler := RequireRateLimit(rl, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	send := func(content string) int {
		body := `{"model":"gpt-4o","messages":[{"role":"user","content":"` + content + `"}]}`
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req = req.WithContext(withAPIKey(req, &models.APIKey{
			ID:                    "k1",
			RateLimitRPM:          new(0),
			RateLimitTPM:          new(40),
			RateLimitConcurrency:  new(0),
		}))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	if got := send("hi"); got != http.StatusOK {
		t.Fatalf("small request got %d, want 200", got)
	}
	if got := send(strings.Repeat("word ", 500)); got != http.StatusTooManyRequests {
		t.Errorf("large prompt got %d, want 429", got)
	}
}

// TestRequireRateLimitLeavesBodyReadable proves the limiter hands the request
// on intact — reading the body to size the charge must not starve the handler.
func TestRequireRateLimitLeavesBodyReadable(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	const body = `{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`

	var seen string
	handler := RequireRateLimit(rl, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("handler could not read body: %v", err)
		}
		seen = string(raw)
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req = req.WithContext(withAPIKey(req, &models.APIKey{
		ID: "k2", RateLimitTPM: new(1000), RateLimitRPM: new(0), RateLimitConcurrency: new(0),
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if seen != body {
		t.Errorf("handler saw body %q, want %q", seen, body)
	}
}

// TestRequireRateLimitDoesNotBufferWhenTPMUnset guards the cost of the feature:
// with no TPM limit configured, the body must never be read by the limiter, so
// large uploads are not buffered twice.
func TestRequireRateLimitDoesNotBufferWhenTPMUnset(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	var reads int
	guard := &countingBody{inner: io.NopCloser(strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`)), reads: &reads}

	handler := RequireRateLimit(rl, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Body = guard
	req.ContentLength = 40
	req = req.WithContext(withAPIKey(req, &models.APIKey{
		ID: "k3", RateLimitRPM: new(10), RateLimitTPM: new(0), RateLimitConcurrency: new(0),
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if reads != 0 {
		t.Errorf("limiter read the body %d time(s) with no TPM limit configured", reads)
	}
}

// countingBody counts how many times the limiter pulled from it.
type countingBody struct {
	inner io.ReadCloser
	reads *int
}

func (c *countingBody) Read(p []byte) (int, error) {
	*c.reads++
	return c.inner.Read(p)
}

func (c *countingBody) Close() error { return c.inner.Close() }

// withAPIKey installs an authenticated key on the request context.
func withAPIKey(req *http.Request, k *models.APIKey) context.Context {
	return context.WithValue(req.Context(), ApiKeyContextKey, k)
}