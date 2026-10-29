package executor

import (
	"context"
	"encoding/base64"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
)

// Ported from the JS intl executor's capture-verified spec:
// open-sse/executors/codebuddy-intl.js and its unit test. The gateway's channel
// verification refuses any chat request without the X-Conversation-* family
// (HTTP 400 code 11128 "conversation id missing"), and the identity has to be
// the WorkBuddy AI 5.8.4 desktop client — not the CN CLI.

var intlUUID7RE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func intlBaseHeaders() map[string]string {
	return map[string]string{}
}

func TestCodebuddyIntlHeaders_FreshPerRequestWithoutSession(t *testing.T) {
	cfg := &providers.ProviderConfig{BaseURL: "https://www.workbuddy.ai/v2/chat/completions"}
	body := []byte(`{"messages":[{"role":"user","content":"same opening"}]}`)

	a := map[string]string{}
	if err := codebuddyIntlHeaders(a, &Request{Body: body}, cfg); err != nil {
		t.Fatalf("headers: %v", err)
	}
	b := map[string]string{}
	if err := codebuddyIntlHeaders(b, &Request{Body: body}, cfg); err != nil {
		t.Fatalf("headers: %v", err)
	}

	// With no session to key on, every id is fresh. Deriving the conversation
	// id from the opening message made two unrelated sessions that start alike
	// share one id — an anomalous pattern the gateway can see.
	for _, h := range []string{codebuddyConvIDHeader, codebuddyConvRequestIDHeader, codebuddyConvMessageIDHeader, codebuddyRequestIDHeader} {
		if a[h] == b[h] {
			t.Errorf("%s repeated across requests: %q", h, a[h])
		}
	}
	if !intlUUID7RE.MatchString(a[codebuddyConvIDHeader]) {
		t.Errorf("%s = %q, want dashed UUIDv7", codebuddyConvIDHeader, a[codebuddyConvIDHeader])
	}
}

// The official CLI sends its session id as X-Conversation-ID
// (dist/codebuddy-lite-wb.mjs: M[CONVERSATION_ID_HEADER] = session.id), so one
// conversation keeps one id across turns while the per-request ids rotate.
func TestCodebuddyIntlHeaders_SessionKeepsOneConversation(t *testing.T) {
	cfg := &providers.ProviderConfig{BaseURL: "https://www.workbuddy.ai/v2/chat/completions"}
	body := []byte(`{"messages":[{"role":"user","content":"turn"}]}`)

	first := map[string]string{}
	if err := codebuddyIntlHeaders(first, &Request{SessionID: "sess-123", Body: body}, cfg); err != nil {
		t.Fatalf("headers: %v", err)
	}
	second := map[string]string{}
	if err := codebuddyIntlHeaders(second, &Request{SessionID: "sess-123", Body: body}, cfg); err != nil {
		t.Fatalf("headers: %v", err)
	}
	other := map[string]string{}
	if err := codebuddyIntlHeaders(other, &Request{SessionID: "sess-456", Body: body}, cfg); err != nil {
		t.Fatalf("headers: %v", err)
	}

	if first[codebuddyConvIDHeader] != second[codebuddyConvIDHeader] {
		t.Errorf("same session produced %q then %q", first[codebuddyConvIDHeader], second[codebuddyConvIDHeader])
	}
	if first[codebuddyConvIDHeader] == other[codebuddyConvIDHeader] {
		t.Errorf("different sessions shared %q", first[codebuddyConvIDHeader])
	}
	if first[codebuddyConvRequestIDHeader] == second[codebuddyConvRequestIDHeader] {
		t.Error("request id must stay fresh within one session")
	}
	if first[codebuddyRequestIDHeader] == second[codebuddyRequestIDHeader] {
		t.Error("request/message id must stay fresh within one session")
	}
	if !uuidRE.MatchString(first[codebuddyConvIDHeader]) {
		t.Errorf("session conversation id %q is not a UUID", first[codebuddyConvIDHeader])
	}
}

