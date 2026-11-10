package errclass

import (
	"testing"
	"time"
)

func TestBodyErrorCode(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{"gemini resource exhausted", `{"error":{"code":429,"message":"Resource exhausted","status":"RESOURCE_EXHAUSTED"}}`, 429},
		{"string code 429", `{"error":{"code":"429","message":"x"}}`, 429},
		{"status resource exhausted", `{"error":{"status":"RESOURCE_EXHAUSTED","message":"x"}}`, 429},
		{"status unavailable", `{"error":{"status":"UNAVAILABLE"}}`, 503},
		{"status deadline exceeded", `{"error":{"status":"DEADLINE_EXCEEDED"}}`, 504},
		{"top-level code 503", `{"code":503}`, 503},
		{"non-numeric code", `{"error":{"code":"insufficient_quota"}}`, 0},
		{"out-of-range number", `{"error":{"code":6000}}`, 0},
		{"not json", `not json`, 0},
		{"empty", ``, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := BodyErrorCode([]byte(tc.body)); got != tc.want {
				t.Errorf("BodyErrorCode(%q) = %d, want %d", tc.body, got, tc.want)
			}
		})
	}
}

func TestLooksLikeRateLimitWrapped(t *testing.T) {
	positiveBodies := []string{
		`{"error":{"code":429,"message":"Resource exhausted"}}`,
		`{"error":{"message":"Request too large for gpt-4o on tokens per min (TPM)"}}`,
		`{"error":{"message":"service tier capacity exceeded"}}`,
		`{"error":{"message":"quota exceeded"}}`,
		`{"error":{"status":"RESOURCE_EXHAUSTED"}}`,
	}
	for _, body := range positiveBodies {
		if !LooksLikeRateLimitWrapped([]byte(body)) {
			t.Errorf("LooksLikeRateLimitWrapped(%q) = false, want true", body)
		}
	}

	negativeBodies := []string{
		`{"error":{"message":"internal error"}}`,
		`{"error":{"message":"not found"}}`,
	}
	for _, body := range negativeBodies {
		if LooksLikeRateLimitWrapped([]byte(body)) {
			t.Errorf("LooksLikeRateLimitWrapped(%q) = true, want false", body)
		}
	}
}

func TestCapRetryAfter(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"short wait unchanged", 30 * time.Second, 30 * time.Second},
		{"exactly the cap unchanged", MaxRetryAfterHeader, MaxRetryAfterHeader},
		{"above the cap clamped to the cap", 24 * time.Hour, MaxRetryAfterHeader},
		{"zero unchanged", 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CapRetryAfter(tc.in); got != tc.want {
				t.Errorf("CapRetryAfter(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestStreamErrorFrame(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		kind    Kind
		scope   Scope
		status  int
	}{
		{"anthropic overloaded", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, KindUpstream, ScopeProvider, 529},
		{"anthropic rate limit", `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`, KindRateLimit, ScopeAccount, 429},
		{"openai code 429", `{"error":{"message":"Rate limit reached","type":"tokens","code":"429"}}`, KindRateLimit, ScopeAccount, 429},
		{"openai context", `{"error":{"message":"This model's maximum context length is 8192 tokens","type":"invalid_request_error","code":"context_length_exceeded"}}`, KindContextWindow, ScopeModel, 0},
		{"gemini resource exhausted", `{"error":{"code":429,"message":"Resource has been exhausted","status":"RESOURCE_EXHAUSTED"}}`, KindRateLimit, ScopeAccount, 429},
		{"gemini unavailable", `{"error":{"code":503,"message":"The model is overloaded.","status":"UNAVAILABLE"}}`, KindUpstream, ScopeProvider, 503},
		{"string error model gone", `{"error":"model not found: foo"}`, KindModelUnavailable, ScopeModel, 0},
		{"auth", `{"error":{"message":"invalid x-api-key","type":"authentication_error"}}`, KindAuth, ScopeAccount, 0},
		{"auth message only, no type or status", `{"error":{"message":"Invalid API key provided."}}`, KindAuth, ScopeAccount, 0},
		{"content filter", `{"error":{"message":"Your request was rejected as a result of our safety system"}}`, KindContentFilter, ScopeRequest, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, ok := StreamErrorFrame([]byte(tc.payload))
			if !ok {
				t.Fatalf("StreamErrorFrame(%q) = not-found, want a classification", tc.payload)
			}
			if c.Kind != tc.kind {
				t.Errorf("Kind = %q, want %q", c.Kind, tc.kind)
			}
			if c.EffectiveScope() != tc.scope {
				t.Errorf("Scope = %q, want %q", c.EffectiveScope(), tc.scope)
			}
			if c.StatusCode != tc.status {
				t.Errorf("StatusCode = %d, want %d", c.StatusCode, tc.status)
			}
		})
	}

	notErrorPayloads := []string{
		`{"choices":[{"delta":{"content":"no error here"}}]}`,
		`{"type":"message_start","message":{"usage":{"input_tokens":1}}}`,
		`{"error":null,"choices":[]}`,
		`{"type":"response.failed","error":{"message":"handled by its codec"}}`,
		`[DONE]`,
		``,
		`{}`,
	}
	for _, payload := range notErrorPayloads {
		if _, ok := StreamErrorFrame([]byte(payload)); ok {
			t.Errorf("StreamErrorFrame(%q) = found, want not-found", payload)
		}
	}
}

