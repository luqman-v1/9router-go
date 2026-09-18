package executor

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
)

func TestInjectReasoningContent(t *testing.T) {
	input := []byte(`{
		"model": "deepseek-v4-flash-free",
		"messages": [
			{"role": "user", "content": "hello"},
			{"role": "assistant", "content": "hi there"}
		]
	}`)

	res := InjectReasoningContent(input, "opencode")

	var reqMap map[string]any
	if err := json.Unmarshal(res, &reqMap); err != nil {
		t.Fatalf("failed to parse output: %v", err)
	}

	msgs := reqMap["messages"].([]any)
	asst := msgs[1].(map[string]any)
	if rc, ok := asst["reasoning_content"].(string); !ok || rc != " " {
		t.Errorf("expected reasoning_content to be ' ', got %v", asst["reasoning_content"])
	}
}

func TestForwardOpencode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer public" {
			t.Errorf("expected Bearer public, got %s", r.Header.Get("Authorization"))
		}
		if r.Header.Get("x-opencode-client") != "cli" {
			t.Errorf("expected cli client header, got %s", r.Header.Get("x-opencode-client"))
		}
		if len(r.Header.Get("x-opencode-project")) != 40 {
			t.Errorf("expected 40-char hex x-opencode-project, got %s", r.Header.Get("x-opencode-project"))
		}
		if !strings.HasPrefix(r.Header.Get("x-opencode-session"), "ses_") {
			t.Errorf("expected ses_ prefix on x-opencode-session, got %s", r.Header.Get("x-opencode-session"))
		}
		if !strings.HasPrefix(r.Header.Get("x-opencode-request"), "msg_") {
			t.Errorf("expected msg_ prefix on x-opencode-request, got %s", r.Header.Get("x-opencode-request"))
		}
		if r.Header.Get("User-Agent") != proxy.DefaultOpenCodeUA {
			t.Errorf("expected User-Agent %s, got %s", proxy.DefaultOpenCodeUA, r.Header.Get("User-Agent"))
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"reasoning_content":" "`) {
			t.Errorf("expected reasoning_content injected in request body, got %s", string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"msg_123","choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{
		BaseURL: srv.URL,
	}

	rec := httptest.NewRecorder()
	req := &Request{
		Client:        srv.Client(),
		Config:        cfg,
		APIKey:        "", // Should fallback to "public"
		Body:          []byte(`{"model":"deepseek-v4-flash-free","messages":[{"role":"assistant","content":"prev"}]}`),
		IsStream:      false,
		TranslateResp: false,
	}

	err := ForwardOpencode(rec, req)
	if err != nil {
		t.Fatalf("ForwardOpencode failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
}

func TestForwardOpencodeGo_OpenAIRouting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-go-key" {
			t.Errorf("expected Authorization Bearer secret-go-key header, got %s", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"msg_openai","choices":[{"message":{"content":"hello"}}]}`))
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{
		BaseURL: srv.URL,
	}

	rec := httptest.NewRecorder()
	req := &Request{
		Client:        srv.Client(),
		Config:        cfg,
		APIKey:        "secret-go-key",
		Body:          []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`),
		IsStream:      false,
		TranslateResp: false,
	}

	err := ForwardOpencodeGo(rec, req)
	if err != nil {
		t.Fatalf("ForwardOpencodeGo failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
}

func TestForwardOpencode_MuseSparkResponsesRouting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			t.Errorf("expected path ending in /responses, got %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if parsed["input"] == nil {
			t.Errorf("expected input array in Responses API format, got: %s", string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"resp_123","output":[{"type":"message","content":[{"type":"text","text":"4"}]}]}`))
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{
		BaseURL: srv.URL + "/chat/completions",
	}

	rec := httptest.NewRecorder()
	req := &Request{
		Client:        srv.Client(),
		Config:        cfg,
		APIKey:        "",
		Body:          []byte(`{"model":"muse-spark-1.2-contributor-free","messages":[{"role":"user","content":"2+2?"}],"reasoning_effort":"max"}`),
		IsStream:      false,
		TranslateResp: false,
	}

	err := ForwardOpencode(rec, req)
	if err != nil {
		t.Fatalf("ForwardOpencode muse-spark failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
}

func TestForwardOpencode_MuseSpark13_ResponsesRouting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			t.Errorf("expected /responses for muse-spark-1.3, got %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		_ = json.Unmarshal(body, &parsed)
		if parsed["model"] != "muse-spark-1.3-contributor-free" {
			t.Errorf("expected model muse-spark-1.3, got %v", parsed["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"resp_13","output":[{"type":"message","content":[{"type":"text","text":"ok 1.3"}]}]}`))
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{BaseURL: srv.URL + "/chat/completions"}
	rec := httptest.NewRecorder()
	req := &Request{
		Client: srv.Client(), Config: cfg, APIKey: "",
		Body: []byte(`{"model":"oc/muse-spark-1.3-contributor-free","messages":[{"role":"user","content":"hi"}],"stream":false}`),
		IsStream: false, TranslateResp: false,
	}
	if err := ForwardOpencode(rec, req); err != nil {
		t.Fatalf("1.3 failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestForwardOpencode_MuseSpark_OCPrefix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			t.Errorf("expected /responses for oc/ prefix, got %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"resp_oc","output":[{"type":"message","content":[{"type":"text","text":"ok"}]}]}`))
	}))
	defer srv.Close()
	cfg := &providers.ProviderConfig{BaseURL: srv.URL + "/chat/completions"}
	rec := httptest.NewRecorder()
	req := &Request{
		Client: srv.Client(), Config: cfg, APIKey: "",
		Body: []byte(`{"model":"oc/muse-spark-1.3-contributor-free","messages":[{"role":"user","content":"hi"}]}`),
		IsStream: false, TranslateResp: false,
	}
	if err := ForwardOpencode(rec, req); err != nil {
		t.Fatalf("oc prefix failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestNormalizeMuseSparkResponsesBody_ReasoningAndToolChoice(t *testing.T) {
	raw := []byte(`{
		"model": "muse-spark-1.3-contributor-free",
		"reasoning_effort": "max",
		"tool_choice": {"type": "function", "function": {"name": "shell"}},
		"input": [
			{"type": "message", "role": "user", "content": "hi"},
			{"type": "reasoning", "encrypted_content": "ENCRYPTED_BLOB", "text": "thinking"},
			{"type": "function_call", "name": "read", "encrypted_content": "BLOB_2"}
		]
	}`)

	out, err := normalizeMuseSparkResponsesBody(raw, "muse-spark-1.3-contributor-free")
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}

	// tool_choice should be auto
	if parsed["tool_choice"] != "auto" {
		t.Errorf("expected tool_choice 'auto', got %v", parsed["tool_choice"])
	}

	// reasoning effort max -> xhigh
	rMap, _ := parsed["reasoning"].(map[string]any)
	if rMap["effort"] != "xhigh" || rMap["summary"] != "auto" {
		t.Errorf("expected reasoning effort xhigh, got %v", rMap)
	}

	// input reasoning stripped and encrypted_content removed
	inList, _ := parsed["input"].([]any)
	if len(inList) != 2 {
		t.Fatalf("expected 2 items in input (reasoning stripped), got %d", len(inList))
	}
	fc, _ := inList[1].(map[string]any)
	if fc["encrypted_content"] != nil {
		t.Errorf("expected encrypted_content deleted from function_call, got %v", fc["encrypted_content"])
	}
}

func TestEnsureMessagesMaxTokens(t *testing.T) {
	t.Run("missing max_tokens defaults to 4096", func(t *testing.T) {
		input := []byte(`{"model":"oc/union-alpha","messages":[{"role":"user","content":"hi"}]}`)
		out := ensureMessagesMaxTokens(input, "union-alpha")
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if m["model"] != "union-alpha" {
			t.Errorf("expected model 'union-alpha', got %v", m["model"])
		}
		if m["max_tokens"] != float64(4096) {
			t.Errorf("expected max_tokens 4096, got %v", m["max_tokens"])
		}
	})

	t.Run("max_tokens <= 0 defaults to 4096", func(t *testing.T) {
		input := []byte(`{"model":"union-alpha","max_tokens":0,"messages":[{"role":"user","content":"hi"}]}`)
		out := ensureMessagesMaxTokens(input, "union-alpha")
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if m["max_tokens"] != float64(4096) {
			t.Errorf("expected max_tokens 4096, got %v", m["max_tokens"])
		}
	})

	t.Run("max_completion_tokens used when max_tokens missing", func(t *testing.T) {
		input := []byte(`{"model":"union-alpha","max_completion_tokens":2048,"messages":[{"role":"user","content":"hi"}]}`)
		out := ensureMessagesMaxTokens(input, "union-alpha")
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if m["max_tokens"] != float64(2048) {
			t.Errorf("expected max_tokens 2048, got %v", m["max_tokens"])
		}
	})

	t.Run("existing positive max_tokens preserved", func(t *testing.T) {
		input := []byte(`{"model":"union-alpha","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`)
		out := ensureMessagesMaxTokens(input, "union-alpha")
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if m["max_tokens"] != float64(1024) {
			t.Errorf("expected max_tokens 1024, got %v", m["max_tokens"])
		}
	})
}

func TestForwardOpencode_UnionAlpha_InjectsMaxTokens(t *testing.T) {
	var capturedHeader string
	var capturedBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/messages") {
			t.Errorf("expected /messages path, got %s", r.URL.Path)
		}
		capturedHeader = r.Header.Get("anthropic-version")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"msg_ua","type":"message","role":"assistant","content":[{"type":"text","text":"hello from union-alpha"}]}`))
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{BaseURL: srv.URL + "/chat/completions"}
	rec := httptest.NewRecorder()
	req := &Request{
		Client: srv.Client(),
		Config: cfg,
		APIKey: "test-key",
		Body:   []byte(`{"model":"ag/union-alpha","messages":[{"role":"user","content":"hi"}]}`),
		IsStream: false,
	}

	if err := ForwardOpencode(rec, req); err != nil {
		t.Fatalf("ForwardOpencode union-alpha failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if capturedHeader != "2023-06-01" {
		t.Errorf("expected anthropic-version 2023-06-01, got %s", capturedHeader)
	}
	if capturedBody["model"] != "union-alpha" {
		t.Errorf("expected model 'union-alpha', got %v", capturedBody["model"])
	}
	if capturedBody["max_tokens"] != float64(4096) {
		t.Errorf("expected max_tokens 4096 injected, got %v", capturedBody["max_tokens"])
	}
}

func TestEnsureMessagesMaxTokens_ConvertsOpenAIToolsAndSystem(t *testing.T) {
	inputJSON := []byte(`{
		"model": "combo-wombo",
		"messages": [
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": "What is the weather?"}
		],
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "read",
					"description": "Read file",
					"parameters": {
						"type": "object",
						"properties": {
							"path": {"type": "string"}
						},
						"required": ["path"]
					}
				}
			}
		],
		"tool_choice": "auto",
		"max_completion_tokens": 16384,
		"stream_options": {"include_usage": true},
		"store": false,
		"reasoning_effort": "xhigh"
	}`)

	out := ensureMessagesMaxTokens(inputJSON, "union-alpha")
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// 1. Model normalized
	if m["model"] != "union-alpha" {
		t.Errorf("expected model 'union-alpha', got %v", m["model"])
	}

	// 2. max_tokens converted from max_completion_tokens
	if m["max_tokens"] != float64(16384) {
		t.Errorf("expected max_tokens 16384, got %v", m["max_tokens"])
	}
	if _, hasMCT := m["max_completion_tokens"]; hasMCT {
		t.Error("max_completion_tokens should be stripped")
	}

	// 3. System prompt extracted to top level
	if m["system"] != "You are a helpful assistant." {
		t.Errorf("expected system prompt at top level, got %v", m["system"])
	}

	// 4. Messages only contains user message
	msgs, ok := m["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("expected 1 message in messages array, got %v", m["messages"])
	}
	m0 := msgs[0].(map[string]any)
	if m0["role"] != "user" {
		t.Errorf("expected role 'user', got %v", m0["role"])
	}

	// 5. Tools converted to Claude format: must have "name" and "input_schema"
	tools, ok := m["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %v", m["tools"])
	}
	t0 := tools[0].(map[string]any)
	if t0["name"] != "read" {
		t.Errorf("expected tool name 'read', got %v", t0["name"])
	}
	if t0["description"] != "Read file" {
		t.Errorf("expected tool description 'Read file', got %v", t0["description"])
	}
	if _, hasSchema := t0["input_schema"]; !hasSchema {
		t.Error("expected input_schema on tool[0]")
	}
	if _, hasFunc := t0["function"]; hasFunc {
		t.Error("function wrapper should be removed from tool[0]")
	}
	if _, hasType := t0["type"]; hasType {
		t.Error("type: function should be removed from tool[0]")
	}

	// 6. tool_choice converted to Claude object
	tc, ok := m["tool_choice"].(map[string]any)
	if !ok || tc["type"] != "auto" {
		t.Errorf("expected tool_choice {\"type\": \"auto\"}, got %v", m["tool_choice"])
	}

	// 7. OpenAI-specific fields stripped
	if _, ok := m["stream_options"]; ok {
		t.Error("stream_options should be stripped")
	}
	if _, ok := m["store"]; ok {
		t.Error("store should be stripped")
	}
	if _, ok := m["reasoning_effort"]; ok {
		t.Error("reasoning_effort should be stripped")
	}
}

func TestForwardOpencode_UnionAlpha_ConvertsOpenAIToolsInRequest(t *testing.T) {
	var capturedBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"msg_ua","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}]}`))
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{BaseURL: srv.URL + "/chat/completions"}
	rec := httptest.NewRecorder()
	req := &Request{
		Client: srv.Client(),
		Config: cfg,
		APIKey: "test-key",
		Body: []byte(`{
			"model": "oc/union-alpha",
			"messages": [
				{"role": "system", "content": "system instruction"},
				{"role": "user", "content": "hi"}
			],
			"tools": [
				{
					"type": "function",
					"function": {
						"name": "read",
						"parameters": {"type": "object"}
					}
				}
			]
		}`),
		IsStream: false,
	}

	if err := ForwardOpencode(rec, req); err != nil {
		t.Fatalf("ForwardOpencode failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	// Upstream received Claude-formatted tools
	tools := capturedBody["tools"].([]any)
	t0 := tools[0].(map[string]any)
	if t0["name"] != "read" || t0["input_schema"] == nil {
		t.Errorf("expected tool with name and input_schema, got %+v", t0)
	}
	if capturedBody["system"] != "system instruction" {
		t.Errorf("expected top-level system, got %v", capturedBody["system"])
	}
}

func TestBuildResponsesBody_StringInput(t *testing.T) {
	body := []byte(`{
		"model": "opencode/muse-spark-1.3-contributor-free",
		"input": "Say hello in one sentence.",
		"max_output_tokens": 100,
		"stream": true
	}`)

	out, cleanModel, err := buildResponsesBody(body)
	if err != nil {
		t.Fatalf("buildResponsesBody failed: %v", err)
	}
	if cleanModel != "muse-spark-1.3-contributor-free" {
		t.Errorf("expected cleanModel 'muse-spark-1.3-contributor-free', got %q", cleanModel)
	}

	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	inputList, ok := parsed["input"].([]any)
	if !ok || len(inputList) == 0 {
		t.Fatalf("expected non-empty input array, got %v", parsed["input"])
	}
	msg := inputList[0].(map[string]any)
	if msg["role"] != "user" {
		t.Errorf("expected role user, got %v", msg["role"])
	}
	content := msg["content"].([]any)
	c0 := content[0].(map[string]any)
	if c0["text"] != "Say hello in one sentence." {
		t.Errorf("expected text 'Say hello in one sentence.', got %v", c0["text"])
	}
}

func TestBuildResponsesBody_EmptyArrayInput(t *testing.T) {
	body := []byte(`{
		"model": "muse-spark-1.3",
		"input": [],
		"max_output_tokens": 100
	}`)

	out, _, err := buildResponsesBody(body)
	if err != nil {
		t.Fatalf("buildResponsesBody failed: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	inputList, ok := parsed["input"].([]any)
	if !ok || len(inputList) == 0 {
		t.Fatalf("expected non-empty placeholder input array, got %v", parsed["input"])
	}
}
