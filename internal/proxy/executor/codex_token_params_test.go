package executor

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/providers"
)

// decodeBody is a small test helper: the executors work on raw JSON bytes, so
// the assertions below read the body back as a map rather than comparing
// serialised strings (key order is not stable).
func decodeBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	return parsed
}

// The ChatGPT Codex OAuth endpoint answers 400 "Unsupported parameter:
// max_output_tokens" for a body that carries one, so the value must not reach
// it. Clients (OpenCode, Cline, Orca) always send max_tokens on chat
// completions, and buildResponsesBody folds that into max_output_tokens.
func TestStripCodexUnsupportedTokenParams_FromChatCompletions(t *testing.T) {
	raw := []byte(`{
		"model": "gpt-5.6-terra",
		"max_tokens": 4096,
		"messages": [{"role": "user", "content": "hi"}]
	}`)

	body := stripCodexUnsupportedTokenParams(raw)
	parsed := decodeBody(t, body)

	for _, key := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
		if _, ok := parsed[key]; ok {
			t.Errorf("expected %q to be stripped for the Codex backend, got %v", key, parsed[key])
		}
	}
	if parsed["model"] != "gpt-5.6-terra" {
		t.Errorf("model must survive the strip, got %v", parsed["model"])
	}
}

// A client that already speaks the Responses API sends max_output_tokens
// directly. That one is stripped too — it is the exact field the backend
// rejects, so passing it through unchanged is the reported bug.
func TestStripCodexUnsupportedTokenParams_FromResponsesInput(t *testing.T) {
	raw := []byte(`{
		"model": "gpt-5.6-terra",
		"input": [{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hi"}]}],
		"max_output_tokens": 2048
	}`)

	body := stripCodexUnsupportedTokenParams(raw)
	parsed := decodeBody(t, body)

	if _, ok := parsed["max_output_tokens"]; ok {
		t.Errorf("a client-supplied max_output_tokens must not reach the Codex backend, got %v", parsed["max_output_tokens"])
	}
	if _, ok := parsed["input"]; !ok {
		t.Error("input must survive the strip")
	}
}

// Stripping is destructive, so a body that cannot be parsed must be returned
// byte-for-byte: a malformed payload should fail at the upstream with a useful
// error, not be silently rewritten into a valid-looking one.
func TestStripCodexUnsupportedTokenParams_LeavesUnparsableBodyAlone(t *testing.T) {
	raw := []byte(`{"model": "gpt-5.6-terra", "max_tokens":`)

	if body := stripCodexUnsupportedTokenParams(raw); string(body) != string(raw) {
		t.Errorf("an unparsable body must be passed through unchanged, got %s", body)
	}
}

// grok-cli speaks the same Responses dialect over a different backend that does
// accept the parameter, so the strip must stay scoped to the Codex path rather
// than living inside buildResponsesBody.
func TestStripCodexUnsupportedTokenParams_LeavesBuildResponsesBodyAlone(t *testing.T) {
	raw := []byte(`{
		"model": "gpt-5.6-terra",
		"max_tokens": 4096,
		"messages": [{"role": "user", "content": "hi"}]
	}`)

	transformed, _, err := buildResponsesBody(raw)
	if err != nil {
		t.Fatalf("buildResponsesBody failed: %v", err)
	}

	parsed := decodeBody(t, transformed)
	if _, ok := parsed["max_output_tokens"]; !ok {
		t.Error("buildResponsesBody must keep emitting max_output_tokens; the strip is a Codex-backend concern")
	}
}

// The compaction marker and every other field must survive: ForwardCodex runs
// the strip and applyCodexCompact over the same body, and a compaction request
// is still a request the backend validates.
func TestStripCodexUnsupportedTokenParams_KeepsOtherFields(t *testing.T) {
	raw := []byte(`{
		"model": "gpt-5.6-terra",
		"max_output_tokens": 100,
		"_compact": true,
		"stream": true,
		"store": false,
		"reasoning": {"effort": "medium"}
	}`)

	parsed := decodeBody(t, stripCodexUnsupportedTokenParams(raw))

	if parsed["_compact"] != true {
		t.Error("the _compact marker must survive so applyCodexCompact still sees it")
	}
	if parsed["stream"] != true || parsed["store"] != false {
		t.Error("stream/store must survive the strip")
	}
	if _, ok := parsed["reasoning"]; !ok {
		t.Error("reasoning must survive the strip")
	}
}

