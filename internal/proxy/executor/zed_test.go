package executor

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/providers"
)

// zedTestJWT builds an unsigned JWT whose payload carries the given exp claim.
// The gateway never verifies the signature, so the payload is all the expiry
// logic under test can read.
func zedTestJWT(t *testing.T, exp int64) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"exp": exp})
	if err != nil {
		t.Fatalf("marshal jwt payload: %v", err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"HS256"}`)) + "." + enc(payload) + ".sig"
}

// zedProbe is a fake Zed cloud: it mints LLM tokens, records the shape of both
// auth layers, and answers /completions from a scripted responder.
type zedProbe struct {
	t   *testing.T
	srv *httptest.Server

	mu sync.Mutex

	tokenCalls int
	tokenPath  string
	tokenQuery string
	tokenMeth  string
	tokenAuth  string
	tokenSys   string
	tokenBody  map[string]any

	completionCalls int
	completionAuth  []string
	completionSys   string
	completionAcc   string
	envelope        map[string]any

	// reply answers /completions. nil means a plain 200 SSE stream.
	reply func(w http.ResponseWriter, attempt int)
}

func newZedProbe(t *testing.T, reply func(w http.ResponseWriter, attempt int)) *zedProbe {
	t.Helper()
	p := &zedProbe{t: t, reply: reply}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The handler runs on the server's own goroutine, so state is recorded
		// under the lock and the response is written after releasing it: a
		// slow client must never hold the lock the test needs to read state.
		var (
			tokenBody []byte
			envelope  map[string]any
			attempt   int
			path      = r.URL.Path
		)
		raw, _ := io.ReadAll(r.Body)

		p.mu.Lock()
		switch path {
		case providers.ZedLLMTokensPath:
			p.tokenCalls++
			p.tokenPath = path
			p.tokenQuery = r.URL.RawQuery
			p.tokenMeth = r.Method
			p.tokenAuth = r.Header.Get("Authorization")
			p.tokenSys = r.Header.Get(providers.ZedSystemIDHeader)
			tokenBody = raw
			_ = json.Unmarshal(raw, &p.tokenBody)
		case providers.ZedCompletionsPath:
			p.completionCalls++
			attempt = p.completionCalls
			p.completionAuth = append(p.completionAuth, r.Header.Get("Authorization"))
			p.completionSys = r.Header.Get(providers.ZedSystemIDHeader)
			p.completionAcc = r.Header.Get("Accept")
			p.envelope = nil
			_ = json.Unmarshal(raw, &envelope)
			p.envelope = envelope
		}
		p.mu.Unlock()

		switch path {
		case providers.ZedLLMTokensPath:
			_ = tokenBody
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"` + zedTestJWT(t, time.Now().Add(2*time.Hour).Unix()) + `"}`))
		case providers.ZedCompletionsPath:
			if p.reply != nil {
				p.reply(w, attempt)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(zedClaudeSSE))
		default:
			http.Error(w, "unexpected upstream path", http.StatusNotFound)
		}
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *zedProbe) config() *providers.ProviderConfig {
	return &providers.ProviderConfig{
		BaseURL:    p.srv.URL + providers.ZedCompletionsPath,
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	}
}

// zedClaudeSSE is a minimal but real-shaped Anthropic Messages stream.
const zedClaudeSSE = `event: message_start
data: {"type":"message_start","message":{"id":"msg_zed","type":"message","role":"assistant","model":"claude-haiku-5-5","content":[],"usage":{"input_tokens":7,"output_tokens":1}}}

data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello from Zed"}}

data: {"type":"content_block_stop","index":0}

data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}

data: {"type":"message_stop"}

`

// zedChatBody is a plain OpenAI chat-completions request.
const zedChatBody = `{"model":"claude-haiku-5-5","stream":true,"max_tokens":64,
 "messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hi"}]}`

// zedTestCreds returns credentials unique to one test, so the process-wide
// token cache cannot leak a token across tests.
func zedTestCreds(tag string) (blob string, connData map[string]any) {
	return `{"version":2,"id":"client_token_` + tag + `","token":"acct-` + tag + `"}`,
		map[string]any{"userId": "493663", "organizationId": "org_" + tag, "systemId": "sys_" + tag}
}

