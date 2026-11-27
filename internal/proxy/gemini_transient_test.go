package proxy

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/translator"
)

func TestForwardGemini_TransportTimeout_IsRetryable(t *testing.T) {
	tests := []struct {
		name    string
		isError func(err error) bool
		// wantRetryable: true means ForwardGemini must return UpstreamError 502
		wantRetryable bool
	}{
		{
			name: "ResponseHeaderTimeout classifies as retryable 502",
			isError: func(err error) bool {
				var ue *UpstreamError
				return errors.As(err, &ue) && ue.StatusCode == http.StatusBadGateway
			},
			wantRetryable: true,
		},
	}

	// Subtests exercise the classifier directly — the httptest server below
	// covers the ForwardGemini wiring, because a live ResponseHeaderTimeout
	// server is timing-sensitive and flaky.
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cases := []struct {
				err  error
				want bool
			}{
				{err: fmt.Errorf("Post %q: http2: timeout awaiting response headers", "https://daily-cloudcode-pa.googleapis.com/v1internal:generateContent"), want: true},
				{err: fmt.Errorf("timeout awaiting response headers"), want: true},
				{err: context.DeadlineExceeded, want: true},
				{err: &url.Error{Op: "Post", URL: "https://x", Err: fmt.Errorf("timeout")}, want: false}, // bare "timeout" without net timeout opt-in
				{err: fmt.Errorf("EOF"), want: true},
				{err: fmt.Errorf("connection reset by peer"), want: true},
				{err: fmt.Errorf("dial tcp: connection refused"), want: true},
				{err: fmt.Errorf("unknown error"), want: false},
				{err: nil, want: false},
			}
			for i, c := range cases {
				if got := isTransientGeminiTransportError(c.err); got != c.want {
					t.Errorf("case %d isTransientGeminiTransportError(%v) = %v, want %v", i, c.err, got, c.want)
				}
			}
		})
	}
}

func TestForwardGemini_HeaderTimeout_ReturnsRetryable502(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		// Never write headers within ResponseHeaderTimeout — let the client timer fire.
		// Unblock on client close OR a bounded sleep so srv.Close() can return
		// (HTTP/1 server does not cancel r.Context() on client timeout alone).
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()

	transport := &http.Transport{
		ResponseHeaderTimeout: 50_000_000, // 50ms — keep the test fast
		ForceAttemptHTTP2:     true,
	}
	client := &http.Client{Transport: transport}

	cfg := &providers.ProviderConfig{BaseURL: srv.URL}
	body := `{"model":"gemini-3.8-flash-medium","messages":[{"role":"user","content":"hi"}]}`
	// projectID non-empty routes through WrapForAntigravity, which needs valid JSON.
	// If translation fails the call returns before dialing, so pin the body to the
	// OpenAI shape TranslateOpenAIToGeminiWithSession accepts.

	_, err := ForwardGemini(context.Background(), client, cfg, "ya29.fake", body, false, "proj-1", "gemini-3.8-flash-medium", "")
	if err == nil {
		t.Fatal("ForwardGemini() error = nil, want retryable 502")
	}
	var ue *UpstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("ForwardGemini() error type = %T (%v), want *UpstreamError", err, err)
	}
	if ue.StatusCode != http.StatusBadGateway {
		t.Errorf("ForwardGemini() status = %d, want 502", ue.StatusCode)
	}
	if !strings.Contains(string(ue.Body), "ForwardGemini antigravity transport") {
		t.Errorf("ForwardGemini() body = %s, want transport marker", string(ue.Body))
	}
	// hits may be 0-1 depending on whether the transport dialed before the timer;
	// the assertion that matters is the error classification, not the count.
}

func TestForwardGemini_ClientCancelled_StaysTerminal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &http.Transport{ForceAttemptHTTP2: true}}
	cfg := &providers.ProviderConfig{BaseURL: srv.URL}
	body := `{"model":"gemini-3.8-flash-medium","messages":[{"role":"user","content":"hi"}]}`

	_, err := ForwardGemini(ctx, client, cfg, "ya29.fake", body, false, "proj-1", "gemini-3.8-flash-medium", "")
	if err == nil {
		t.Fatal("ForwardGemini() error = nil, want context error")
	}
	var ue *UpstreamError
	if errors.As(err, &ue) {
		t.Errorf("ForwardGemini() cancelled error must stay unwrapped (not UpstreamError), got %T: %v", err, err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("ForwardGemini() error = %v, want context.Canceled", err)
	}
}

