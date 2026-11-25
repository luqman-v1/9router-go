package executor

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
)

const kiroCRTestSSE = `data: {"type":"response.output_text.delta","delta":"hello"}

data: {"type":"response.reasoning_text.delta","delta":"think"}

data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":10,"output_tokens":3}}}

data: [DONE]

`

// kiroCRTestEnv wires legacy and CreateResponse to one httptest server:
// legacy Kiro hits the root path, CreateResponse hits /v1/responses.
type kiroCRTestEnv struct {
	srv          *httptest.Server
	legacyCalls  int
	createCalls  int
	legacyStatus int
	legacyBody   string
	createStatus int
	createSSE    string
}

func newKiroCRTestEnv(t *testing.T) *kiroCRTestEnv {
	t.Helper()
	env := &kiroCRTestEnv{legacyStatus: http.StatusOK, createStatus: http.StatusOK, createSSE: kiroCRTestSSE}
	env.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/responses" {
			env.createCalls++
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(env.createStatus)
			fmt.Fprint(w, env.createSSE)
			return
		}
		env.legacyCalls++
		w.WriteHeader(env.legacyStatus)
		fmt.Fprint(w, env.legacyBody)
	}))
	t.Cleanup(env.srv.Close)
	return env
}

func (env *kiroCRTestEnv) req(stream bool) *Request {
	return &Request{
		Ctx:       context.Background(),
		Client:    env.srv.Client(),
		Config:    &providers.ProviderConfig{BaseURL: env.srv.URL},
		APIKey:    "test-token",
		Body:      []byte(`{"conversationState":{"currentMessage":{"userInputMessage":{"content":"hi","modelId":"claude-sonnet-4.5"}},"history":[]}}`),
		IsStream:  stream,
		ModelName: "claude-sonnet-4.5",
		ConnData:  map[string]any{"region": "us-east-1"},
	}
}

func setupKiroCR(t *testing.T, env *kiroCRTestEnv) {
	t.Helper()
	oldURL, oldDelay := kiroCreateResponseURL, kiroCreateResponseRetryDelay
	kiroCreateResponseURL = env.srv.URL + "/v1/responses"
	kiroCreateResponseRetryDelay = 0
	t.Cleanup(func() {
		kiroCreateResponseURL = oldURL
		kiroCreateResponseRetryDelay = oldDelay
	})
}

func TestForwardKiroCreateResponseAutoFallback(t *testing.T) {
	env := newKiroCRTestEnv(t)
	setupKiroCR(t, env)
	t.Setenv("NINEROUTER_KIRO_CREATE_RESPONSE", "auto")
	env.legacyStatus = http.StatusTooManyRequests
	env.legacyBody = `{"message":"USER_REQUEST_RATE_EXCEEDED"}`

	rec := httptest.NewRecorder()
	if err := ForwardKiro(rec, env.req(true)); err != nil {
		t.Fatalf("ForwardKiro: %v", err)
	}
	out := rec.Body.String()
	if env.legacyCalls != 1 || env.createCalls != 1 {
		t.Fatalf("legacy calls = %d, create calls = %d; want 1 and 1", env.legacyCalls, env.createCalls)
	}
	for _, want := range []string{`"content":"hello"`, `"reasoning_content":"think"`, `"finish_reason":"stop"`, "data: [DONE]"} {
		if !strings.Contains(out, want) {
			t.Errorf("client SSE missing %q: %s", want, out)
		}
	}
	if strings.Count(out, "data: [DONE]") != 1 {
		t.Errorf("expected exactly one [DONE], got %d", strings.Count(out, "data: [DONE]"))
	}
}

func TestForwardKiroCreateResponseAutoGeneric429NoFallback(t *testing.T) {
	env := newKiroCRTestEnv(t)
	setupKiroCR(t, env)
	t.Setenv("NINEROUTER_KIRO_CREATE_RESPONSE", "auto")
	env.legacyStatus = http.StatusTooManyRequests
	env.legacyBody = `{"message":"rate limited"}`

	err := ForwardKiro(httptest.NewRecorder(), env.req(true))
	if err == nil {
		t.Fatal("expected original 429 error")
	}
	if env.createCalls != 0 {
		t.Errorf("generic 429 must not call CreateResponse, got %d calls", env.createCalls)
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error lost original status: %v", err)
	}
}