func TestResolveZedModel(t *testing.T) {
	tests := []struct {
		name      string
		requested string
		overrides ZedModelOverrides
		want      ZedWireModel
		wantErr   bool
	}{
		{name: "claude prefix", requested: "claude-haiku-5-5", want: ZedWireModel{Provider: "anthropic", Model: "claude-haiku-5-5"}},
		{name: "claude uppercase", requested: "CLAUDE-SONNET-5-5", want: ZedWireModel{Provider: "anthropic", Model: "CLAUDE-SONNET-5-5"}},
		{name: "gpt prefix", requested: "gpt-5.6-luna", want: ZedWireModel{Provider: "open_ai", Model: "gpt-5.6-luna"}},
		{name: "o-series", requested: "o3-mini", want: ZedWireModel{Provider: "open_ai", Model: "o3-mini"}},
		{name: "provider prefix stripped", requested: "zed/claude-haiku-5-5", want: ZedWireModel{Provider: "anthropic", Model: "claude-haiku-5-5"}},
		{name: "unknown model", requested: "llama-9", wantErr: true},
		{name: "empty model", requested: "", wantErr: true},
		{
			name:      "override pins provider",
			requested: "gpt-5.6-luna",
			overrides: ZedModelOverrides{"gpt-5.6-luna": {Provider: "anthropic"}},
			want:      ZedWireModel{Provider: "anthropic", Model: "gpt-5.6-luna"},
		},
		{
			name:      "override remaps model",
			requested: "claude-haiku-5-5",
			overrides: ZedModelOverrides{"claude-haiku-5-5": {Provider: "anthropic", Model: "claude-haiku-5-6"}},
			want:      ZedWireModel{Provider: "anthropic", Model: "claude-haiku-5-6"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveZedModel(tt.requested, tt.overrides)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ResolveZedModel(%q) = %+v, want error", tt.requested, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveZedModel(%q): %v", tt.requested, err)
			}
			if got != tt.want {
				t.Errorf("ResolveZedModel(%q) = %+v, want %+v", tt.requested, got, tt.want)
			}
		})
	}
}