// TestForwardGemini_Bare429RetriesWithMovedSystemInstruction pins the 1-hop
// fallback retry: when the Antigravity backend answers a bare 429
// RESOURCE_EXHAUSTED (no QUOTA_EXHAUSTED / RATE_LIMIT_EXCEEDED / "Individual
// quota reached" marker), ForwardGemini retries once with the
// systemInstruction folded into contents[0]. If the retry also fails, the
// original 429 is returned so the combo loop can rotate the connection
// (local hardening).
func TestForwardGemini_Bare429RetriesWithMovedSystemInstruction(t *testing.T) {
	var hits atomic.Int64
	var secondBody atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		body, _ := io.ReadAll(r.Body)
		if n == 1 {
			// First request: bare 429 RESOURCE_EXHAUSTED.
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"code":429,"message":"Resource exhausted","status":"RESOURCE_EXHAUSTED"}}`)
			return
		}
		// Second request (retry): save body for inspection, then succeed.
		secondBody.Store(string(body))
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`)
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{BaseURL: srv.URL}
	body := `{"model":"gemini-3-flash","messages":[{"role":"system","content":"You are a helpful assistant."},{"role":"user","content":"hello"}]}`

	resp, err := ForwardGemini(context.Background(), srv.Client(), cfg, "ya29.fake", body, false, "proj-1", "gemini-3-flash", "")
	if err != nil {
		t.Fatalf("ForwardGemini() error = %v, want nil", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("ForwardGemini() status = %d, want 200", resp.StatusCode)
	}
	if hits.Load() != 2 {
		t.Errorf("ForwardGemini() made %d requests, want 2 (original + retry)", hits.Load())
	}

	// Verify the retry body has no system_instruction and the system prompt
	// is in contents[0]. The retry body is wrapped in an Antigravity envelope
	// ({project, model, request: {geminiRequest}}), so unwrap it first.
	retryBody := secondBody.Load().(string)
	var envelope struct {
		Request jsontext.Value `json:"request"`
	}
	if jsonErr := json.Unmarshal([]byte(retryBody), &envelope); jsonErr != nil {
		t.Fatalf("unmarshal retry envelope: %v", jsonErr)
	}
	if len(envelope.Request) == 0 {
		t.Fatal("retry body has no request field (expected Antigravity envelope)")
	}
	var m map[string]any
	if jsonErr := json.Unmarshal([]byte(envelope.Request), &m); jsonErr != nil {
		t.Fatalf("unmarshal retry inner request: %v", jsonErr)
	}
	if si, ok := m["system_instruction"]; ok && si != nil {
		if siMap, ok := si.(map[string]any); ok && len(siMap) > 0 {
			t.Errorf("retry body still has system_instruction: %v", si)
		}
	}
	contents, _ := m["contents"].([]any)
	if len(contents) == 0 {
		t.Fatal("retry body contents is empty")
	}
	first, _ := contents[0].(map[string]any)
	if first == nil {
		t.Fatal("retry body contents[0] is not an object")
	}
	if role, _ := first["role"].(string); role != "user" {
		t.Errorf("retry body contents[0].role = %q, want user", role)
	}
	parts, _ := first["parts"].([]any)
	if len(parts) == 0 {
		t.Fatal("retry body contents[0].parts is empty")
	}
	firstPart, _ := parts[0].(map[string]any)
	if firstPart == nil {
		t.Fatal("retry body contents[0].parts[0] is not an object")
	}
	if text, _ := firstPart["text"].(string); !strings.Contains(text, "helpful assistant") {
		t.Errorf("retry body contents[0].parts[0].text = %q, want it to contain the system prompt", text)
	}
}