// The unit tests above pin the helper; this one pins the wiring, because a
// helper that ForwardCodex never calls would leave the 400 exactly as
// reported. The fake upstream rejects the body the way ChatGPT does, so a
// parameter missing from this list is one the backend would still 400 on.
func TestForwardCodex_UpstreamNeverSeesTokenParams(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			t.Errorf("upstream got non-JSON: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		for _, key := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens", "temperature", "top_p"} {
			if _, ok := m[key]; ok {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"detail":"Unsupported parameter: ` + key + `"}`))
				return
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{BaseURL: srv.URL, AuthHeader: "Authorization", AuthScheme: "bearer"}
	rec := httptest.NewRecorder()
	body := []byte(`{"model":"cx/gpt-5.6-terra","max_tokens":4096,"temperature":0.7,"top_p":0.9,"messages":[{"role":"user","content":"hi"}],"stream":true}`)
	// A 400 here means the parameter still reached the backend — the reported
	// bug, and the only thing this test exists to catch.
	if err := ForwardCodex(rec, &Request{
		Client:   srv.Client(),
		Config:   cfg,
		APIKey:   "test-token",
		Body:     body,
		IsStream: true,
	}); err != nil {
		t.Fatalf("ForwardCodex errored: %v", err)
	}
	if rec.Code == http.StatusBadRequest {
		t.Fatalf("upstream rejected the request as unsupported-parameter: %s", rec.Body.String())
	}
}

// The Codex backend's reasoning models answer 400 "Unsupported parameter:
// temperature" (and the same for top_p). Zed sends temperature on every turn,
// so both sampling knobs must be dropped on the Codex path.
func TestStripCodexUnsupportedTokenParams_DropsSamplingParams(t *testing.T) {
	raw := []byte(`{
		"model": "gpt-6-astra",
		"input": [{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hi"}]}],
		"temperature": 0.7,
		"top_p": 0.9,
		"reasoning": {"effort": "high"}
	}`)

	parsed := decodeBody(t, stripCodexUnsupportedTokenParams(raw))

	for _, key := range []string{"temperature", "top_p"} {
		if _, ok := parsed[key]; ok {
			t.Errorf("expected %q to be stripped for the Codex backend, got %v", key, parsed[key])
		}
	}
	if _, ok := parsed["reasoning"]; !ok {
		t.Error("reasoning must survive the strip")
	}
}

func TestBuildResponsesBody_PreservesPromptCacheKey(t *testing.T) {
	raw := []byte(`{
		"model": "gpt-5.6-terra",
		"prompt_cache_key": "explicit-session-key",
		"messages": [{"role": "user", "content": "hi"}]
	}`)

	transformed, _, err := buildResponsesBody(raw)
	if err != nil {
		t.Fatalf("buildResponsesBody failed: %v", err)
	}

	parsed := decodeBody(t, transformed)
	if parsed["prompt_cache_key"] != "explicit-session-key" {
		t.Errorf("expected prompt_cache_key = %q, got %v", "explicit-session-key", parsed["prompt_cache_key"])
	}
}

func TestForwardCodex_PromptCachingContinuity(t *testing.T) {
	var gotBody map[string]any
	var gotSessionHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSessionHeader = r.Header.Get("session_id")
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{BaseURL: srv.URL, AuthHeader: "Authorization", AuthScheme: "bearer"}
	rec := httptest.NewRecorder()
	body := []byte(`{
		"model": "cx/gpt-5.6-terra",
		"messages": [
			{"role": "user", "content": "hello"}
		],
		"reasoning_effort": "high",
		"previous_response_id": "resp_old_123",
		"stream_options": {"include_usage": true},
		"metadata": {"some": "value"}
	}`)

	err := ForwardCodex(rec, &Request{
		Client:       srv.Client(),
		Config:       cfg,
		APIKey:       "sk-test",
		Body:         body,
		IsStream:     true,
		SessionID:    "claude-session-uuid-42",
		ConnectionID: "conn-cx-1",
	})
	if err != nil {
		t.Fatalf("ForwardCodex failed: %v", err)
	}

	// 1. session_id header must match req.SessionID
	if gotSessionHeader != "claude-session-uuid-42" {
		t.Errorf("session_id header = %q, want %q", gotSessionHeader, "claude-session-uuid-42")
	}

	// 2. prompt_cache_key must be injected matching SessionID
	if gotBody["prompt_cache_key"] != "claude-session-uuid-42" {
		t.Errorf("body prompt_cache_key = %v, want %q", gotBody["prompt_cache_key"], "claude-session-uuid-42")
	}

	// 3. include = ["reasoning.encrypted_content"] must be injected when reasoning is active
	includeList, ok := gotBody["include"].([]any)
	if !ok || len(includeList) == 0 || includeList[0] != "reasoning.encrypted_content" {
		t.Errorf("body include = %v, want [\"reasoning.encrypted_content\"]", gotBody["include"])
	}

	// 4. unsupported parameters outside RESPONSES_API_ALLOWLIST must be stripped
	for _, badKey := range []string{"previous_response_id", "stream_options", "metadata"} {
		if _, exists := gotBody[badKey]; exists {
			t.Errorf("unsupported parameter %q should be stripped, but was present in body", badKey)
		}
	}
}

func TestForwardCodex_StripsStoredItemReferences(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{BaseURL: srv.URL, AuthHeader: "Authorization", AuthScheme: "bearer"}
	rec := httptest.NewRecorder()
	// Responses API format input with stored server IDs
	body := []byte(`{
		"model": "gpt-5.6-terra",
		"input": [
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "turn 1"}]},
			{"type": "message", "role": "assistant", "id": "msg_12345", "content": [{"type": "output_text", "text": "resp 1"}]},
			{"type": "item_reference", "id": "rs_abc123"},
			"rs_raw_string_id",
			{"type": "function_call", "id": "fc_789", "name": "read", "arguments": "{}"}
		],
		"stream": true,
		"store": false
	}`)

	err := ForwardCodex(rec, &Request{
		Client:       srv.Client(),
		Config:       cfg,
		APIKey:       "sk-test",
		Body:         body,
		IsStream:     true,
		ConnectionID: "conn-cx-2",
	})
	if err != nil {
		t.Fatalf("ForwardCodex failed: %v", err)
	}

	inputs, ok := gotBody["input"].([]any)
	if !ok {
		t.Fatalf("expected input to be an array, got %T", gotBody["input"])
	}
	// "rs_raw_string_id" and "item_reference" must be filtered out entirely
	for _, item := range inputs {
		if s, isStr := item.(string); isStr && s == "rs_raw_string_id" {
			t.Error("raw server ID string rs_raw_string_id must be stripped")
		}
		if m, isMap := item.(map[string]any); isMap {
			if m["type"] == "item_reference" {
				t.Error("item_reference must be stripped from input")
			}
			// Server generated IDs must be deleted from items
			if id, hasID := m["id"].(string); hasID {
				if id == "msg_12345" || id == "fc_789" {
					t.Errorf("server generated id %q must be deleted from input item", id)
				}
			}
		}
	}
}

func TestStripCodexStoredItemReferences_PreserveLitePrefix(t *testing.T) {
	raw := []byte(`{
		"model": "gpt-6-sol",
		"input": [
			{"type": "message", "role": "developer", "id": "msg_dev1", "content": [{"type": "input_text", "text": "sys"}]},
			{"type": "message", "role": "assistant", "id": "msg_ast1", "content": [{"type": "output_text", "text": "hi"}]}
		]
	}`)

	// When responsesLite = true (lite model): msg_dev1 is preserved, msg_ast1 is stripped
	liteBody := stripCodexUnsupportedTokenParams(raw, true)
	liteParsed := decodeBody(t, liteBody)
	liteInput := liteParsed["input"].([]any)
	devItemLite := liteInput[0].(map[string]any)
	if devItemLite["id"] != "msg_dev1" {
		t.Errorf("expected msg_dev1 to be preserved on lite model, got %v", devItemLite["id"])
	}
	astItemLite := liteInput[1].(map[string]any)
	if _, ok := astItemLite["id"]; ok {
		t.Errorf("expected msg_ast1 to be stripped on lite model, got %v", astItemLite["id"])
	}

	// When responsesLite = false (classic model): msg_dev1 MUST be stripped
	classicBody := stripCodexUnsupportedTokenParams(raw, false)
	classicParsed := decodeBody(t, classicBody)
	classicInput := classicParsed["input"].([]any)
	devItemClassic := classicInput[0].(map[string]any)
	if _, ok := devItemClassic["id"]; ok {
		t.Errorf("expected msg_dev1 to be stripped on classic model, got %v", devItemClassic["id"])
	}
}
