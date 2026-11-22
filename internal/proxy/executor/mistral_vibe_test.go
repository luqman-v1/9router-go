package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
)

func TestNormalizeMistralVibeRequest(t *testing.T) {
	body := []byte(`{
		"model":"glm-5.3",
		"store":true,
		"stream":true,
		"max_completion_tokens":222,
		"seed":17,
		"user":"u-1",
		"messages":[
			{"role":"assistant","content":"hi","reasoning_content":"hidden","index":3},
			{"role":"tool","content":"result"}
		],
		"tools":[{"type":"function","function":{"name":"lookup"}}]
	}`)

	var chat map[string]any
	if err := json.Unmarshal(normalizeMistralVibeRequest(body), &chat); err != nil {
		t.Fatalf("decode normalized request: %v", err)
	}
	if got := chat["model"]; got != "zai-glm-5-3" {
		t.Errorf("model = %v, want canonical zai-glm-5-3", got)
	}
	if _, ok := chat["store"]; ok {
		t.Error("store should be removed")
	}
	if _, ok := chat["user"]; ok {
		t.Error("user should be removed")
	}
	if got := chat["random_seed"]; got != float64(17) {
		t.Errorf("random_seed = %v, want 17", got)
	}
	if _, ok := chat["seed"]; ok {
		t.Error("seed should be renamed away")
	}
	if got := chat["max_tokens"]; got != float64(222) {
		t.Errorf("max_tokens = %v, want 222 from max_completion_tokens", got)
	}
	if _, ok := chat["max_completion_tokens"]; ok {
		t.Error("max_completion_tokens should be renamed away")
	}
	if got := chat["reasoning_effort"]; got != "high" {
		t.Errorf("reasoning_effort = %v, want high for a GLM tool-result turn", got)
	}
	opts, ok := chat["stream_options"].(map[string]any)
	if !ok || opts["include_usage"] != true {
		t.Errorf("stream_options = %v, want include_usage true", chat["stream_options"])
	}
	messages := chat["messages"].([]any)
	first := messages[0].(map[string]any)
	if _, ok := first["reasoning_content"]; ok {
		t.Error("reasoning_content should be stripped from replayed messages")
	}
	if _, ok := first["index"]; ok {
		t.Error("index should be stripped from replayed messages")
	}
}

func TestNormalizeMistralVibeRequestDefaultBudget(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  float64
	}{
		{"glm-5.3", 131072},
		{"mistral-large-4", 32768},
	} {
		body := []byte(`{"model":"` + tc.model + `","messages":[]}`)
		var chat map[string]any
		if err := json.Unmarshal(normalizeMistralVibeRequest(body), &chat); err != nil {
			t.Fatalf("decode %s: %v", tc.model, err)
		}
		if got := chat["max_tokens"]; got != tc.want {
			t.Errorf("%s max_tokens = %v, want %v", tc.model, got, tc.want)
		}
	}
}

func TestNormalizeMistralVibeResponsesShape(t *testing.T) {
	body := []byte(`{"model":"glm-5.3","store":true,"input":"hi","reasoning":{"effort":"low"}}`)
	var chat map[string]any
	if err := json.Unmarshal(normalizeMistralVibeRequest(body), &chat); err != nil {
		t.Fatal(err)
	}
	if chat["model"] != "zai-glm-5-3" {
		t.Errorf("model = %v, want alias resolved", chat["model"])
	}
	if _, ok := chat["store"]; ok {
		t.Error("store should be removed on Responses requests too")
	}
	if _, ok := chat["max_tokens"]; ok {
		t.Error("chat-only max_tokens default must not be injected into a Responses request")
	}
}

func TestNormalizeMistralVibeCompletion(t *testing.T) {
	body := []byte(`{"choices":[{"finish_reason":"stop","message":{"content":[
		{"type":"thinking","thinking":[{"text":"let me think"}]},
		{"type":"text","text":"done "},
		{"type":"text","text":"now"}
	],"tool_calls":[{"id":"1"}]}}]}`)

	var completion map[string]any
	if err := json.Unmarshal(normalizeMistralVibeCompletion(body), &completion); err != nil {
		t.Fatalf("decode completion: %v", err)
	}
	choice := completion["choices"].([]any)[0].(map[string]any)
	message := choice["message"].(map[string]any)
	if got := message["content"]; got != "done now" {
		t.Errorf("content = %v, want flattened text", got)
	}
	if got := message["reasoning_content"]; got != "let me think" {
		t.Errorf("reasoning_content = %v, want flattened thinking", got)
	}
	if got := choice["finish_reason"]; got != "tool_calls" {
		t.Errorf("finish_reason = %v, want tool_calls", got)
	}
}