// A connection-supplied explicit id outranks the session derivation.
func TestCodebuddyIntlHeaders_ExplicitBeatsSession(t *testing.T) {
	cfg := &providers.ProviderConfig{BaseURL: "https://www.workbuddy.ai/v2/chat/completions"}
	// The connection's static headers reach the builder as the starting bag.
	h := map[string]string{codebuddyConvIDHeader: "caller-conversation"}
	if err := codebuddyIntlHeaders(h, &Request{SessionID: "sess-123"}, cfg); err != nil {
		t.Fatalf("headers: %v", err)
	}
	if h[codebuddyConvIDHeader] != "caller-conversation" {
		t.Errorf("%s = %q, want the caller's value", codebuddyConvIDHeader, h[codebuddyConvIDHeader])
	}
}

func TestCodebuddyIntlHeaders_ShapesAndRelationships(t *testing.T) {
	h := intlBaseHeaders()
	if err := applyCodebuddyConversationHeaders(h); err != nil {
		t.Fatalf("family: %v", err)
	}

	if !intlUUID7RE.MatchString(h[codebuddyConvIDHeader]) {
		t.Errorf("%s = %q, want dashed UUIDv7", codebuddyConvIDHeader, h[codebuddyConvIDHeader])
	}
	for _, name := range []string{codebuddyConvRequestIDHeader, codebuddyRootRequestIDHeader, codebuddyConvMessageIDHeader, codebuddyRequestIDHeader} {
		if !hex32RE.MatchString(h[name]) {
			t.Errorf("%s = %q, want 32 hex chars", name, h[name])
		}
	}
	if h[codebuddyRootRequestIDHeader] != h[codebuddyConvRequestIDHeader] {
		t.Errorf("%s must mirror %s: %q vs %q",
			codebuddyRootRequestIDHeader, codebuddyConvRequestIDHeader,
			h[codebuddyRootRequestIDHeader], h[codebuddyConvRequestIDHeader])
	}
	if h[codebuddyConvMessageIDHeader] != h[codebuddyRequestIDHeader] {
		t.Errorf("%s must mirror %s: %q vs %q",
			codebuddyConvMessageIDHeader, codebuddyRequestIDHeader,
			h[codebuddyConvMessageIDHeader], h[codebuddyRequestIDHeader])
	}
}

