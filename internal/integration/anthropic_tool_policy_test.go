//go:build integration

package integration

import (
	json "encoding/json/v2"
	"net/http"
	"testing"
)

// claudeMessagesResponder answers the Anthropic Messages dialect, which is what
// the gateway must produce when an OpenAI-format client is routed at a real
// Anthropic upstream.
func claudeMessagesResponder() http.HandlerFunc {
	return JSONResponder(http.StatusOK, `{
	  "id": "msg_integration",
	  "type": "message",
	  "role": "assistant",
	  "model": "claude-sonnet-4-5",
	  "content": [{"type": "text", "text": "upstream reply"}],
	  "stop_reason": "end_turn",
	  "stop_sequence": null,
	  "usage": {"input_tokens": 11, "output_tokens": 7}
	}`)
}

// addAnthropicConnection stores a connection whose baseUrl still reads as the
// real Anthropic endpoint, because that is the only configuration for which
// isAnthropicUpstream (fallback.go:165) fires and the OpenAI->Claude request
// conversion runs. A `providerSpecificData.vercelRelayUrl` names the fake as an
// edge relay, so the request is dialled locally while the config still reports
// api.anthropic.com as its target — the same shape the dashboard's edge pools
// produce in production.
func addAnthropicConnection(t *testing.T, env *Env, id string, up *Upstream) {
	t.Helper()
	priority := 1
	data := `{"apiKey":"sk-upstream","baseUrl":"https://api.anthropic.com/v1/messages",` +
		`"providerSpecificData":{"vercelRelayUrl":"` + up.URL + `"}}`
	if err := env.Repo.CreateProviderConnectionFull(id, "claude", "apikey", "Anthropic Relay", &priority, data); err != nil {
		t.Fatalf("create connection %s: %v", id, err)
	}
	stored, err := env.Repo.GetProviderConnectionByID(id)
	if err != nil || stored == nil {
		t.Fatalf("read back connection %s: %v", id, err)
	}
	if stored.Data == "" {
		t.Fatalf("connection %s stored no data — the request would dial the real provider", id)
	}
}

// sentToolChoice decodes the tool_choice the gateway actually forwarded.
func sentToolChoice(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var req struct {
		ToolChoice map[string]any `json:"tool_choice"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("decode forwarded body %s: %v", raw, err)
	}
	return req.ToolChoice
}

// TestToolChoicePolicyReachesAnthropicUpstream pins the half of #4581 that a
// unit test on the converter alone cannot prove: that an OpenAI client's
// parallel_tool_calls:false actually survives the whole pipeline and arrives at
// a real Anthropic Messages endpoint as disable_parallel_tool_use.
//
// The unit test in internal/proxy/executor covers ensureMessagesMaxTokens. This
// one covers everything around it — provider selection, isAnthropicUpstream,
// token savers, the relay base URL — which is exactly where a client can lose
// the instruction while every converter test stays green.
func TestToolChoicePolicyReachesAnthropicUpstream(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, claudeMessagesResponder())
	addAnthropicConnection(t, env, "conn-anthropic", upstream)

	res := env.Post(t, "/v1/chat/completions", map[string]any{
		"model":    "claude/claude-sonnet-4-5",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
		"tools": []map[string]any{{
			"type":     "function",
			"function": map[string]any{"name": "probe", "parameters": map[string]any{"type": "object"}},
		}},
		"parallel_tool_calls": false,
	})
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}

	sent := upstream.Last(t)
	var forwarded map[string]any
	if err := json.Unmarshal(sent.Body, &forwarded); err != nil {
		t.Fatalf("decode forwarded body: %v", err)
	}

	// The OpenAI-only key must not survive: the Messages API rejects it.
	if _, present := forwarded["parallel_tool_calls"]; present {
		t.Errorf("parallel_tool_calls reached the Claude wire: %s", sent.Body)
	}

	choice := sentToolChoice(t, sent.Body)
	if choice["disable_parallel_tool_use"] != true {
		t.Errorf("tool_choice.disable_parallel_tool_use = %v, want true — the single-tool-call policy was lost",
			choice["disable_parallel_tool_use"])
	}
}

// TestNamedToolChoiceSurvivesTheParallelPolicy is the clobber guard. Merging
// disable_parallel_tool_use must not cost the caller its named tool choice —
// that would turn "call exactly this tool" into "call anything".
func TestNamedToolChoiceSurvivesTheParallelPolicy(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, claudeMessagesResponder())
	addAnthropicConnection(t, env, "conn-anthropic", upstream)

	res := env.Post(t, "/v1/chat/completions", map[string]any{
		"model":    "claude/claude-sonnet-4-5",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
		"tools": []map[string]any{{
			"type":     "function",
			"function": map[string]any{"name": "probe", "parameters": map[string]any{"type": "object"}},
		}},
		"tool_choice":         map[string]any{"type": "function", "function": map[string]any{"name": "probe"}},
		"parallel_tool_calls": false,
	})
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}

	choice := sentToolChoice(t, upstream.Last(t).Body)
	if choice["type"] != "tool" || choice["name"] != "probe" {
		t.Errorf("tool_choice = %v, want the caller's named tool preserved (type=tool name=probe)", choice)
	}
	if choice["disable_parallel_tool_use"] != true {
		t.Errorf("tool_choice.disable_parallel_tool_use = %v, want true", choice["disable_parallel_tool_use"])
	}
}

// TestToolChoiceNoneReachesAnthropicUpstream is the runtime half of #4577. A
// client that forbids tool calls must not have its instruction turned into
// permission: before the port, the string "none" was dropped on the way to
// Claude and the model read the request as "tools allowed".
func TestToolChoiceNoneReachesAnthropicUpstream(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, claudeMessagesResponder())
	addAnthropicConnection(t, env, "conn-anthropic", upstream)

	res := env.Post(t, "/v1/chat/completions", map[string]any{
		"model":    "claude/claude-sonnet-4-5",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
		"tools": []map[string]any{{
			"type":     "function",
			"function": map[string]any{"name": "probe", "parameters": map[string]any{"type": "object"}},
		}},
		"tool_choice": "none",
	})
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}

	choice := sentToolChoice(t, upstream.Last(t).Body)
	if choice == nil {
		t.Fatal("tool_choice was dropped on the way to Claude — the model would read the request as \"tools allowed\"")
	}
	if choice["type"] != "none" {
		t.Errorf("tool_choice.type = %v, want \"none\"", choice["type"])
	}
}