// TestClassifyDetailPrecedence pins the precedence rules on plain
// status+body input, independent of the stream decode: type wins over message,
// message wins over status, and a bare status falls through to the status map.
func TestClassifyDetailPrecedence(t *testing.T) {
	cases := []struct {
		name       string
		typeArg    string
		code       int
		status     string
		message    string
		wantKind   Kind
		wantScope  Scope
		wantStatus int
	}{
		{"type beats message", "rate_limit_error", 503, "", "model not found", KindRateLimit, ScopeAccount, 503},
		{"overloaded type carries no code gets 529", "overloaded_error", 0, "", "", KindUpstream, ScopeProvider, 529},
		{"message beats status", "", 400, "", "your request was rejected as a result of our safety system", KindContentFilter, ScopeRequest, 400},
		{"context window message beats 400", "", 400, "", "prompt is too long", KindContextWindow, ScopeModel, 400},
		{"auth message only, no type or status", "", 0, "", "Invalid API key provided.", KindAuth, ScopeAccount, 0},
		{"auth message beats a misfiring 429", "", 429, "", "invalid api key", KindAuth, ScopeAccount, 429},
		{"status-only 429", "", 429, "", "", KindRateLimit, ScopeAccount, 429},
		{"status-only 404", "", 404, "", "", KindModelUnavailable, ScopeModel, 404},
		{"status-only 504", "", 504, "", "", KindTimeout, ScopeProvider, 504},
		{"status-only 400", "", 400, "", "", KindBadRequest, ScopeRequest, 400},
		{"status-only 503", "", 503, "", "", KindUpstream, ScopeProvider, 503},
		{"resource exhausted status to 429", "", 0, "RESOURCE_EXHAUSTED", "", KindRateLimit, ScopeAccount, 429},
		{"unknown 5xx is upstream", "", 500, "", "", KindUpstream, ScopeProvider, 500},
		{"type beats message and status", "billing_error", 429, "RESOURCE_EXHAUSTED", "rate limit reached", KindQuotaExhausted, ScopeAccount, 429},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := ClassifyDetail(tc.typeArg, tc.code, tc.status, tc.message)
			if c.Kind != tc.wantKind {
				t.Errorf("Kind = %q, want %q", c.Kind, tc.wantKind)
			}
			if c.EffectiveScope() != tc.wantScope {
				t.Errorf("Scope = %q, want %q", c.EffectiveScope(), tc.wantScope)
			}
			if c.StatusCode != tc.wantStatus {
				t.Errorf("StatusCode = %d, want %d", c.StatusCode, tc.wantStatus)
			}
		})
	}
}

func TestEffectiveScopeDefaults(t *testing.T) {
	// A hand-built Classification with an empty Scope still needs a routing
	// answer; EffectiveScope fills in the conservative default per kind.
	cases := []struct {
		kind Kind
		want Scope
	}{
		{KindModelUnavailable, ScopeModel},
		{KindContextWindow, ScopeModel},
		{KindAuth, ScopeAccount},
		{KindRateLimit, ScopeAccount},
		{KindQuotaExhausted, ScopeAccount},
		{KindUpstream, ScopeProvider},
		{KindTimeout, ScopeProvider},
		{KindBadRequest, ScopeRequest},
		{KindContentFilter, ScopeRequest},
		{"", ScopeRequest},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			if got := (Classification{Kind: tc.kind}).EffectiveScope(); got != tc.want {
				t.Errorf("EffectiveScope(%q) = %q, want %q", tc.kind, got, tc.want)
			}
		})
	}
	// An explicit Scope always wins over the derived default.
	if got := (Classification{Kind: KindAuth, Scope: ScopeModel}).EffectiveScope(); got != ScopeModel {
		t.Errorf("explicit Scope = %q, want %q", got, ScopeModel)
	}
}