func TestCodebuddyIntlHeaders_TraceFamily(t *testing.T) {
	h := intlBaseHeaders()
	if err := applyCodebuddyConversationHeaders(h); err != nil {
		t.Fatalf("family: %v", err)
	}
	for _, name := range []string{"X-Trace-ID", "traceparent", "b3", "X-B3-TraceId", "X-B3-SpanId", "X-B3-Sampled"} {
		if h[name] == "" {
			t.Errorf("%s missing", name)
		}
	}
	if !regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-01$`).MatchString(h["traceparent"]) {
		t.Errorf("traceparent = %q", h["traceparent"])
	}
	if h["X-B3-TraceId"] != h["X-Trace-ID"] {
		t.Errorf("X-B3-TraceId %q != X-Trace-ID %q", h["X-B3-TraceId"], h["X-Trace-ID"])
	}
	if !strings.HasPrefix(h["b3"], h["X-Trace-ID"]+"-"+h["X-B3-SpanId"]+"-") {
		t.Errorf("b3 = %q does not carry trace/span", h["b3"])
	}
}

func TestCodebuddyIntlHeaders_PreservesCallerValues(t *testing.T) {
	h := map[string]string{
		codebuddyConvIDHeader:        "11111111-1111-1111-1111-111111111111",
		codebuddyConvMessageIDHeader: "fixed-message-id",
	}
	if err := applyCodebuddyConversationHeaders(h); err != nil {
		t.Fatalf("family: %v", err)
	}
	if h[codebuddyConvIDHeader] != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("caller conversation id overwritten: %q", h[codebuddyConvIDHeader])
	}
	if h[codebuddyConvMessageIDHeader] != "fixed-message-id" {
		t.Errorf("caller message id overwritten: %q", h[codebuddyConvMessageIDHeader])
	}
	if h[codebuddyRequestIDHeader] != "fixed-message-id" {
		t.Errorf("%s should mirror the caller's message id, got %q", codebuddyRequestIDHeader, h[codebuddyRequestIDHeader])
	}
	if !hex32RE.MatchString(h[codebuddyConvRequestIDHeader]) {
		t.Errorf("%s = %q, want a fresh 32-hex id", codebuddyConvRequestIDHeader, h[codebuddyConvRequestIDHeader])
	}
}

func TestCodebuddyIntlHeaders_BlankCountsAsAbsent(t *testing.T) {
	h := map[string]string{codebuddyConvIDHeader: "   "}
	if err := applyCodebuddyConversationHeaders(h); err != nil {
		t.Fatalf("family: %v", err)
	}
	if !intlUUID7RE.MatchString(h[codebuddyConvIDHeader]) {
		t.Errorf("blank conversation id not replaced: %q", h[codebuddyConvIDHeader])
	}
}

func TestCodebuddyIntlHeaders_CaseInsensitiveNoDuplicates(t *testing.T) {
	// HTTP header names are case-insensitive: testing the exact key would add a
	// second spelling and let the transport join both into one comma-separated
	// value, corrupting the id the caller supplied.
	h := map[string]string{
		"x-conversation-id":         "caller-lower",
		"X-Conversation-Message-Id": "caller-mixed",
		"x-request-id":              "caller-trace",
	}
	if err := applyCodebuddyConversationHeaders(h); err != nil {
		t.Fatalf("family: %v", err)
	}
	count := 0
	for k := range h {
		if strings.EqualFold(k, codebuddyConvIDHeader) {
			count++
		}
	}
	if count != 1 {
		t.Errorf("conversation id appears %d times, want 1", count)
	}
	if h["x-conversation-id"] != "caller-lower" {
		t.Errorf("lowercase caller id lost: %q", h["x-conversation-id"])
	}
	if h["X-Conversation-Message-Id"] != "caller-mixed" {
		t.Errorf("mixed-case caller message id lost: %q", h["X-Conversation-Message-Id"])
	}
	if h["x-request-id"] != "caller-trace" {
		t.Errorf("lowercase caller request id lost: %q", h["x-request-id"])
	}
}

func TestCodebuddyIntlHeaders_Idempotent(t *testing.T) {
	first := intlBaseHeaders()
	if err := applyCodebuddyConversationHeaders(first); err != nil {
		t.Fatalf("family: %v", err)
	}
	second := make(map[string]string, len(first))
	for k, v := range first {
		second[k] = v
	}
	if err := applyCodebuddyConversationHeaders(second); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	for k, v := range first {
		if second[k] != v {
			t.Errorf("%s rotated on second pass: %q -> %q", k, v, second[k])
		}
	}
}

func TestCodebuddyIntlHeaders_NoBodyStillEmitsFamily(t *testing.T) {
	h := intlBaseHeaders()
	if err := applyCodebuddyConversationHeaders(h); err != nil {
		t.Fatalf("family: %v", err)
	}
	if !intlUUID7RE.MatchString(h[codebuddyConvIDHeader]) {
		t.Errorf("no-body family produced %q", h[codebuddyConvIDHeader])
	}
}

// codebuddyHostRewriteTransport sends every request to the test server while leaving the
// URL's host intact, so the executor's channel detection (which keys on the
// base URL) still sees www.workbuddy.ai.
type codebuddyHostRewriteTransport struct {
	target *url.URL
}

func (rt codebuddyHostRewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	r2.URL.Scheme = rt.target.Scheme
	r2.URL.Host = rt.target.Host
	r2.Host = rt.target.Host
	return http.DefaultTransport.RoundTrip(r2)
}

func TestForwardCodebuddyIntl_WireHeaders(t *testing.T) {
	var captured *http.Request
	var capturedBody []byte

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("data: {\"id\":\"chatcmpl-intl\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer ts.Close()

	target, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}

	cfg := &providers.ProviderConfig{
		BaseURL: "https://www.workbuddy.ai/v2/chat/completions",
		StaticHeaders: map[string]string{
			"User-Agent":          "WorkBuddy/5.8.4 workbuddy-ai/5.8.4 CLI/2.161.0",
			"X-Domain":            "www.workbuddy.ai",
			"x-requested-with":    "XMLHttpRequest",
			"x-codebuddy-request": "1",
		},
	}

	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:      context.Background(),
		Client:   &http.Client{Transport: codebuddyHostRewriteTransport{target: target}},
		Config:   cfg,
		APIKey:   "test-token",
		IsStream: true,
		Body:     []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"ping"}]}`),
	}

	if err := ForwardCodebuddyCN(rec, req); err != nil {
		t.Fatalf("ForwardCodebuddyCN(intl) returned error: %v", err)
	}
	if captured == nil {
		t.Fatal("upstream request not captured")
	}

	if got := captured.Header.Get("User-Agent"); got != codebuddyIntlUserAgent {
		t.Errorf("User-Agent = %q, want %q", got, codebuddyIntlUserAgent)
	}
	if captured.Header.Get("X-IDE-Type") != "WorkBuddy" || captured.Header.Get("X-IDE-Name") != "WorkBuddy" || captured.Header.Get("X-IDE-Version") != "5.8.4" {
		t.Errorf("X-IDE-* = %q/%q/%q", captured.Header.Get("X-IDE-Type"), captured.Header.Get("X-IDE-Name"), captured.Header.Get("X-IDE-Version"))
	}
	if captured.Header.Get("X-Domain") != "www.workbuddy.ai" {
		t.Errorf("X-Domain = %q", captured.Header.Get("X-Domain"))
	}
	if captured.Header.Get("X-Private-Data") != "true" || captured.Header.Get("X-Agent-Type") != "main" {
		t.Errorf("X-Private-Data/X-Agent-Type = %q/%q", captured.Header.Get("X-Private-Data"), captured.Header.Get("X-Agent-Type"))
	}
	if captured.Header.Get("X-Stainless-Lang") != "" {
		t.Errorf("intl request carries a Stainless header: %q", captured.Header.Get("X-Stainless-Lang"))
	}
	for _, name := range []string{codebuddyConvIDHeader, codebuddyConvRequestIDHeader, codebuddyRootRequestIDHeader, codebuddyConvMessageIDHeader, "X-Trace-ID", "traceparent", "b3"} {
		if captured.Header.Get(name) == "" {
			t.Errorf("%s did not reach the wire", name)
		}
	}
	if !intlUUID7RE.MatchString(captured.Header.Get(codebuddyConvIDHeader)) {
		t.Errorf("%s on the wire = %q", codebuddyConvIDHeader, captured.Header.Get(codebuddyConvIDHeader))
	}

	var sent map[string]any
	if err := json.Unmarshal(capturedBody, &sent); err != nil {
		t.Fatalf("decode forwarded body: %v", err)
	}
	if sent["stream"] != true {
		t.Errorf("stream = %v, want forced true", sent["stream"])
	}
	msgs, _ := sent["messages"].([]any)
	if len(msgs) < 2 {
		t.Fatalf("messages = %v, want a leading system prompt plus the user turn", msgs)
	}
	first, _ := msgs[0].(map[string]any)
	if first["role"] != "system" || first["content"] != codebuddySystemPrompt {
		t.Errorf("leading message = %v, want the CodeBuddy system prompt", first)
	}
}