func TestBuildZedCompletionBodyAnthropic(t *testing.T) {
	body, err := BuildZedCompletionBody([]byte(zedChatBody), ZedWireModel{Provider: "anthropic", Model: "claude-haiku-5-5"}, "thread-1")
	if err != nil {
		t.Fatalf("BuildZedCompletionBody: %v", err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if got := envelope["provider"]; got != "anthropic" {
		t.Errorf("provider = %v, want anthropic", got)
	}
	if got := envelope["model"]; got != "claude-haiku-5-5" {
		t.Errorf("model = %v, want claude-haiku-5-5", got)
	}
	if got := envelope["thread_id"]; got != "thread-1" {
		t.Errorf("thread_id = %v, want thread-1", got)
	}

	inner, ok := envelope["provider_request"].(map[string]any)
	if !ok {
		t.Fatalf("provider_request missing or not an object: %T", envelope["provider_request"])
	}
	if got := inner["model"]; got != "claude-haiku-5-5" {
		t.Errorf("provider_request.model = %v, want the inner model id", got)
	}
	if got := inner["stream"]; got != true {
		t.Errorf("provider_request.stream = %v, want true", got)
	}
	if got := zedAsInt(inner["max_tokens"]); got <= 0 {
		t.Errorf("provider_request.max_tokens = %v, want a positive budget", inner["max_tokens"])
	}
	// The system turn is hoisted out of messages, as the Messages API requires.
	if got, _ := inner["system"].(string); !strings.Contains(got, "be brief") {
		t.Errorf("provider_request.system = %q, want the extracted system prompt", got)
	}

	messages, ok := inner["messages"].([]any)
	if !ok || len(messages) == 0 {
		t.Fatalf("provider_request.messages = %T, want a non-empty array", inner["messages"])
	}
	first, _ := messages[0].(map[string]any)
	content, ok := first["content"].([]any)
	if !ok {
		t.Fatalf("message content = %T, want an array of content blocks", first["content"])
	}
	block, _ := content[0].(map[string]any)
	if got := block["type"]; got != "text" {
		t.Errorf("content block type = %v, want text", got)
	}
	if got := block["text"]; got != "hi" {
		t.Errorf("content block text = %v, want hi", got)
	}
}

// TestBuildZedCompletionBodyAnthropic_StripsTemperature: Zed editors send
// temperature on every chat-completions turn, and the Claude lane serves
// adaptive thinking (claude-haiku-5-5). Anthropic answers any temperature
// other than 1 on a thinking request with a 400, so the knob must not reach
// the provider_request — dropping it entirely, since 1 is the backend's
// adaptive default anyway.
func TestBuildZedCompletionBodyAnthropic_StripsTemperature(t *testing.T) {
	clientBody := `{"model":"claude-haiku-5-5","stream":true,"max_tokens":64,"temperature":0.7,
	 "messages":[{"role":"user","content":"hi"}]}`

	body, err := BuildZedCompletionBody([]byte(clientBody), ZedWireModel{Provider: "anthropic", Model: "claude-haiku-5-5"}, "")
	if err != nil {
		t.Fatalf("BuildZedCompletionBody: %v", err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	inner, ok := envelope["provider_request"].(map[string]any)
	if !ok {
		t.Fatalf("provider_request missing: %T", envelope["provider_request"])
	}
	if _, has := inner["temperature"]; has {
		t.Errorf("provider_request carries temperature = %v; Anthropic rejects it on the adaptive lane", inner["temperature"])
	}
}

func TestBuildZedCompletionBodyOpenAI(t *testing.T) {
	request := `{"model":"gpt-5.6-luna","max_completion_tokens":128,
	 "messages":[{"role":"user","content":"hi there"},
	             {"role":"assistant","content":"sure","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":1}"}}]},
	             {"role":"tool","tool_call_id":"call_1","content":"42"}],
	 "tools":[{"type":"function","function":{"name":"lookup","description":"d","parameters":{"type":"object"}}}]}`

	body, err := BuildZedCompletionBody([]byte(request), ZedWireModel{Provider: "open_ai", Model: "gpt-5.6-luna"}, "")
	if err != nil {
		t.Fatalf("BuildZedCompletionBody: %v", err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if got := envelope["provider"]; got != "open_ai" {
		t.Errorf("provider = %v, want open_ai", got)
	}
	if _, hasThread := envelope["thread_id"]; hasThread {
		t.Errorf("thread_id present for an empty session id: %v", envelope["thread_id"])
	}

	inner, ok := envelope["provider_request"].(map[string]any)
	if !ok {
		t.Fatalf("provider_request missing: %T", envelope["provider_request"])
	}
	if got := zedAsInt(inner["max_output_tokens"]); got != 128 {
		t.Errorf("max_output_tokens = %v, want 128", inner["max_output_tokens"])
	}

	items, ok := inner["input"].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("input = %T, want a non-empty array", inner["input"])
	}
	message, _ := items[0].(map[string]any)
	if got := message["type"]; got != "message" {
		t.Errorf("input[0].type = %v, want message", got)
	}
	if got := message["role"]; got != "user" {
		t.Errorf("input[0].role = %v, want user", got)
	}
	content, ok := message["content"].([]any)
	if !ok {
		t.Fatalf("input[0].content = %T, want an array", message["content"])
	}
	block, _ := content[0].(map[string]any)
	if got := block["type"]; got != "input_text" {
		t.Errorf("content block type = %v, want input_text", got)
	}
	if got := block["text"]; got != "hi there" {
		t.Errorf("content block text = %v, want 'hi there'", got)
	}

	// The tool call and its result become typed Responses items.
	var sawCall, sawOutput bool
	for _, itemRaw := range items {
		item, _ := itemRaw.(map[string]any)
		switch item["type"] {
		case "function_call":
			sawCall = item["call_id"] == "call_1" && item["name"] == "lookup"
		case "function_call_output":
			sawOutput = item["call_id"] == "call_1"
		}
	}
	if !sawCall {
		t.Errorf("input has no function_call item with call_id call_1: %v", items)
	}
	if !sawOutput {
		t.Errorf("input has no function_call_output item with call_id call_1: %v", items)
	}

	tools, _ := inner["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %v, want one entry", inner["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	if tool["name"] != "lookup" || tool["type"] != "function" {
		t.Errorf("tool = %v, want a flattened function tool named lookup", tool)
	}
}

func TestForwardZedTwoStageAuth(t *testing.T) {
	probe := newZedProbe(t, nil)
	blob, connData := zedTestCreds("auth")

	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:       context.Background(),
		Client:    probe.srv.Client(),
		Config:    probe.config(),
		APIKey:    blob,
		Body:      []byte(zedChatBody),
		IsStream:  true,
		ConnData:  connData,
		ModelName: "claude-haiku-5-5",
		StartTime: time.Now(),
		SessionID: "session-1",
	}
	var ttft int64
	req.TTFT = &ttft

	if err := ForwardZed(rec, req); err != nil {
		t.Fatalf("ForwardZed: %v", err)
	}

	// Layer 1: the exchange is a POST to the fixed path with "{uid} {blob}",
	// the org in the body and the system id header. The credential blob is
	// sent verbatim — Zed validates the whole envelope, not its inner token.
	if probe.tokenCalls != 1 {
		t.Fatalf("llm token calls = %d, want 1", probe.tokenCalls)
	}
	if probe.tokenMeth != http.MethodPost {
		t.Errorf("token method = %s, want POST", probe.tokenMeth)
	}
	if probe.tokenPath != "/client/llm_tokens" {
		t.Errorf("token path = %s, want /client/llm_tokens", probe.tokenPath)
	}
	if got, want := probe.tokenAuth, "493663 "+blob; got != want {
		t.Errorf("token Authorization = %q, want %q", got, want)
	}
	if got, want := probe.tokenSys, "sys_auth"; got != want {
		t.Errorf("token %s = %q, want %q", providers.ZedSystemIDHeader, got, want)
	}
	if got := probe.tokenBody["organization_id"]; got != "org_auth" {
		t.Errorf("token body organization_id = %v, want org_auth", got)
	}

	// Layer 2: the completion carries a Bearer token, never the account pair.
	if probe.completionCalls != 1 {
		t.Fatalf("completion calls = %d, want 1", probe.completionCalls)
	}
	if got := probe.completionAuth[0]; !strings.HasPrefix(got, "Bearer ") {
		t.Errorf("completion Authorization = %q, want a Bearer token", got)
	}
	if got := probe.completionSys; got != "sys_auth" {
		t.Errorf("completion %s = %q, want sys_auth", providers.ZedSystemIDHeader, got)
	}
	if got := probe.completionAcc; got != "text/event-stream" {
		t.Errorf("completion Accept = %q, want text/event-stream", got)
	}

	// The envelope: Zed's own shape around the Anthropic body.
	if got := probe.envelope["provider"]; got != "anthropic" {
		t.Errorf("envelope provider = %v, want anthropic", got)
	}
	if got := probe.envelope["model"]; got != "claude-haiku-5-5" {
		t.Errorf("envelope model = %v, want claude-haiku-5-5", got)
	}
	if got := probe.envelope["thread_id"]; got != "session-1" {
		t.Errorf("envelope thread_id = %v, want the request session id", got)
	}
}

func TestForwardZedAnthropicStreamTranslation(t *testing.T) {
	probe := newZedProbe(t, nil)
	blob, connData := zedTestCreds("stream")

	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:       context.Background(),
		Client:    probe.srv.Client(),
		Config:    probe.config(),
		APIKey:    blob,
		Body:      []byte(zedChatBody),
		IsStream:  true,
		ConnData:  connData,
		ModelName: "claude-haiku-5-5",
		// TTFT is milliseconds since StartTime, truncated to an integer. A
		// warm token cache can drive the whole call under 1ms, which records
		// 0 and is indistinguishable from "never recorded". Dating the start
		// far enough back keeps any recorded value nonzero without waiting.
		StartTime: time.Now().Add(-50 * time.Millisecond),
	}
	var ttft int64
	req.TTFT = &ttft

	if err := ForwardZed(rec, req); err != nil {
		t.Fatalf("ForwardZed: %v", err)
	}

	out := rec.Body.String()
	if !strings.Contains(out, `"role":"assistant"`) {
		t.Errorf("stream carried no assistant role delta:\n%s", out)
	}
	if !strings.Contains(out, "Hello from Zed") {
		t.Errorf("stream carried no text delta:\n%s", out)
	}
	if !strings.Contains(out, "data: [DONE]") {
		t.Errorf("stream was not terminated with [DONE]:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk); err != nil {
			t.Fatalf("emitted frame is not JSON (%v): %s", err, line)
		}
		if _, ok := chunk["choices"]; !ok {
			t.Errorf("emitted frame is not an OpenAI chunk: %s", line)
		}
	}
	if ttft == 0 {
		t.Errorf("TTFT was never recorded")
	}
}

func TestForwardZedNonStreamAggregatesSSE(t *testing.T) {
	probe := newZedProbe(t, nil)
	blob, connData := zedTestCreds("nonstream")

	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:       context.Background(),
		Client:    probe.srv.Client(),
		Config:    probe.config(),
		APIKey:    blob,
		Body:      []byte(`{"model":"claude-haiku-5-5","messages":[{"role":"user","content":"hi"}]}`),
		IsStream:  false,
		ConnData:  connData,
		ModelName: "claude-haiku-5-5",
		StartTime: time.Now(),
	}
	if err := ForwardZed(rec, req); err != nil {
		t.Fatalf("ForwardZed: %v", err)
	}

	var completion map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &completion); err != nil {
		t.Fatalf("response is not JSON (%v): %s", err, rec.Body.String())
	}
	choices, _ := completion["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("choices = %v, want one", completion["choices"])
	}
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	if got := message["content"]; got != "Hello from Zed" {
		t.Errorf("message content = %v, want the aggregated stream text", got)
	}
}

func TestForwardZedRefreshesTokenOn401(t *testing.T) {
	probe := newZedProbe(t, func(w http.ResponseWriter, attempt int) {
		if attempt == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"token expired"}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(zedClaudeSSE))
	})
	blob, connData := zedTestCreds("retry")

	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:       context.Background(),
		Client:    probe.srv.Client(),
		Config:    probe.config(),
		APIKey:    blob,
		Body:      []byte(zedChatBody),
		IsStream:  true,
		ConnData:  connData,
		ModelName: "claude-haiku-5-5",
		// TTFT is milliseconds since StartTime, truncated to an integer. A
		// warm token cache can drive the whole call under 1ms, which records
		// 0 and is indistinguishable from "never recorded". Dating the start
		// far enough back keeps any recorded value nonzero without waiting.
		StartTime: time.Now().Add(-50 * time.Millisecond),
	}
	var ttft int64
	req.TTFT = &ttft

	if err := ForwardZed(rec, req); err != nil {
		t.Fatalf("ForwardZed after refresh: %v", err)
	}
	if probe.tokenCalls != 2 {
		t.Errorf("llm token calls = %d, want 2 (cold mint + 401 refresh)", probe.tokenCalls)
	}
	if probe.completionCalls != 2 {
		t.Errorf("completion calls = %d, want 2 (original + replay)", probe.completionCalls)
	}
	for _, got := range probe.completionAuth {
		if !strings.HasPrefix(got, "Bearer ") {
			t.Errorf("completion Authorization = %q, want a Bearer token", got)
		}
	}
	if !strings.Contains(rec.Body.String(), "Hello from Zed") {
		t.Errorf("replayed stream was not served:\n%s", rec.Body.String())
	}
}