// TestForwardGemini_ExplicitQuota429DoesNotRetry pins the gate: a 429 with
// QUOTA_EXHAUSTED details is a real quota error, not a content rejection,
// so ForwardGemini must NOT retry with the moved systemInstruction
// (local hardening).
func TestForwardGemini_ExplicitQuota429DoesNotRetry(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"code":429,"message":"Quota exhausted","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED"}]}}`)
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{BaseURL: srv.URL}
	body := `{"model":"gemini-3-flash","messages":[{"role":"system","content":"sys"},{"role":"user","content":"hello"}]}`

	resp, err := ForwardGemini(context.Background(), srv.Client(), cfg, "ya29.fake", body, false, "proj-1", "gemini-3-flash", "")
	if err == nil {
		resp.Body.Close()
		t.Fatal("ForwardGemini() error = nil, want UpstreamError")
	}
	var ue *UpstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("ForwardGemini() error type = %T, want *UpstreamError", err)
	}
	if ue.StatusCode != http.StatusTooManyRequests {
		t.Errorf("ForwardGemini() status = %d, want 429", ue.StatusCode)
	}
	if hits.Load() != 1 {
		t.Errorf("ForwardGemini() made %d requests, want 1 (no retry for explicit quota)", hits.Load())
	}
}

// TestForwardGemini_Bare429RetryFails_ReturnsOriginal429 pins the fallback:
// when the retry also fails, the original 429 is returned so the combo loop
// can rotate the connection (local hardening).
// TestForwardGemini_Bare429RetryCarriesAPIKeyID pins that the bare-429 retry
// translates the body with the caller's apiKeyID. Without it the retry reads
// the unscoped signature keys, so a bare-key request (no session header) loses
// the thought signature it stored on the previous turn and the retried body
// falls back to the generic default instead.
func TestForwardGemini_Bare429RetryCarriesAPIKeyID(t *testing.T) {
	const (
		apiKeyID   = "proxy-test-key-bare429"
		toolCallID = "call_bare429_proxy_test"
		signature  = "c2lnLWZyb20tdGhlLXN0b3Jl"
	)
	translator.StoreGeminiThoughtSignature(toolCallID, signature, "", "gemini-3-flash", apiKeyID)

	var secondBody atomic.Value
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		body, _ := io.ReadAll(r.Body)
		if n == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"code":429,"message":"Resource exhausted","status":"RESOURCE_EXHAUSTED"}}`)
			return
		}
		secondBody.Store(string(body))
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`)
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{BaseURL: srv.URL}
	body := `{"model":"gemini-3-flash","messages":[` +
		`{"role":"system","content":"You are a helpful assistant."},` +
		`{"role":"user","content":"list files"},` +
		`{"role":"assistant","content":"","tool_calls":[{"id":"` + toolCallID + `","type":"function","function":{"name":"bash","arguments":"{}"}}]},` +
		`{"role":"tool","tool_call_id":"` + toolCallID + `","content":"a.txt"}]}`

	resp, err := ForwardGemini(context.Background(), srv.Client(), cfg, "ya29.fake", body, false, "proj-1", "gemini-3-flash", apiKeyID)
	if err != nil {
		t.Fatalf("ForwardGemini() error = %v, want nil", err)
	}
	defer resp.Body.Close()
	if hits.Load() != 2 {
		t.Fatalf("ForwardGemini() made %d requests, want 2 (original + retry)", hits.Load())
	}

	var envelope struct {
		Request jsontext.Value `json:"request"`
	}
	if jsonErr := json.Unmarshal([]byte(secondBody.Load().(string)), &envelope); jsonErr != nil {
		t.Fatalf("unmarshal retry envelope: %v", jsonErr)
	}
	// The signature rides on the PART, beside functionCall, not inside it.
	var inner struct {
		Contents []struct {
			Parts []struct {
				ThoughtSignature string `json:"thoughtSignature"`
				FunctionCall     *struct {
					Name string `json:"name"`
				} `json:"functionCall"`
			} `json:"parts"`
		} `json:"contents"`
	}
	if jsonErr := json.Unmarshal([]byte(envelope.Request), &inner); jsonErr != nil {
		t.Fatalf("unmarshal retry request: %v", jsonErr)
	}
	got := ""
	for _, c := range inner.Contents {
		for _, p := range c.Parts {
			if p.FunctionCall != nil && p.ThoughtSignature != "" {
				got = p.ThoughtSignature
			}
		}
	}
	if got != signature {
		t.Errorf("retry functionCall thoughtSignature = %q, want the stored %q (the apiKeyID was not threaded into the retry)", got, signature)
	}
}

func TestForwardGemini_Bare429RetryFails_ReturnsOriginal429(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"code":429,"message":"Resource exhausted","status":"RESOURCE_EXHAUSTED"}}`)
			return
		}
		// Retry also fails with 429.
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"code":429,"message":"Resource exhausted","status":"RESOURCE_EXHAUSTED"}}`)
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{BaseURL: srv.URL}
	body := `{"model":"gemini-3-flash","messages":[{"role":"system","content":"sys"},{"role":"user","content":"hello"}]}`

	resp, err := ForwardGemini(context.Background(), srv.Client(), cfg, "ya29.fake", body, false, "proj-1", "gemini-3-flash", "")
	if err == nil {
		resp.Body.Close()
		t.Fatal("ForwardGemini() error = nil, want UpstreamError")
	}
	var ue *UpstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("ForwardGemini() error type = %T, want *UpstreamError", err)
	}
	if ue.StatusCode != http.StatusTooManyRequests {
		t.Errorf("ForwardGemini() status = %d, want 429 (original)", ue.StatusCode)
	}
	if hits.Load() != 2 {
		t.Errorf("ForwardGemini() made %d requests, want 2 (original + retry)", hits.Load())
	}
}

// TestForwardGemini_GeminiCLIDoesNotTriggerAntigravity429Retry verifies that
// gemini-cli format connections skip the bare-429 retry path. The retry is
// Antigravity-specific (WrapForAntigravity), so a gemini-cli connection must
// return the 429 directly instead of attempting the moved-systemInstruction
// retry.
func TestForwardGemini_GeminiCLIDoesNotTriggerAntigravity429Retry(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"code":429,"message":"Resource exhausted","status":"RESOURCE_EXHAUSTED"}}`)
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{BaseURL: srv.URL + "/v1internal", Format: "gemini-cli"}
	body := `{"model":"gemini-3-flash","messages":[{"role":"system","content":"sys"},{"role":"user","content":"hello"}]}`

	resp, err := ForwardGemini(context.Background(), srv.Client(), cfg, "ya29.fake", body, false, "proj-1", "gemini-3-flash", "")
	if err == nil {
		resp.Body.Close()
		t.Fatal("ForwardGemini() error = nil, want UpstreamError")
	}
	var ue *UpstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("ForwardGemini() error type = %T, want *UpstreamError", err)
	}
	if ue.StatusCode != http.StatusTooManyRequests {
		t.Errorf("ForwardGemini() status = %d, want 429", ue.StatusCode)
	}
	if hits.Load() != 1 {
		t.Errorf("ForwardGemini() made %d requests, want 1 (no retry for gemini-cli)", hits.Load())
	}
}