// An edge relay rewrites BaseURL to the relay URL and keeps the real target only
// in x-relay-target, so channel selection must not depend on BaseURL: a relayed
// intl connection was routed as CN, losing the WorkBuddy identity and the family
// the gateway refuses without.
func TestForwardCodebuddy_RelayKeepsChannel(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider string
		target   string
		wantUA   string
		wantFam  bool
	}{
		{"intl relay", "codebuddy-intl", "https://www.workbuddy.ai", codebuddyIntlUserAgent, true},
		{"intl alias relay", "cbai", "https://www.workbuddy.ai", codebuddyIntlUserAgent, true},
		{"cn relay", "codebuddy-cn", "https://copilot.tencent.com", "CLI/2.108.1 CodeBuddy/2.108.1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var captured *http.Request
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				captured = r
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("data: {\"id\":\"x\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
			}))
			defer ts.Close()

			target, err := url.Parse(ts.URL)
			if err != nil {
				t.Fatalf("parse test server URL: %v", err)
			}
			// The relay rewrites BaseURL and records the real target in the header.
			cfg := &providers.ProviderConfig{
				BaseURL: "https://relay.example.test/edge",
				StaticHeaders: map[string]string{
					"x-relay-target": tc.target,
					"x-relay-path":   "/v2/chat/completions",
				},
			}
			req := &Request{
				Ctx:      context.Background(),
				Client:   &http.Client{Transport: codebuddyHostRewriteTransport{target: target}},
				Config:   cfg,
				Provider: tc.provider,
				APIKey:   "test-token",
				IsStream: true,
				Body:     []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"ping"}]}`),
			}
			if err := ForwardCodebuddyCN(httptest.NewRecorder(), req); err != nil {
				t.Fatalf("forward: %v", err)
			}
			if captured == nil {
				t.Fatal("upstream request not captured")
			}
			if got := captured.Header.Get("User-Agent"); got != tc.wantUA {
				t.Errorf("User-Agent = %q, want %q", got, tc.wantUA)
			}
			hasFamily := captured.Header.Get("X-Conversation-ID") != "" && captured.Header.Get("traceparent") != ""
			if hasFamily != tc.wantFam {
				t.Errorf("family present = %v, want %v", hasFamily, tc.wantFam)
			}
			// The relay's own routing headers must survive the merge.
			if captured.Header.Get("x-relay-target") != tc.target {
				t.Errorf("x-relay-target = %q, want %q", captured.Header.Get("x-relay-target"), tc.target)
			}
		})
	}
}

func syntheticJWT(sub string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"` + sub + `"}`))
	return header + "." + payload + ".signature"
}

