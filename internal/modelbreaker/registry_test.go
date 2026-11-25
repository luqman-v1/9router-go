package modelbreaker

import (
	"testing"

	"9router/proxy/internal/errclass"
)

// TestCountsTowardBreaker pins the #1450 rule: only an availability failure
// charges the per-model breaker. A context-window overflow is produced by
// the caller's own prompt, so charging it lets any caller open the breaker
// for a provider/model pair and 503 every other tenant after five
// oversized requests.
func TestCountsTowardBreaker(t *testing.T) {
	tests := []struct {
		name  string
		class errclass.Classification
		want  bool
	}{
		{
			name:  "model unavailable counts",
			class: errclass.Classification{Kind: errclass.KindModelUnavailable, Scope: errclass.ScopeModel},
			want:  true,
		},
		{
			name:  "model unavailable counts with empty scope (kind fallback)",
			class: errclass.Classification{Kind: errclass.KindModelUnavailable},
			want:  true,
		},
		{
			name:  "context window does not count",
			class: errclass.Classification{Kind: errclass.KindContextWindow, Scope: errclass.ScopeModel},
			want:  false,
		},
		{
			name:  "context window does not count with empty scope (kind fallback)",
			class: errclass.Classification{Kind: errclass.KindContextWindow},
			want:  false,
		},
		{
			name:  "rate limit does not count",
			class: errclass.Classification{Kind: errclass.KindRateLimit, Scope: errclass.ScopeAccount},
			want:  false,
		},
		{
			name:  "upstream 5xx does not count",
			class: errclass.Classification{Kind: errclass.KindUpstream, Scope: errclass.ScopeProvider},
			want:  false,
		},
		{
			name:  "auth does not count",
			class: errclass.Classification{Kind: errclass.KindAuth, Scope: errclass.ScopeAccount},
			want:  false,
		},
		{
			name:  "empty classification does not count",
			class: errclass.Classification{},
			want:  false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CountsTowardBreaker(tc.class); got != tc.want {
				t.Errorf("CountsTowardBreaker(%+v) = %v, want %v", tc.class, got, tc.want)
			}
		})
	}
}

// TestCountsTowardBreakerClassifyRoundTrip checks the verdict end to end
// from the raw upstream frame, the way modelBreakerFailure sees it: a
// context-window 400 must not charge, a model-not-found frame must.
func TestCountsTowardBreakerClassifyRoundTrip(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{
			name:   "context window 400 does not charge",
			status: 400,
			body:   `{"error":{"type":"invalid_request_error","message":"This model's maximum context length is 8192 tokens","code":"context_length_exceeded"}}`,
			want:   false,
		},
		{
			name:   "prompt too long 400 does not charge",
			status: 400,
			body:   `{"error":{"message":"prompt is too long"}}`,
			want:   false,
		},
		{
			name:   "model not found charges",
			status: 404,
			body:   `{"error":{"type":"model_not_found","message":"model foo/bar not found"}}`,
			want:   true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CountsTowardBreaker(Classify(tc.status, []byte(tc.body))); got != tc.want {
				t.Errorf("CountsTowardBreaker(Classify(%d, %s)) = %v, want %v", tc.status, tc.body, got, tc.want)
			}
		})
	}
}
