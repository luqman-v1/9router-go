package translator

import (
	"encoding/json"
	"strings"
	"testing"
)

// Bedrock Converse puts the callable name under tools[].toolSpec.name. Fitting
// it is only half the contract: if the response comes back carrying the fitted
// name and nobody reverses it, the client receives a tool_call for a name it
// never declared.
func TestFitToolNames_ConverseToolSpecStaysConsistent(t *testing.T) {
	longName := "mcp__server_name__action_detail_something_very_long_indeed_action_12345"
	body := []byte(`{
		"model": "anthropic.claude-3-5-sonnet",
		"tools": [{"toolSpec": {"name": "` + longName + `", "inputSchema": {"json": {"type": "object"}}}}]
	}`)

	got, toolMap := FitToolNames(body)
	if len(toolMap) != 1 {
		t.Fatalf("expected the Converse tool name to be fitted, got map %v", toolMap)
	}

	var parsed map[string]any
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	spec := parsed["tools"].([]any)[0].(map[string]any)["toolSpec"].(map[string]any)
	fitted, _ := spec["name"].(string)
	if len(fitted) > MaxToolNameLength {
		t.Errorf("tools[].toolSpec.name %q is %d chars, over the %d limit", fitted, len(fitted), MaxToolNameLength)
	}
	if fitted == longName {
		t.Error("the long toolSpec name still travelled upstream; the 64-char fix did not apply")
	}
	if spec["inputSchema"] == nil {
		t.Error("fitting the name must not disturb the rest of the toolSpec")
	}

	resp := []byte(`{"output":{"message":{"content":[{"toolUse":{"toolUseId":"tooluse_1","name":"` + fitted + `"}}]}}}`)
	if restored := string(RestoreToolNamesInPayload(resp, toolMap)); !strings.Contains(restored, longName) {
		t.Errorf("expected %q restored for the client, got: %s", longName, restored)
	}
}

// The response restores from two places: the streaming contentBlockStart that
// opens a toolUse block, and the non-streaming output.message.content array.
// Covering only one leaves a fitted name visible to the client on the other lane.
func TestRestoreToolNames_ConverseToolUse(t *testing.T) {
	longName := "mcp__server_name__action_detail_something_very_long_indeed_action_12345"
	shortName := "mcp__server_name__action_detail_something_very_long_indeed_act_1"
	toolMap := map[string]string{shortName: longName}

	t.Run("streaming contentBlockStart", func(t *testing.T) {
		frame := []byte(`data: {"contentBlockStart":{"start":{"toolUse":{"toolUseId":"tooluse_1","name":"` + shortName + `"}}}}`)
		restored := string(RestoreToolNamesInSSE(frame, toolMap))
		if !strings.Contains(restored, longName) {
			t.Errorf("expected %q restored in contentBlockStart, got: %s", longName, restored)
		}
	})

	t.Run("non-streaming output message", func(t *testing.T) {
		body := []byte(`{"output":{"message":{"content":[{"toolUse":{"toolUseId":"tooluse_1","name":"` + shortName + `"}}]}}}`)
		restored := string(RestoreToolNamesInPayload(body, toolMap))
		if !strings.Contains(restored, longName) {
			t.Errorf("expected %q restored in output.message.content, got: %s", longName, restored)
		}
	})

	t.Run("a name we never fitted is left alone", func(t *testing.T) {
		body := []byte(`{"output":{"message":{"content":[{"toolUse":{"toolUseId":"tooluse_1","name":"get_weather"}}]}}}`)
		restored := string(RestoreToolNamesInPayload(body, toolMap))
		if !strings.Contains(restored, "get_weather") {
			t.Errorf("an unfitted name must survive untouched, got: %s", restored)
		}
	})
}

// A name the collect pass records is a name the replace pass writes. toolSpec
// has to appear in both or the map holds a fitted name the request never used.
func TestFitToolNames_ConverseToolSpecRoundTripsThroughHistory(t *testing.T) {
	longName := "mcp__server_name__action_detail_something_very_long_indeed_action_12345"
	body := []byte(`{
		"model": "anthropic.claude-3-5-sonnet",
		"tools": [{"toolSpec": {"name": "` + longName + `"}}],
		"messages": [
			{"role": "assistant", "tool_calls": [{"function": {"name": "` + longName + `"}}]}
		]
	}`)

	got, toolMap := FitToolNames(body)
	if len(toolMap) != 1 {
		t.Fatalf("expected exactly one fitted name, got %v", toolMap)
	}

	var parsed map[string]any
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	spec := parsed["tools"].([]any)[0].(map[string]any)["toolSpec"].(map[string]any)
	call := parsed["messages"].([]any)[0].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)

	if spec["name"] != call["name"] {
		t.Errorf("request contradicts itself: toolSpec says %v but history says %v", spec["name"], call["name"])
	}
}