// The desktop client sends the product version, the model it is asking for and
// the signed-in account beside the rest of the identity. Their absence is a
// mismatch a risk engine can score, so they ride along when they can be known —
// and are omitted, never blanked, when they cannot.
func TestCodebuddyIntlHeaders_ProductModelAndUser(t *testing.T) {
	cfg := &providers.ProviderConfig{BaseURL: "https://www.workbuddy.ai/v2/chat/completions"}
	req := &Request{ModelName: "glm-5.3", APIKey: syntheticJWT("afa74f65-b463-4581-a913-d583913dbb21")}

	bag := map[string]string{}
	if err := codebuddyIntlHeaders(bag, req, cfg); err != nil {
		t.Fatalf("headers: %v", err)
	}
	if got := bag["X-Product-Version"]; got != codebuddyIntlIDEVer {
		t.Errorf("X-Product-Version = %q, want %q", got, codebuddyIntlIDEVer)
	}
	if got := bag["X-Model-ID"]; got != "glm-5.3" {
		t.Errorf("X-Model-ID = %q, want the requested model", got)
	}
	if got := bag["X-User-Id"]; got != "afa74f65-b463-4581-a913-d583913dbb21" {
		t.Errorf("X-User-Id = %q, want the token subject", got)
	}

	// A plain API key and no model name: both headers must be absent, not empty.
	bare := map[string]string{}
	if err := codebuddyIntlHeaders(bare, &Request{APIKey: "sr_plain"}, cfg); err != nil {
		t.Fatalf("headers: %v", err)
	}
	for _, name := range []string{"X-Model-ID", "X-User-Id"} {
		if _, ok := bare[name]; ok {
			t.Errorf("%s must be omitted when it cannot be known, got %q", name, bare[name])
		}
	}
}
