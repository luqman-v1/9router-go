package resilience

import (
	"strings"
	"testing"
)

// claudeToolUse builds one assistant turn holding a single tool_use block, the
// shape a Claude client sends for a tool call it already made.
func claudeToolUse(name string, input any) map[string]any {
	return map[string]any{
		"role": "assistant",
		"content": []any{
			map[string]any{"type": "tool_use", "id": "toolu_1", "name": name, "input": input},
		},
	}
}

func toolResultTurn() map[string]any {
	return map[string]any{
		"role": "user",
		"content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": "error"},
		},
	}
}

// A provider that streams a bare tool call repeats it: the client rejects the
// call for missing required fields and retries, so every attempt hashes the
// same and the identical-call threshold only fires after three rounds. Catch it
// on the first repeat, once the tool is known to take arguments.
func TestInspectHistoryTurns_EmptyArgumentsAfterArgsIntervenes(t *testing.T) {
	messages := []map[string]any{
		{"role": "user", "content": []any{map[string]any{"type": "text", "text": "list /tmp"}}},
		claudeToolUse("Bash", map[string]any{"command": "ls /tmp"}),
		toolResultTurn(),
		claudeToolUse("Bash", map[string]any{}),
		toolResultTurn(),
	}

	got := InspectHistoryTurns(messages, DefaultStalledToolLoopThreshold)
	if got == nil {
		t.Fatal("expected an intervention for a bare tool call that normally carries arguments")
	}
	if got.ToolName != "Bash" {
		t.Errorf("tool name = %q, want Bash", got.ToolName)
	}
	if !strings.Contains(got.Intervention, "no arguments") {
		t.Errorf("intervention should name the empty call, got %q", got.Intervention)
	}
}

// An empty-argument call is not evidence of a loop when the client always calls
// that tool bare (TodoRead and friends), so it must not fire.
func TestInspectHistoryTurns_EmptyArgumentsForArglessToolIsIgnored(t *testing.T) {
	messages := []map[string]any{
		{"role": "user", "content": []any{map[string]any{"type": "text", "text": "what is next?"}}},
		claudeToolUse("TodoRead", map[string]any{}),
		toolResultTurn(),
		claudeToolUse("TodoRead", map[string]any{}),
		toolResultTurn(),
	}

	if got := InspectHistoryTurns(messages, DefaultStalledToolLoopThreshold); got != nil {
		t.Fatalf("argless tool must not be flagged, got %v", got.Message)
	}
}

// A normal tool call whose arguments are present stays out of the way.
func TestInspectHistoryTurns_NormalArgumentsUnchanged(t *testing.T) {
	messages := []map[string]any{
		{"role": "user", "content": []any{map[string]any{"type": "text", "text": "list /tmp"}}},
		claudeToolUse("Bash", map[string]any{"command": "ls /tmp"}),
		toolResultTurn(),
		claudeToolUse("Bash", map[string]any{"command": "ls /var"}),
		toolResultTurn(),
	}

	if got := InspectHistoryTurns(messages, DefaultStalledToolLoopThreshold); got != nil {
		t.Fatalf("progressing calls must not be flagged, got %v", got.Message)
	}
}

func TestEmptyToolArguments(t *testing.T) {
	for _, args := range []string{"", "  ", "{}", " null ", "null"} {
		if !emptyToolArguments(args) {
			t.Errorf("%q should count as empty", args)
		}
	}
	for _, args := range []string{`{"command":"ls"}`, `[]`, `0`} {
		if emptyToolArguments(args) {
			t.Errorf("%q should not count as empty", args)
		}
	}
}