func TestForwardZedSurfacesUpstreamError(t *testing.T) {
	// The probe server listens on loopback, which the SSRF guard blocks
	// unless private networking is allowed for the process.
	t.Setenv("ALLOW_PRIVATE_NETWORKING", "true")
	probe := newZedProbe(t, func(w http.ResponseWriter, _ int) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"plan does not include this model"}`))
	})
	blob, connData := zedTestCreds("forbidden")

	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:       context.Background(),
		Client:    probe.srv.Client(),
		Config:    probe.config(),
		APIKey:    blob,
		Body:      []byte(zedChatBody),
		IsStream:  true,
		ConnData:  connData,
		ModelName: "claude-haiku-5-5",
		StartTime: time.Now(),
	}
	err := ForwardZed(rec, req)
	if err == nil {
		t.Fatalf("ForwardZed returned nil for a 403")
	}
	if !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "plan does not include this model") {
		t.Errorf("error = %v, want the upstream status and body", err)
	}
}

func TestZedTokenExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	future := now.Add(2 * time.Hour)

	token := zedTestJWT(t, future.Unix())
	if got := zedTokenExpiry(token, now); !got.Equal(future) {
		t.Errorf("zedTokenExpiry(valid jwt) = %v, want %v", got, future)
	}
	// An unreadable token is still cached, on the conservative window.
	if got := zedTokenExpiry("not-a-jwt", now); !got.Equal(now.Add(zedLLMTokenTTL)) {
		t.Errorf("zedTokenExpiry(opaque) = %v, want now+%v", got, zedLLMTokenTTL)
	}
	// A padded base64url payload decodes too: the segment length is chosen so
	// the unpadded encoding would end mid-quantum.
	paddedPayload, err := json.Marshal(map[string]any{"exp": future.Unix(), "x": 1})
	if err != nil {
		t.Fatalf("marshal padded payload: %v", err)
	}
	padded := "hdr." + base64.URLEncoding.EncodeToString(paddedPayload) + ".sig"
	if got := zedTokenExpiry(padded, now); !got.Equal(future) {
		t.Errorf("zedTokenExpiry(padded jwt) = %v, want %v", got, future)
	}
}

func TestZedAggregateClaudeSSE(t *testing.T) {
	aggregated, err := zedAggregateClaudeSSE([]byte(zedClaudeSSE))
	if err != nil {
		t.Fatalf("zedAggregateClaudeSSE: %v", err)
	}
	var message map[string]any
	if err := json.Unmarshal(aggregated, &message); err != nil {
		t.Fatalf("aggregated body is not JSON: %v", err)
	}
	if got := message["type"]; got != "message" {
		t.Errorf("type = %v, want message", got)
	}
	if got := message["stop_reason"]; got != "end_turn" {
		t.Errorf("stop_reason = %v, want end_turn", got)
	}
	content, _ := message["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content = %v, want one block", message["content"])
	}
	block, _ := content[0].(map[string]any)
	if got := block["text"]; got != "Hello from Zed" {
		t.Errorf("block text = %v, want the concatenated deltas", got)
	}
	if _, ok := message["usage"].(map[string]any); !ok {
		t.Errorf("usage missing from the aggregated message: %v", message)
	}
}

func TestZedResponsesJSONToChat(t *testing.T) {
	raw := []byte(`{"id":"resp_1","model":"gpt-5.6-luna","status":"completed",
	 "output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]},
	           {"type":"function_call","call_id":"call_9","name":"lookup","arguments":"{}"}],
	 "usage":{"input_tokens":11,"output_tokens":3,"total_tokens":14}}`)
	converted, err := zedResponsesJSONToChat(raw)
	if err != nil {
		t.Fatalf("zedResponsesJSONToChat: %v", err)
	}
	var completion map[string]any
	if err := json.Unmarshal(converted, &completion); err != nil {
		t.Fatalf("converted body is not JSON: %v", err)
	}
	choices, _ := completion["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("choices = %v, want one", completion["choices"])
	}
	choice, _ := choices[0].(map[string]any)
	if got := choice["finish_reason"]; got != "tool_calls" {
		t.Errorf("finish_reason = %v, want tool_calls", got)
	}
	message, _ := choice["message"].(map[string]any)
	if got := message["content"]; got != "done" {
		t.Errorf("message content = %v, want done", got)
	}
	toolCalls, _ := message["tool_calls"].([]any)
	if len(toolCalls) != 1 {
		t.Fatalf("tool_calls = %v, want one", message["tool_calls"])
	}
	call, _ := toolCalls[0].(map[string]any)
	fn, _ := call["function"].(map[string]any)
	if fn["name"] != "lookup" || call["id"] != "call_9" {
		t.Errorf("tool call = %v, want lookup/call_9", call)
	}
	usage, _ := completion["usage"].(map[string]any)
	if zedAsInt(usage["prompt_tokens"]) != 11 || zedAsInt(usage["completion_tokens"]) != 3 {
		t.Errorf("usage = %v, want 11/3", usage)
	}
}

// TestForwardZedOpenAIBranch drives the open_ai wire provider end to end with
// a Responses event stream and checks the client still receives chat chunks.
func TestForwardZedOpenAIBranch(t *testing.T) {
	const responsesSSE = `event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"hi"}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_2","model":"gpt-5.6-luna","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}

`
	probe := newZedProbe(t, func(w http.ResponseWriter, _ int) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(responsesSSE))
	})
	blob, connData := zedTestCreds("openai")

	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:       context.Background(),
		Client:    probe.srv.Client(),
		Config:    probe.config(),
		APIKey:    blob,
		Body:      []byte(`{"model":"gpt-5.6-luna","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
		IsStream:  true,
		ConnData:  connData,
		ModelName: "gpt-5.6-luna",
		StartTime: time.Now(),
	}
	var ttft int64
	req.TTFT = &ttft

	if err := ForwardZed(rec, req); err != nil {
		t.Fatalf("ForwardZed: %v", err)
	}
	if got := probe.envelope["provider"]; got != "open_ai" {
		t.Errorf("envelope provider = %v, want open_ai", got)
	}
	out := rec.Body.String()
	if !strings.Contains(out, `"choices"`) {
		t.Errorf("client did not receive chat chunks:\n%s", out)
	}
	if !strings.Contains(out, "hi") {
		t.Errorf("client did not receive the text delta:\n%s", out)
	}
}