func TestForwardKiroCreateResponseAutoNonOKPassthrough(t *testing.T) {
	env := newKiroCRTestEnv(t)
	setupKiroCR(t, env)
	t.Setenv("NINEROUTER_KIRO_CREATE_RESPONSE", "auto")
	env.legacyStatus = http.StatusTooManyRequests
	env.legacyBody = `{"message":"USER_REQUEST_RATE_EXCEEDED"}`
	env.createStatus = http.StatusForbidden
	env.createSSE = `{"message":"bearer token expired"}`

	err := ForwardKiro(httptest.NewRecorder(), env.req(true))
	if err == nil {
		t.Fatal("expected CreateResponse error")
	}
	if env.createCalls != 1 {
		t.Errorf("create calls = %d, want 1", env.createCalls)
	}
	if !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "bearer token expired") {
		t.Errorf("error must carry the CreateResponse status and body, got %v", err)
	}
}

func TestForwardKiroCreateResponseOffNoFallback(t *testing.T) {
	env := newKiroCRTestEnv(t)
	setupKiroCR(t, env)
	t.Setenv("NINEROUTER_KIRO_CREATE_RESPONSE", "off")
	env.legacyStatus = http.StatusTooManyRequests
	env.legacyBody = `{"message":"USER_REQUEST_RATE_EXCEEDED"}`

	err := ForwardKiro(httptest.NewRecorder(), env.req(true))
	if err == nil {
		t.Fatal("expected original error")
	}
	if env.createCalls != 0 {
		t.Errorf("off mode must not call CreateResponse, got %d calls", env.createCalls)
	}
}

func TestForwardKiroCreateResponseAlwaysSkipsLegacy(t *testing.T) {
	env := newKiroCRTestEnv(t)
	setupKiroCR(t, env)
	t.Setenv("NINEROUTER_KIRO_CREATE_RESPONSE", "always")

	rec := httptest.NewRecorder()
	if err := ForwardKiro(rec, env.req(true)); err != nil {
		t.Fatalf("ForwardKiro: %v", err)
	}
	if env.legacyCalls != 0 {
		t.Errorf("always mode must not call legacy, got %d calls", env.legacyCalls)
	}
	if env.createCalls != 1 {
		t.Errorf("always mode must call CreateResponse once, got %d", env.createCalls)
	}
	if !strings.Contains(rec.Body.String(), "data: [DONE]") {
		t.Errorf("missing DONE: %s", rec.Body.String())
	}
}

func TestForwardKiroCreateResponseNonStreamFold(t *testing.T) {
	env := newKiroCRTestEnv(t)
	setupKiroCR(t, env)
	t.Setenv("NINEROUTER_KIRO_CREATE_RESPONSE", "always")

	rec := httptest.NewRecorder()
	req := env.req(false)
	if err := ForwardKiro(rec, req); err != nil {
		t.Fatalf("ForwardKiro: %v", err)
	}
	out := rec.Body.String()
	if !strings.Contains(out, `"object":"chat.completion"`) {
		t.Errorf("expected folded chat.completion JSON, got %s", out)
	}
	if !strings.Contains(out, "hello") {
		t.Errorf("folded response missing content: %s", out)
	}
}

