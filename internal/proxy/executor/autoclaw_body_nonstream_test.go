package executor

import (
	"encoding/json"
	"testing"
)

// TestAutoclawBodyNormalisesToDesktopShape pins the captured AutoClaw 2.6.1
// body contract: the managed gateway answers 406 with an empty body when a
// bare OpenAI body (max_tokens, stream:false) is sent. Every request is
// normalised to stream:true + stream_options/store/max_completion_tokens/
// reasoning_effort; ForwardAutoclaw folds the resulting SSE for a non-stream
// client, so the client's stream flag never leaks into the wire contract.
func TestAutoclawBodyNormalisesToDesktopShape(t *testing.T) {
	body := []byte(`{"model":"zai_glm-5.3-flash","messages":[{"role":"user","content":"hi"}],"max_tokens":512,"tools":[{"type":"function"}]}`)

	out, err := autoclawBody(body, false)
	if err != nil {
		t.Fatalf("autoclawBody: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if m["stream"] != true {
		t.Errorf("stream = %v, want true", m["stream"])
	}
	opts, _ := m["stream_options"].(map[string]any)
	if opts["include_usage"] != true {
		t.Errorf("stream_options = %v, want include_usage:true", opts)
	}
	if m["store"] != false {
		t.Errorf("store = %v, want false", m["store"])
	}
	if m["max_completion_tokens"] != float64(512) {
		t.Errorf("max_completion_tokens = %v, want 512 (legacy max_tokens)", m["max_completion_tokens"])
	}
	if _, present := m["max_tokens"]; present {
		t.Errorf("max_tokens must be removed, got %v", m["max_tokens"])
	}
	if m["reasoning_effort"] != "max" {
		t.Errorf("reasoning_effort = %v, want max", m["reasoning_effort"])
	}
	if m["model"] != "zai_glm-5.3-flash" {
		t.Errorf("model = %v, want preserved", m["model"])
	}
	if _, present := m["tools"]; !present {
		t.Errorf("tools must pass through untouched")
	}
}

func TestAutoclawBodyDefaults(t *testing.T) {
	out, err := autoclawBody([]byte(`{"model":"zai_auto","messages":[]}`), true)
	if err != nil {
		t.Fatalf("autoclawBody: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["max_completion_tokens"] != float64(37500) {
		t.Errorf("max_completion_tokens = %v, want 37500", m["max_completion_tokens"])
	}
}

func TestAutoclawBodyPrependsDesktopSystemBeforeClientSystem(t *testing.T) {
	body := []byte(`{"model":"zai_auto","messages":[{"role":"system","content":"Name the conversation."},{"role":"user","content":"halo"}]}`)
	out, err := autoclawBody(body, true)
	if err != nil {
		t.Fatalf("autoclawBody: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msgs, _ := m["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages = %d, want 3 (desktop system + client system + user)", len(msgs))
	}
	first, _ := msgs[0].(map[string]any)
	if first["content"] != autoclawDesktopSystemPrompt {
		t.Errorf("first system prompt is not the desktop prompt")
	}
	second, _ := msgs[1].(map[string]any)
	if second["content"] != "Name the conversation." {
		t.Errorf("client system prompt lost: %v", second["content"])
	}
}
