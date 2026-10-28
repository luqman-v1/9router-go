package executor

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
)

func TestTransformCodebuddyBody_ForceStream(t *testing.T) {
	// non-stream request should be forced to stream=true
	input := `{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}],"stream":false}`
	out, err := transformCodebuddyBody([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if m["stream"] != true {
		t.Errorf("expected stream=true, got %v", m["stream"])
	}
}

func TestTransformCodebuddyBody_StreamAlreadyTrue(t *testing.T) {
	input := `{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}],"stream":true}`
	out, err := transformCodebuddyBody([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if m["stream"] != true {
		t.Errorf("expected stream=true, got %v", m["stream"])
	}
}

func TestTransformCodebuddyBody_ReasoningEffortNone(t *testing.T) {
	// "none" and "off" should be stripped
	for _, eff := range []string{"none", "off"} {
		input := `{"model":"glm-5.2","messages":[],"reasoning_effort":"` + eff + `"}`
		out, err := transformCodebuddyBody([]byte(input))
		if err != nil {
			t.Fatalf("unexpected error for %q: %v", eff, err)
		}
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("unmarshal output: %v", err)
		}
		if _, exists := m["reasoning_effort"]; exists {
			t.Errorf("reasoning_effort=%q should be removed, but still present", eff)
		}
		if _, exists := m["reasoning_summary"]; exists {
			t.Errorf("reasoning_summary should NOT be set when effort=%q", eff)
		}
	}
}

func TestTransformCodebuddyBody_ReasoningEffortMedium(t *testing.T) {
	// Active reasoning_effort should inject reasoning_summary="auto"
	input := `{"model":"glm-5.2","messages":[],"reasoning_effort":"medium"}`
	out, err := transformCodebuddyBody([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if m["reasoning_effort"] != "medium" {
		t.Errorf("reasoning_effort should stay %q, got %v", "medium", m["reasoning_effort"])
	}
	if m["reasoning_summary"] != "auto" {
		t.Errorf("expected reasoning_summary=%q, got %v", "auto", m["reasoning_summary"])
	}
}

func TestTransformCodebuddyBody_NoReasoningEffort(t *testing.T) {
	// When reasoning_effort is not set, neither should reasoning_summary
	input := `{"model":"glm-5.2","messages":[{"role":"user","content":"hello"}]}`
	out, err := transformCodebuddyBody([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if _, exists := m["reasoning_effort"]; exists {
		t.Error("reasoning_effort should not be present")
	}
	if _, exists := m["reasoning_summary"]; exists {
		t.Error("reasoning_summary should not be present when no effort set")
	}
}

func TestTransformCodebuddyBody_UpstreamMessageShape(t *testing.T) {
	// Upstream PR 5032 parity: leading system prompt prepended, client system
	// instructions preserved, user content as typed blocks.
	input := `{"model":"glm-5.3-flash","messages":[{"role":"system","content":"client-sys"},{"role":"user","content":"hi"}],"stream":false}`
	out, err := transformCodebuddyBody([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var m struct {
		Stream   bool `json:"stream"`
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if !m.Stream {
		t.Error("stream must be forced true")
	}
	if len(m.Messages) != 2 {
		t.Fatalf("expected system + user messages, got %d: %s", len(m.Messages), out)
	}
	if m.Messages[0].Role != "system" {
		t.Errorf("first message must be system, got %q", m.Messages[0].Role)
	}
	sysContent, ok := m.Messages[0].Content.(string)
	if !ok || sysContent != "You are CodeBuddy Code.\n\nclient-sys" {
		t.Errorf("expected combined system prompt, got %q", sysContent)
	}
	blocks, ok := m.Messages[1].Content.([]any)
	if !ok || len(blocks) != 1 {
		t.Fatalf("user content must be typed blocks, got %#v", m.Messages[1].Content)
	}
	block, _ := blocks[0].(map[string]any)
	if block["type"] != "text" || block["text"] != "hi" {
		t.Errorf("unexpected user block: %#v", block)
	}
}

func TestTransformCodebuddyBody_SanitizesChannelMarkers(t *testing.T) {
	// Upstream PR 5032 parity: prepends "You are CodeBuddy Code.", preserves caller
	// system/developer instructions, and sanitizes external client markers.
	input := `{
		"model": "glm-5.2",
		"messages": [
			{
				"role": "system",
				"content": "You are Claude Code, Anthropic's official CLI for Claude.\nAlways reply in JSON."
			},
			{
				"role": "developer",
				"content": "Follow strict schema rules."
			},
			{
				"role": "user",
				"content": "hello"
			},
			{
				"role": "assistant",
				"content": "I am running with cc_entrypoint=cli"
			}
		]
	}`
	out, err := transformCodebuddyBody([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var m struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}

	if len(m.Messages) != 3 {
		t.Fatalf("expected 3 messages (system + user + assistant), got %d: %s", len(m.Messages), out)
	}

	// 1. Consolidated system message
	if m.Messages[0].Role != "system" {
		t.Fatalf("first message role = %q, want system", m.Messages[0].Role)
	}
	sysStr, ok := m.Messages[0].Content.(string)
	if !ok {
		t.Fatalf("first message content must be string, got %T", m.Messages[0].Content)
	}
	if !strings.HasPrefix(sysStr, "You are CodeBuddy Code.") {
		t.Errorf("system prompt must start with 'You are CodeBuddy Code.', got %q", sysStr)
	}
	if !strings.Contains(sysStr, "Always reply in JSON.") {
		t.Errorf("system prompt must preserve caller system instructions, got %q", sysStr)
	}
	if !strings.Contains(sysStr, "Follow strict schema rules.") {
		t.Errorf("system prompt must preserve caller developer instructions, got %q", sysStr)
	}
	if strings.Contains(sysStr, "You are Claude Code") {
		t.Errorf("system prompt must sanitize external client markers, got %q", sysStr)
	}
	if !strings.Contains(sysStr, "[external-client-identity]") {
		t.Errorf("system prompt must replace marker with [external-client-identity], got %q", sysStr)
	}

	// 2. User message formatted as typed blocks
	if m.Messages[1].Role != "user" {
		t.Fatalf("second message role = %q, want user", m.Messages[1].Role)
	}
	userBlocks, ok := m.Messages[1].Content.([]any)
	if !ok || len(userBlocks) != 1 {
		t.Fatalf("user content must be 1 typed block, got %#v", m.Messages[1].Content)
	}
	block, _ := userBlocks[0].(map[string]any)
	if block["type"] != "text" || block["text"] != "hello" {
		t.Errorf("unexpected user block: %#v", block)
	}

	// 3. Assistant message sanitized
	if m.Messages[2].Role != "assistant" {
		t.Fatalf("third message role = %q, want assistant", m.Messages[2].Role)
	}
	asstStr, ok := m.Messages[2].Content.(string)
	if !ok {
		t.Fatalf("assistant content must be string, got %T", m.Messages[2].Content)
	}
	if strings.Contains(asstStr, "cc_entrypoint=cli") {
		t.Errorf("assistant content must not contain cc_entrypoint=cli, got %q", asstStr)
	}
	if !strings.Contains(asstStr, "[external-client-identity]") {
		t.Errorf("assistant content must replace marker with [external-client-identity], got %q", asstStr)
	}
}

func TestSSEToOpenAIJSON_MergesChunks(t *testing.T) {
	sse := "data: {\"id\":\"chatcmpl-1\",\"created\":123,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\" world\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"thinking\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":3}}\n\n" +
		"data: [DONE]\n\n"
	out, ok := sseToOpenAIJSON([]byte(sse))
	if !ok {
		t.Fatal("expected SSE detected")
	}
	var resp struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("output not valid JSON: %v\n%s", err, out)
	}
	if resp.ID != "chatcmpl-1" || resp.Model != "glm-5.2" {
		t.Errorf("id/model mismatch: %+v", resp)
	}
	if resp.Choices[0].Message.Content != "Hello world" {
		t.Errorf("content not merged, got %q", resp.Choices[0].Message.Content)
	}
	if resp.Choices[0].Message.ReasoningContent != "thinking" {
		t.Errorf("reasoning_content missing, got %q", resp.Choices[0].Message.ReasoningContent)
	}
	if resp.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason missing, got %q", resp.Choices[0].FinishReason)
	}
	if resp.Usage.PromptTokens != 5 {
		t.Errorf("usage not carried, got %+v", resp.Usage)
	}
}

func TestSSEToOpenAIJSON_MergesToolCalls(t *testing.T) {
	sse := "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"get_\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"weather\",\"arguments\":\"{\\\"city\\\":\\\"KL\\\"}\"}}]}}]}\n\n"
	out, ok := sseToOpenAIJSON([]byte(sse))
	if !ok {
		t.Fatal("expected SSE detected")
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content   any `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("output not valid JSON: %v\n%s", err, out)
	}
	if resp.Choices[0].Message.Content != nil {
		t.Errorf("content should be null when tool_calls present, got %v", resp.Choices[0].Message.Content)
	}
	tcs := resp.Choices[0].Message.ToolCalls
	if len(tcs) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(tcs))
	}
	if tcs[0].ID != "call_1" {
		t.Errorf("tool call id not carried, got %q", tcs[0].ID)
	}
	if tcs[0].Function.Name != "get_weather" {
		t.Errorf("tool name not merged, got %q", tcs[0].Function.Name)
	}
	if tcs[0].Function.Arguments != `{"city":"KL"}` {
		t.Errorf("tool arguments not merged, got %q", tcs[0].Function.Arguments)
	}
}

func TestSSEToOpenAIJSON_NotSSE(t *testing.T) {
	// A plain JSON error body must pass through untouched.
	if _, ok := sseToOpenAIJSON([]byte(`{"error":{"message":"boom","code":400}}`)); ok {
		t.Error("plain JSON should not be treated as SSE")
	}
}

// TestSSEToOpenAIJSON_RepeatedArgumentDeltaAppends pins the plain-append rule
// on the codebuddy executor, which carried the drop-a-delta heuristic after
// forward.go dropped it.
//
// The repeated fragment here is a substring of what has accumulated, so the
// old `strings.Contains(existing, a)` guard fired and dropped it, and the
// arguments stopped being valid JSON. Same shape as the maintainer's
// `parts=["{"cmd":"run ", "run ", "echo done""]` case.
func TestSSEToOpenAIJSON_RepeatedArgumentDeltaAppends(t *testing.T) {
	parts := []string{`{"cmd":"run `, `run `, `echo done"}`}
	var sse strings.Builder
	sse.WriteString(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_r","type":"function","function":{"name":"run_tool","arguments":"` + escapeJSONArg(parts[0]) + `"}}]}}]}` + "\n\n")
	for _, p := range parts[1:] {
		sse.WriteString(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"` + escapeJSONArg(p) + `"}}]}}]}` + "\n\n")
	}

	out, ok := sseToOpenAIJSON([]byte(sse.String()))
	if !ok {
		t.Fatal("expected SSE detected")
	}
	var resp struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					Function struct {
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("output not valid JSON: %v\n%s", err, out)
	}
	got := resp.Choices[0].Message.ToolCalls[0].Function.Arguments
	want := strings.Join(parts, "")
	if got != want {
		t.Errorf("arguments = %q, want %q", got, want)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Errorf("accumulated arguments are not valid JSON: %v (%q)", err, got)
	}
}

// escapeJSONArg renders s as the body of a JSON string literal.
func escapeJSONArg(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1])
}

func TestForwardCodebuddyCN_WireHeadersAndBody(t *testing.T) {
	var capturedReq *http.Request
	var capturedBody []byte

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedReq = r
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("data: {\"id\":\"chatcmpl-wire\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer ts.Close()

	cfg := &providers.ProviderConfig{
		BaseURL: ts.URL,
		StaticHeaders: map[string]string{
			"User-Agent":          "CLI/2.108.1 CodeBuddy/2.108.1",
			"X-Product":           "SaaS",
			"X-IDE-Type":          "CLI",
			"X-IDE-Name":          "CLI",
			"x-requested-with":    "XMLHttpRequest",
			"x-codebuddy-request": "1",
		},
	}

	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:      context.Background(),
		Client:   ts.Client(),
		Config:   cfg,
		APIKey:   "test-token",
		IsStream: true,
		Body: []byte(`{
			"model": "deepseek-v4.1-flash",
			"messages": [
				{"role": "system", "content": "You are Claude Code, Anthropic's official CLI for Claude.\nDo work."},
				{"role": "user", "content": "ping"}
			]
		}`),
	}

	err := ForwardCodebuddyCN(rec, req)
	if err != nil {
		t.Fatalf("ForwardCodebuddyCN returned error: %v", err)
	}

	if capturedReq == nil {
		t.Fatal("upstream request not captured")
	}

	// Verify headers
	if capturedReq.Header.Get("User-Agent") != "CLI/2.108.1 CodeBuddy/2.108.1" {
		t.Errorf("User-Agent = %q", capturedReq.Header.Get("User-Agent"))
	}
	if capturedReq.Header.Get("X-App") != "cli" {
		t.Errorf("X-App = %q", capturedReq.Header.Get("X-App"))
	}
	if capturedReq.Header.Get("X-IDE-Type") != "CLI" || capturedReq.Header.Get("X-IDE-Name") != "CLI" || capturedReq.Header.Get("X-IDE-Version") != "2.108.1" {
		t.Errorf("X-IDE-* mismatch")
	}
	if capturedReq.Header.Get("X-Private-Data") != "false" {
		t.Errorf("X-Private-Data = %q", capturedReq.Header.Get("X-Private-Data"))
	}
	if capturedReq.Header.Get("X-Domain") == "" {
		t.Error("X-Domain must be set")
	}
	if capturedReq.Header.Get("X-Stainless-Runtime") != "node" || capturedReq.Header.Get("X-Stainless-Lang") != "js" {
		t.Errorf("Stainless headers mismatch")
	}
	reqID := capturedReq.Header.Get("X-Request-ID")
	if len(reqID) != 32 {
		t.Errorf("X-Request-ID = %q, want 32 hex chars", reqID)
	}
	if capturedReq.Header.Get("X-Conversation-Message-ID") != reqID {
		t.Errorf("X-Conversation-Message-ID must match X-Request-ID")
	}
	if len(capturedReq.Header.Get("X-Conversation-Request-ID")) != 32 {
		t.Errorf("X-Conversation-Request-ID = %q, want 32 hex chars", capturedReq.Header.Get("X-Conversation-Request-ID"))
	}
	if capturedReq.Header.Get("X-Conversation-ID") == "" {
		t.Error("X-Conversation-ID must be set")
	}
	if capturedReq.Header.Get("X-Agent-Intent") != "craft" || capturedReq.Header.Get("X-Agent-Purpose") != "conversation" {
		t.Errorf("Agent intent/purpose headers mismatch")
	}

	// Verify body
	var parsedBody struct {
		Stream   bool `json:"stream"`
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(capturedBody, &parsedBody); err != nil {
		t.Fatalf("unmarshal captured body: %v", err)
	}
	if !parsedBody.Stream {
		t.Error("body stream must be true")
	}
	if len(parsedBody.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(parsedBody.Messages))
	}
	sysText, _ := parsedBody.Messages[0].Content.(string)
	if !strings.HasPrefix(sysText, "You are CodeBuddy Code.") {
		t.Errorf("system prompt must start with 'You are CodeBuddy Code.', got %q", sysText)
	}
	if !strings.Contains(sysText, "Do work.") {
		t.Errorf("system prompt must preserve caller instructions, got %q", sysText)
	}
	if strings.Contains(sysText, "You are Claude Code") {
		t.Errorf("system prompt must sanitize external client markers, got %q", sysText)
	}
}

func TestForwardCodebuddyCN_RateLimitMapping(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"code":6004,"msg":"超出频率限制，恢复时间 2026-10-23 15:30:00"}`))
	}))
	defer ts.Close()

	cfg := &providers.ProviderConfig{BaseURL: ts.URL}
	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:    context.Background(),
		Client: ts.Client(),
		Config: cfg,
		APIKey: "tok",
		Body:   []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
	}

	err := ForwardCodebuddyCN(rec, req)
	if err == nil {
		t.Fatal("expected error from upstream 400 with 6004")
	}
	var ue *proxy.UpstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *proxy.UpstreamError, got %T: %v", err, err)
	}
	if ue.StatusCode != http.StatusTooManyRequests {
		t.Errorf("expected mapped StatusCode = 429, got %d", ue.StatusCode)
	}
}