func TestBuildKiroCreateResponseBody(t *testing.T) {
	body := []byte(`{
		"conversationState":{
			"conversationId":"conv-1",
			"history":[{"assistantResponseMessage":{"content":"earlier","toolUses":[{"toolUseId":"tu-1","name":"read","input":{"path":"/tmp"}}]}}],
			"currentMessage":{"userInputMessage":{
				"content":"current question",
				"modelId":"model-from-body",
				"origin":"CUSTOM_ORIGIN",
				"userInputMessageContext":{
					"tools":[{"toolSpecification":{"name":"calc","description":"do math","inputSchema":{"json":{"type":"object"}}}}],
					"toolResults":[{"toolUseId":"tu-2","content":[{"text":"result line 1"},{"text":"result line 2"}]}]
				},
				"images":[{"format":"JPEG","source":{"bytes":"aGVsbG8="}}]
			}}
		},
		"additionalModelRequestFields":{"thinking":{"type":"enabled","display":"summarized"},"reasoning":{"effort":"high"}},
		"profileArn":"arn:profile:from-body"
	}`)
	psd := map[string]any{"profileArn": "arn:profile:from-psd"}

	out, err := buildKiroCreateResponseBody(body, "arg-model", psd)
	if err != nil {
		t.Fatalf("build body: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("parse output: %v", err)
	}
	if m["model"] != "model-from-body" {
		t.Errorf("model = %v, want model-from-body", m["model"])
	}
	if m["origin"] != "CUSTOM_ORIGIN" {
		t.Errorf("origin = %v", m["origin"])
	}
	if m["profileArn"] != "arn:profile:from-body" {
		t.Errorf("profileArn = %v", m["profileArn"])
	}
	if m["stream"] != true {
		t.Errorf("stream = %v, want true", m["stream"])
	}
	meta, _ := m["metadata"].(map[string]any)
	if meta["kiro:agent_mode"] != "vibe" || meta["kiro:conversation_id"] != "conv-1" {
		t.Errorf("metadata = %v", meta)
	}
	tools, _ := m["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %v, want one", tools)
	}
	tool, _ := tools[0].(map[string]any)
	if tool["name"] != "calc" || tool["type"] != "function" {
		t.Errorf("tool mapping = %v", tool)
	}
	input, _ := m["input"].([]any)
	foundOutput, foundImage, foundToolCall := false, false, false
	for _, raw := range input {
		item, _ := raw.(map[string]any)
		switch item["type"] {
		case "function_call_output":
			foundOutput = item["call_id"] == "tu-2" && item["output"] == "result line 1\nresult line 2"
		case "function_call":
			foundToolCall = item["name"] == "read" && item["call_id"] == "tu-1"
		}
		if content, ok := item["content"].([]any); ok {
			for _, part := range content {
				p, _ := part.(map[string]any)
				if p["type"] == "input_image" {
					foundImage = strings.HasPrefix(fmt.Sprint(p["image_url"]), "data:image/jpeg;base64,")
				}
			}
		}
	}
	if !foundOutput || !foundImage || !foundToolCall {
		t.Errorf("input missing mapped items: %s", out)
	}
	reasoning, _ := m["reasoning"].(map[string]any)
	if reasoning["type"] != "enabled" || reasoning["summary"] != "summarized" || reasoning["effort"] != "high" {
		t.Errorf("reasoning = %v", reasoning)
	}
}

func TestBuildKiroCreateResponseHeaders(t *testing.T) {
	psd := map[string]any{"authMethod": "api_key", "profileArn": "arn:p", "apiKey": "key"}
	h := buildKiroCreateResponseHeaders("access-token", psd, []byte(`{}`))
	for k, want := range map[string]string{
		"Content-Type":                "application/json",
		"Authorization":               "Bearer access-token",
		"x-amzn-kiro-origin":          "AI_EDITOR",
		"x-amzn-codewhisperer-optout": "true",
		"User-Agent":                  "KiroIDE-1.4.154-KiroIDE/acp-client",
		"x-amzn-kiro-attempt":         "1;max=3",
		"x-amzn-kiro-profile":         "arn:p",
		"TokenType":                   "API_KEY",
		"x-amz-sso-bearer":            "access-token",
	} {
		if h[k] != want {
			t.Errorf("header %s = %q, want %q", k, h[k], want)
		}
	}
}

func TestKiroCreateResponseEndpointValidation(t *testing.T) {
	tests := map[string]string{
		"us-east-1":    "https://runtime.us-east-1.kiro.dev/v1/responses",
		"eu-central-1": "https://runtime.eu-central-1.kiro.dev/v1/responses",
		"evil.host":    "https://runtime.us-east-1.kiro.dev/v1/responses",
		"":             "https://runtime.us-east-1.kiro.dev/v1/responses",
	}
	for region, want := range tests {
		if got := kiroCreateResponseEndpoint(region); got != want {
			t.Errorf("endpoint(%q) = %q, want %q", region, got, want)
		}
	}
}

func TestKiroCreateResponseRetryOn429(t *testing.T) {
	env := newKiroCRTestEnv(t)
	setupKiroCR(t, env)
	oldDelay := kiroCreateResponseRetryDelay
	kiroCreateResponseRetryDelay = 0
	t.Cleanup(func() { kiroCreateResponseRetryDelay = oldDelay })

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, kiroCRTestSSE)
	}))
	defer srv.Close()

	body := []byte(`{"conversationState":{"currentMessage":{}}}`)
	crBody, err := buildKiroCreateResponseBody(body, "m", nil)
	if err != nil {
		t.Fatal(err)
	}
	headers := buildKiroCreateResponseHeaders("tok", nil, crBody)
	resp, err := executeKiroCreateResponse(context.Background(), srv.Client(), srv.URL, headers, crBody)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	defer resp.Body.Close()
	if calls != 2 {
		t.Errorf("expected 2 calls (one retry), got %d", calls)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
}
