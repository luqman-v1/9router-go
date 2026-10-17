package executor

import (
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
		if r.Header.Get("x-opencode-project") != "global" {
			t.Errorf("expected x-opencode-project global like upstream, got %s", r.Header.Get("x-opencode-project"))
		}
		if !strings.HasPrefix(r.Header.Get("x-opencode-session"), "ses_") {
			t.Errorf("expected ses_ prefix on x-opencode-session, got %s", r.Header.Get("x-opencode-session"))
		}
		if r.Header.Get("x-opencode-request") != "" {
			t.Errorf("v2 request must omit x-opencode-request, got %q", r.Header.Get("x-opencode-request"))
		}
		if r.Header.Get("x-opencode-session-id") != r.Header.Get("x-opencode-session") || r.Header.Get("x-session-affinity") != r.Header.Get("x-opencode-session") || r.Header.Get("x-session-id") != r.Header.Get("x-opencode-session") {
			t.Errorf("session affinity headers do not match: %#v", r.Header)
		}
		if r.Header.Get("User-Agent") != proxy.DefaultOpenCodeUA {
			t.Errorf("expected User-Agent %s, got %s", proxy.DefaultOpenCodeUA, r.Header.Get("User-Agent"))
		}
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		if err := json.Unmarshal(body, &parsed); err == nil {
			if parsed["tool_choice"] == "none" {
				t.Errorf("OpenCode v2 rejects tool_choice none: %s", body)
			}
			tools, _ := parsed["tools"].([]any)
			for _, name := range []string{"read", "shell"} {
				found := false
				for _, tool := range tools {
					tm, _ := tool.(map[string]any)
					fn, _ := tm["function"].(map[string]any)
					if fn["name"] == name {
						found = true
					}
				}
				if !found {
					t.Errorf("missing v2 fingerprint tool %q: %s", name, body)
				}
			}
		}
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

// A harness body — Claude-Code-style capitalised tools, a forced tool_choice,
// a tool without a schema, and a foreign cache_control key — is canonicalized
// into exactly what the v2 inference gate accepts, so the same model works
// whether the caller is a clean API client or an agent harness
// (decolua/9router#4842 gate rules).
func TestForwardOpencode_HarnessBodyIsCanonicalizedForTheGate(t *testing.T) {
	var sent map[string]any
	var sentRaw string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		sentRaw = string(raw)
		_ = json.Unmarshal(raw, &sent)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"ok"}}]}` + "\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{
		BaseURL: srv.URL + "/zen/v1/chat/completions",
	}

	rec := httptest.NewRecorder()
	req := &Request{
		Client: srv.Client(),
		Config: cfg,
		APIKey: "",
		Body: []byte(`{"model":"deepseek-v4-flash-free","messages":[{"role":"user","content":"hi"}],` +
			`"tools":[{"type":"function","function":{"name":"Read","description":"caller Read"}},` +
			`{"type":"function","function":{"name":"Task","description":"t","parameters":{"type":"object","properties":{"path":{"type":"string"}},"cache_control":{"type":"ephemeral"}}}}],` +
			`"tool_choice":{"type":"function","function":{"name":"Task"}},"parallel_tool_calls":false}`),
		IsStream:      false,
		TranslateResp: false,
	}

	if err := ForwardOpencode(rec, req); err != nil {
		t.Fatalf("ForwardOpencode failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if sent == nil {
		t.Fatal("upstream received no body")
	}
	tools, _ := sent["tools"].([]any)
	if len(tools) != 4 {
		t.Fatalf("want caller Read+Task plus injected read+shell, got %d: %s", len(tools), sentRaw)
	}
	names := map[string]bool{}
	for _, tool := range tools {
		tm, _ := tool.(map[string]any)
		fn, _ := tm["function"].(map[string]any)
		if fn == nil {
			t.Errorf("chat lane lost the .function wrapper: %s", sentRaw)
			continue
		}
		name, _ := fn["name"].(string)
		names[name] = true
		params, _ := fn["parameters"].(map[string]any)
		if params == nil || params["type"] != "object" {
			t.Errorf("tool %q reached the gate without an object schema: %s", name, sentRaw)
		}
	}
	for _, want := range []string{"Read", "Task", "read", "shell"} {
		if !names[want] {
			t.Errorf("tool %q missing from the gate request: %s", want, sentRaw)
		}
	}
	choice, ok := sent["tool_choice"].(map[string]any)
	if !ok {
		t.Fatalf("forced tool_choice must survive on the chat lane, got %v: %s", sent["tool_choice"], sentRaw)
	}
	if fn, _ := choice["function"].(map[string]any); fn["name"] != "Task" {
		t.Errorf("forced choice name=%v, want Task: %s", fn["name"], sentRaw)
	}
}

func TestForwardOpencode_MuseSpark_EdgeRelay(t *testing.T) {
	var gotTarget, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTarget = r.Header.Get("x-relay-target")
		gotPath = r.Header.Get("x-relay-path")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// buildResponsesBody forces stream:true, so the /responses upstream
		// answers with an event stream even for a non-streaming client.
		w.Write([]byte(responsesEventStream("relay ok")))
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{
		BaseURL: srv.URL, // local fake upstream; leave its path intact
		StaticHeaders: map[string]string{
			"x-relay-target": "https://opencode.ai",
			"x-relay-path":   "/zen/v1/chat/completions",
		},
	}

	rec := httptest.NewRecorder()
	req := &Request{
		Client:        srv.Client(),
		Config:        cfg,
		APIKey:        "public",
		Body:          []byte(`{"model":"muse-spark-1.3-contributor-free","input":"test"}`),
		IsStream:      false,
		TranslateResp: false,
	}

	err := ForwardOpencode(rec, req)
	if err != nil {
		t.Fatalf("ForwardOpencode failed: %v", err)
	}
	if gotTarget != "https://opencode.ai" {
		t.Errorf("expected x-relay-target 'https://opencode.ai', got %q", gotTarget)
	}
	if gotPath != "/inference/v1/responses" {
		t.Errorf("expected x-relay-path '/inference/v1/responses', got %q", gotPath)
	}
}

func TestForwardOpencodeGo_OpenAIRouting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-go-key" {
			t.Errorf("expected Authorization Bearer secret-go-key header, got %s", r.Header.Get("Authorization"))
		}
		// The paid go lane keeps the v1 identity: only the keyless free
		// executor moved to v2 (upstream decolua/9router#4842 leaves keyed
		// executors untouched).
		if r.Header.Get("User-Agent") != proxy.DefaultOpenCodeV1UA {
			t.Errorf("expected paid-lane User-Agent %s, got %s", proxy.DefaultOpenCodeV1UA, r.Header.Get("User-Agent"))
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
		if !strings.HasSuffix(r.URL.Path, "/inference/v1/responses") {
			t.Errorf("expected the zen base to be rewritten to the inference gate, got %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if parsed["input"] == nil {
			t.Errorf("expected input array in Responses API format, got: %s", string(body))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(responsesEventStream("4")))
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{
		BaseURL: srv.URL + "/zen/v1/chat/completions",
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
			t.Errorf("expected responses endpoint, got %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		_ = json.Unmarshal(body, &parsed)
		if parsed["model"] != "muse-spark-1.3-contributor-free" {
			t.Errorf("expected model muse-spark-1.3, got %v", parsed["model"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(responsesEventStream("ok 1.3")))
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{BaseURL: srv.URL + "/chat/completions"}
	rec := httptest.NewRecorder()
	req := &Request{
		Client: srv.Client(), Config: cfg, APIKey: "",
		Body:     []byte(`{"model":"oc/muse-spark-1.3-contributor-free","messages":[{"role":"user","content":"hi"}],"stream":false}`),
		IsStream: false, TranslateResp: false,
	}
	if err := ForwardOpencode(rec, req); err != nil {
		t.Fatalf("1.3 failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestOpenCodeInferenceBase(t *testing.T) {
	tests := []struct{ input, want string }{
		{"https://opencode.ai/zen/v1/chat/completions", "https://opencode.ai/inference/v1"},
		{"https://opencode.ai/zen/v1", "https://opencode.ai/inference/v1"},
		{"http://127.0.0.1:9000/zen/v1/chat/completions", "http://127.0.0.1:9000/inference/v1"},
		{"http://127.0.0.1:9000", "http://127.0.0.1:9000"},
		{"https://relay.example/api", "https://relay.example/api"},
		{"https://opencode.ai/inference/v1", "https://opencode.ai/inference/v1"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := opencodeInferenceBase(tt.input); got != tt.want {
				t.Errorf("opencodeInferenceBase(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestIsOpencodeResponsesModel(t *testing.T) {
	// Ports the upstream free-executor lane rule (isResponsesModel = curated
	// set plus isMuseSparkModel /^muse[-_]?spark(?:$|[-_:.\s])/i).
	tests := []struct {
		model string
		want  bool
	}{
		{"muse-spark-1.2-contributor-free", true},
		{"muse-spark-1.3-contributor-free", true},
		{"muse-spark-1.3-contributor-free(xhigh)", true},
		{"muse-spark-1.4-contributor-free", true},
		{"muse-spark-2.0-contributor-free", true},
		{"jev-1.13-free", false},
		{"space-bunny-free", false},
		{"big-pickle", false},
		{"grok-4.6", false},
		{"gpt-5.6-luna", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			if got := isOpencodeResponsesModel(tt.model); got != tt.want {
				t.Errorf("isOpencodeResponsesModel(%q) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}

func TestIsOpencodeGoResponsesModel(t *testing.T) {
	// The paid go lane keeps its pre-v2 predicate: narrowing it to the free
	// ids would silently move paid traffic to the chat lane.
	tests := []struct {
		model string
		want  bool
	}{
		{"muse-spark-1.2-contributor", true},
		{"muse-spark-1.3-contributor", true},
		{"muse-spark-1.2-contributor-free", true},
		{"grok-4.6", true},
		{"gpt-5.6-luna", true},
		{"space-bunny-free", false},
		{"glm-5.2", false},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			if got := isOpencodeGoResponsesModel(tt.model); got != tt.want {
				t.Errorf("isOpencodeGoResponsesModel(%q) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}

func TestIsOpencodeFreeModel(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"big-pickle", false},
		{"big-pickle(high)", false},
		{"longcat-2.5-preview-free", true},
		{"longcat-2.5-preview-free(thinking)", true},
		{"deepseek-v4-flash-free", true},
		{"muse-spark-1.3-contributor-free", true},
		{"space-bunny-free", true},
		{"jev-1.13-free", true},
		{"deepseek-v4-pro", false},
		{"grok-4.6", false},
		{"gpt-5.6-luna", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			if got := isOpencodeFreeModel(tt.model); got != tt.want {
				t.Errorf("isOpencodeFreeModel(%q) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}

// A paid go Muse Spark model must stay on the go Responses lane with the v1
// identity — the free-executor narrowing must never leak into this lane.
func TestForwardOpencodeGo_MuseSparkResponsesRouting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/zen/go/v1/responses") {
			t.Errorf("expected paid go responses lane, got %s", r.URL.Path)
		}
		if r.Header.Get("User-Agent") != proxy.DefaultOpenCodeV1UA {
			t.Errorf("expected paid-lane User-Agent %s, got %s", proxy.DefaultOpenCodeV1UA, r.Header.Get("User-Agent"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(responsesEventStream("go spark ok")))
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{BaseURL: srv.URL + "/zen/go/v1/chat/completions"}
	rec := httptest.NewRecorder()
	req := &Request{
		Client: srv.Client(), Config: cfg, APIKey: "secret-go-key",
		Body:     []byte(`{"model":"muse-spark-1.2-contributor","messages":[{"role":"user","content":"hi"}]}`),
		IsStream: false, TranslateResp: false,
	}
	if err := ForwardOpencodeGo(rec, req); err != nil {
		t.Fatalf("ForwardOpencodeGo failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
}

// space-bunny-free 403s on the keyed go lane, so it takes the keyless
// inference chat lane with public credentials — never the user's go key.
func TestForwardOpencodeGo_SpaceBunnyFreeTakesInferenceGate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/inference/v1/chat/completions") {
			t.Errorf("expected inference chat lane, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer public" {
			t.Errorf("expected Bearer public, got %s", r.Header.Get("Authorization"))
		}
		if r.Header.Get("User-Agent") != proxy.DefaultOpenCodeUA {
			t.Errorf("expected v2 User-Agent %s, got %s", proxy.DefaultOpenCodeUA, r.Header.Get("User-Agent"))
		}
		if r.Header.Get("x-opencode-client") != "cli" {
			t.Errorf("expected cli client header, got %s", r.Header.Get("x-opencode-client"))
		}
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if parsed["model"] != "space-bunny-free" {
			t.Errorf("expected bare model id, got %v", parsed["model"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"bunny"}}]}` + "\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{BaseURL: srv.URL + "/zen/go/v1/chat/completions"}
	rec := httptest.NewRecorder()
	req := &Request{
		Client: srv.Client(), Config: cfg, APIKey: "secret-go-key",
		Body:     []byte(`{"model":"opencode-go/space-bunny-free","messages":[{"role":"user","content":"hi"}]}`),
		IsStream: false, TranslateResp: false,
	}
	if err := ForwardOpencodeGo(rec, req); err != nil {
		t.Fatalf("ForwardOpencodeGo failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
}

func TestForwardOpencodeGo_GeminiWire(t *testing.T) {
	var capturedPath string
	var capturedAPIKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path + "?" + r.URL.RawQuery
		capturedAPIKey = r.Header.Get("x-goog-api-key")

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"candidates": [{
				"content": {
					"parts": [{"text": "Hello from Gemini 4 Argon on OpenCode Go"}],
					"role": "model"
				},
				"finishReason": "STOP",
				"index": 0
			}],
			"usageMetadata": {
				"promptTokenCount": 5,
				"candidatesTokenCount": 8,
				"totalTokenCount": 13
			}
		}`))
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{BaseURL: srv.URL + "/inference/go/v1/models"}
	rec := httptest.NewRecorder()
	req := &Request{
		Client: srv.Client(), Config: cfg, APIKey: "test-goog-key",
		Body:     []byte(`{"model":"opencode-go/gemini-4-argon-contributor","messages":[{"role":"user","content":"hi"}]}`),
		IsStream: false, TranslateResp: false,
	}
	if err := ForwardOpencodeGo(rec, req); err != nil {
		t.Fatalf("ForwardOpencodeGo failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
	if !strings.Contains(capturedPath, "/gemini-4-argon-contributor:generateContent") {
		t.Errorf("expected path to contain /gemini-4-argon-contributor:generateContent, got %s", capturedPath)
	}
	if capturedAPIKey != "test-goog-key" {
		t.Errorf("expected x-goog-api-key test-goog-key, got %s", capturedAPIKey)
	}
	if !strings.Contains(rec.Body.String(), "Hello from Gemini 4 Argon on OpenCode Go") {
		t.Errorf("expected response to contain translated text, got %s", rec.Body.String())
	}
}

func TestForwardOpencode_MuseSpark_OCPrefix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			t.Errorf("expected /responses for oc/ prefix, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(responsesEventStream("ok")))
	}))
	defer srv.Close()
	cfg := &providers.ProviderConfig{BaseURL: srv.URL + "/chat/completions"}
	rec := httptest.NewRecorder()
	req := &Request{
		Client: srv.Client(), Config: cfg, APIKey: "",
		Body:     []byte(`{"model":"oc/muse-spark-1.3-contributor-free","messages":[{"role":"user","content":"hi"}]}`),
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
		input := []byte(`{"model":"oc/claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`)
		out := ensureMessagesMaxTokens(input, "claude-sonnet-4-5")
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if m["model"] != "claude-sonnet-4-5" {
			t.Errorf("expected model 'claude-sonnet-4-5', got %v", m["model"])
		}
		if m["max_tokens"] != float64(4096) {
			t.Errorf("expected max_tokens 4096, got %v", m["max_tokens"])
		}
	})

	t.Run("max_tokens <= 0 defaults to 4096", func(t *testing.T) {
		input := []byte(`{"model":"claude-sonnet-4-5","max_tokens":0,"messages":[{"role":"user","content":"hi"}]}`)
		out := ensureMessagesMaxTokens(input, "claude-sonnet-4-5")
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if m["max_tokens"] != float64(4096) {
			t.Errorf("expected max_tokens 4096, got %v", m["max_tokens"])
		}
	})

	t.Run("max_completion_tokens used when max_tokens missing", func(t *testing.T) {
		input := []byte(`{"model":"claude-sonnet-4-5","max_completion_tokens":2048,"messages":[{"role":"user","content":"hi"}]}`)
		out := ensureMessagesMaxTokens(input, "claude-sonnet-4-5")
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if m["max_tokens"] != float64(2048) {
			t.Errorf("expected max_tokens 2048, got %v", m["max_tokens"])
		}
	})

	t.Run("existing positive max_tokens preserved", func(t *testing.T) {
		input := []byte(`{"model":"claude-sonnet-4-5","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`)
		out := ensureMessagesMaxTokens(input, "claude-sonnet-4-5")
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if m["max_tokens"] != float64(1024) {
			t.Errorf("expected max_tokens 1024, got %v", m["max_tokens"])
		}
	})
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

	out := ensureMessagesMaxTokens(inputJSON, "claude-sonnet-4-5")
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// 1. Model normalized
	if m["model"] != "claude-sonnet-4-5" {
		t.Errorf("expected model 'claude-sonnet-4-5', got %v", m["model"])
	}

	// 2. max_tokens converted from max_completion_tokens, then raised to fit
	// the xhigh thinking budget (32768) plus answer headroom (1024).
	if m["max_tokens"] != float64(33792) {
		t.Errorf("expected max_tokens 33792, got %v", m["max_tokens"])
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

// responsesEventStream is what a /responses upstream sends for the request
// buildResponsesBody builds: stream:true is forced, so an event stream comes
// back even when the client asked for a single JSON answer.
func responsesEventStream(text string) string {
	return "event: response.output_text.delta\n" +
		`data: {"type":"response.output_text.delta","delta":"` + text + `"}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed"}` + "\n\n"
}