// The operator's Custom Headers must reach the wire. This executor assembles
// its own header map, so a value the dashboard accepted was previously
// replaced here and never sent — the dashboard showed the header, the upstream
// never saw it.
func TestForwardCodebuddyCN_OperatorOverridesWin(t *testing.T) {
	var got http.Header
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer ts.Close()

	cfg := &providers.ProviderConfig{
		BaseURL: ts.URL,
		StaticHeaders: map[string]string{
			"User-Agent": "CLI/2.108.1 CodeBuddy/2.108.1",
			"X-IDE-Type": "CLI",
		},
		HeaderOverrides: map[string]string{
			"User-Agent":                "IDE/2.108.1 CodeBuddy/2.108.1",
			"X-IDE-Type":                "IDE",
			"X-IDE-Name":                "IDE",
			"X-Conversation-ID":         "6ba7b810-9dad-11d3-a456-426614174000",
			"X-Conversation-Request-ID": "6ba7b811-9dad-11d3-a456-426614174000",
			"X-Conversation-Message-ID": "6ba7b812-9dad-11d3-a456-426614174000",
		},
	}
	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:      context.Background(),
		Client:   ts.Client(),
		Config:   cfg,
		APIKey:   "tok",
		IsStream: true,
		Body:     []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
	}
	if err := ForwardCodebuddyCN(rec, req); err != nil {
		t.Fatalf("ForwardCodebuddyCN: %v", err)
	}
	if got == nil {
		t.Fatal("upstream request not captured")
	}

	for header, want := range cfg.HeaderOverrides {
		if v := got.Get(header); v != want {
			t.Errorf("%s = %q, want the operator's %q", header, v, want)
		}
	}
	// A generated value must not survive next to the operator's: two entries
	// for one name leave the winner to map iteration order.
	if n := len(got.Values("X-Conversation-ID")); n != 1 {
		t.Errorf("X-Conversation-ID sent %d times, want exactly 1", n)
	}
}

// A lowercase override must replace the executor's own spelling rather than
// land beside it.
func TestForwardCodebuddyCN_OverrideCaseInsensitive(t *testing.T) {
	var got http.Header
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer ts.Close()

	cfg := &providers.ProviderConfig{
		BaseURL:         ts.URL,
		HeaderOverrides: map[string]string{"x-ide-type": "IDE"},
	}
	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:      context.Background(),
		Client:   ts.Client(),
		Config:   cfg,
		APIKey:   "tok",
		IsStream: true,
		Body:     []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
	}
	if err := ForwardCodebuddyCN(rec, req); err != nil {
		t.Fatalf("ForwardCodebuddyCN: %v", err)
	}
	if v := got.Get("X-IDE-Type"); v != "IDE" {
		t.Errorf("X-IDE-Type = %q, want IDE", v)
	}
	if n := len(got.Values("X-IDE-Type")); n != 1 {
		t.Errorf("X-IDE-Type sent %d times, want exactly 1", n)
	}
}