func TestMistralVibeStreamPatcher(t *testing.T) {
	patcher := &mistralVibeStreamPatcher{}
	first := patcher.Process([]byte(`{"choices":[{"delta":{"content":[{"type":"thinking","thinking":[{"text":"hmm"}]},{"type":"text","text":"hi"}],"tool_calls":[{"index":0}]},"finish_reason":""}]}`))
	var chunk map[string]any
	if err := json.Unmarshal(first, &chunk); err != nil {
		t.Fatalf("decode stream chunk: %v", err)
	}
	delta := chunk["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
	if delta["content"] != "hi" || delta["reasoning_content"] != "hmm" {
		t.Errorf("flattened delta = %v", delta)
	}
	last := patcher.Process([]byte(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`))
	if !strings.Contains(string(last), `"finish_reason":"tool_calls"`) {
		t.Errorf("terminal finish reason was not repaired: %s", last)
	}
}

func TestSnapMistralVibeReasoningEffort(t *testing.T) {
	body := []byte(`{"model":"mistral-large","reasoning_effort":"medium","messages":[]}`)
	detail := []byte(`{"error":{"message":"reasoning_effort 'medium' is not supported. Supported levels: 'none', 'high'"}}`)
	got, ok := snapMistralVibeReasoningEffort(body, detail)
	if !ok {
		t.Fatal("expected a rewritten body")
	}
	var chat map[string]any
	if err := json.Unmarshal(got, &chat); err != nil {
		t.Fatal(err)
	}
	if chat["reasoning_effort"] != "high" {
		t.Errorf("reasoning_effort = %v, want high", chat["reasoning_effort"])
	}

	dropped, ok := snapMistralVibeReasoningEffort([]byte(`{"model":"mistral-large-2512","reasoning_effort":"low"}`), []byte(`{"error":{"message":"reasoning_effort is not enabled for this model"}}`))
	if !ok || strings.Contains(string(dropped), "reasoning_effort") {
		t.Errorf("effort should be dropped for an unsupported model: %s", dropped)
	}
}

func TestForwardMistralRewritesAndSnapsOnVibeLane(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) == 1 {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"error":{"message":"reasoning_effort 'medium' is not supported. Supported: 'none', 'high'"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":[{"type":"text","text":"ok"}]},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	req := &Request{
		Ctx:       context.Background(),
		Client:    srv.Client(),
		Config:    &providers.ProviderConfig{BaseURL: srv.URL, AuthScheme: "bearer", AuthHeader: "Authorization"},
		Provider:  "mistral",
		APIKey:    "test",
		ConnData:  map[string]any{"usageLane": "vibe"},
		ModelName: "mistral-large",
		Body:      []byte(`{"model":"mistral-large","reasoning_effort":"medium","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`),
	}
	rec := httptest.NewRecorder()
	if err := ForwardMistral(rec, req); err != nil {
		t.Fatalf("ForwardMistral: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("upstream attempts = %d, want 2 (initial + snap retry)", len(bodies))
	}
	if !strings.Contains(bodies[1], `"reasoning_effort":"high"`) {
		t.Errorf("retry body did not snap the effort: %s", bodies[1])
	}
	if !strings.Contains(rec.Body.String(), `"content":"ok"`) {
		t.Errorf("GLM content parts were not normalized: %s", rec.Body.String())
	}
}

func TestForwardMistralLeavesAPILaneUntouched(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"plain"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	body := `{"model":"glm-5.3","store":true,"max_tokens":10,"messages":[]}`
	req := &Request{
		Ctx:      context.Background(),
		Client:   srv.Client(),
		Config:   &providers.ProviderConfig{BaseURL: srv.URL, AuthScheme: "bearer", AuthHeader: "Authorization"},
		Provider: "mistral",
		APIKey:   "test",
		ConnData: map[string]any{"usageLane": "api"},
		Body:     []byte(body),
	}
	if err := ForwardMistral(httptest.NewRecorder(), req); err != nil {
		t.Fatalf("ForwardMistral: %v", err)
	}
	var sent, want map[string]any
	if err := json.Unmarshal([]byte(gotBody), &sent); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(body), &want); err != nil {
		t.Fatal(err)
	}
	if len(sent) != len(want) {
		t.Fatalf("API lane fields changed: got %v want %v", sent, want)
	}
	for key, value := range want {
		if !reflect.DeepEqual(sent[key], value) {
			t.Errorf("API lane %s = %v, want %v", key, sent[key], value)
		}
	}
}
