package translator

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

func arglessBody(messages string) []byte {
	return []byte(`{"model":"m","max_tokens":16,"messages":` + messages + `}`)
}

func toolUseBlocks(t *testing.T, body []byte) []string {
	t.Helper()
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	msgs, _ := req["messages"].([]any)
	var names []string
	for _, mRaw := range msgs {
		msg, _ := mRaw.(map[string]any)
		blocks, _ := msg["content"].([]any)
		for _, bRaw := range blocks {
			block, _ := bRaw.(map[string]any)
			if typ, _ := block["type"].(string); typ == "tool_use" {
				name, _ := block["name"].(string)
				names = append(names, name)
			}
		}
	}
	return names
}

// A bare call is dropped, and so is the tool_result paired with it: leaving the
// result behind would hand the conversation an orphan the provider rejects.
func TestDropArgumentlessToolCalls_DropsCallAndItsResult(t *testing.T) {
	body := arglessBody(`[
		{"role":"user","content":[{"type":"text","text":"list /tmp"}]},
		{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls /tmp"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"a b c"}]},
		{"role":"assistant","content":[{"type":"tool_use","id":"t2","name":"Bash","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":"missing required parameter"}]},
		{"role":"user","content":[{"type":"text","text":"thử lại đi"}]}
	]`)

	out := DropArgumentlessToolCalls(body)

	if got := toolUseBlocks(t, out); len(got) != 1 || got[0] != "Bash" {
		t.Fatalf("expected only the well-formed call to survive, got %v", got)
	}
	if strings.Contains(string(out), `"tool_use_id":"t2"`) {
		t.Error("the orphaned tool_result must go with the call it answers")
	}
	if !strings.Contains(string(out), `"tool_use_id":"t1"`) {
		t.Error("the result for the surviving call must stay")
	}
}

// Every call to a tool is bare here — nothing proves the tool takes arguments,
// so the conversation must pass through byte for byte.
func TestDropArgumentlessToolCalls_LeavesArglessToolsAlone(t *testing.T) {
	body := arglessBody(`[
		{"role":"user","content":[{"type":"text","text":"what is next?"}]},
		{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"TodoRead","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"[]"}]}
	]`)

	if out := DropArgumentlessToolCalls(body); string(out) != string(body) {
		t.Fatalf("body should be untouched, got %s", out)
	}
}

// A conversation with nothing to remove is not re-encoded: pass-through keeps
// the client's byte order and any key the encoder would reorder.
func TestDropArgumentlessToolCalls_NoChangeKeepsBody(t *testing.T) {
	body := []byte(`{"max_tokens":16,"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	if out := DropArgumentlessToolCalls(body); string(out) != string(body) {
		t.Fatalf("body should be untouched, got %s", out)
	}
}

// The converted lane must apply the same rule, since SanitizeClaudePassthrough
// does not run there.
func TestTranslateClaudeToOpenAI_DropsArgumentlessCall(t *testing.T) {
	body := arglessBody(`[
		{"role":"user","content":[{"type":"text","text":"list /tmp"}]},
		{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls /tmp"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"a b c"}]},
		{"role":"assistant","content":[{"type":"tool_use","id":"t2","name":"Bash","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":"missing required parameter"}]}
	]`)

	out, err := TranslateClaudeToOpenAI(body, "", "")
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if strings.Contains(string(out), "missing required parameter") {
		t.Errorf("the bare call and its result should not reach the OpenAI lane: %s", out)
	}
}

// The Claude passthrough lane runs its own sanitizer, so the rule lives there
// too — one call site would leave half the traffic feeding the model its own
// broken call back.
func TestSanitizeClaudePassthrough_DropsArgumentlessToolUse(t *testing.T) {
	body := arglessBody(`[
		{"role":"user","content":[{"type":"text","text":"list /tmp"}]},
		{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls /tmp"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"a b c"}]},
		{"role":"assistant","content":[{"type":"tool_use","id":"t2","name":"Bash","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":"missing required parameter"}]}
	]`)

	out := SanitizeClaudePassthrough(body, false)

	if got := toolUseBlocks(t, out); len(got) != 1 {
		t.Fatalf("expected one surviving tool call, got %v", got)
	}
	if strings.Contains(string(out), `"tool_use_id":"t2"`) {
		t.Error("orphaned tool_result should have been dropped with its call")
	}
}
