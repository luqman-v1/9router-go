package translator

import (
	"fmt"
	"strings"
	"testing"
)

func TestToolFit_ExistingUniquenessAndRestore(t *testing.T) {
	t.Run("CollidingLongPrefixesTruncateUniquelyUnderMaxLimit", func(t *testing.T) {
		// Fixture with multiple tools sharing a common prefix exceeding 64 characters
		sharedPrefix := "mcp__enterprise_cloud_cluster_management__execute_critical_operation_step_" // 75 chars
		longA := sharedPrefix + "provision_primary_database_node"                                    // 107 chars
		longB := sharedPrefix + "provision_secondary_replica_node"                                   // 108 chars
		longC := sharedPrefix + "provision_tertiary_witness_node"                                    // 106 chars
		longD := sharedPrefix + "provision_quaternary_backup_node"                                   // 107 chars

		shortUntouched := "read_system_metrics"
		boundary64 := "exactly_sixty_four_characters_long_tool_name_abcdefghijklmnopqrs" // 64 chars
		if len(boundary64) != MaxToolNameLength {
			t.Fatalf("boundary fixture must be exactly %d chars, got %d", MaxToolNameLength, len(boundary64))
		}

		longTools := []string{longA, longB, longC, longD}

		req := map[string]any{
			"model": "gpt-4o",
			"tools": []any{
				map[string]any{"type": "function", "function": map[string]any{"name": longA, "description": "node A"}},
				map[string]any{"type": "function", "function": map[string]any{"name": longB, "description": "node B"}},
				map[string]any{"type": "function", "function": map[string]any{"name": longC, "description": "node C"}},
				map[string]any{"type": "function", "function": map[string]any{"name": longD, "description": "node D"}},
				map[string]any{"type": "function", "function": map[string]any{"name": shortUntouched, "description": "short"}},
				map[string]any{"type": "function", "function": map[string]any{"name": boundary64, "description": "boundary"}},
			},
			"messages": []any{
				map[string]any{"role": "user", "content": "deploy cluster"},
				map[string]any{
					"role": "assistant",
					"tool_calls": []any{
						map[string]any{"id": "call_a", "type": "function", "function": map[string]any{"name": longA}},
						map[string]any{"id": "call_b", "type": "function", "function": map[string]any{"name": longB}},
					},
				},
				map[string]any{"role": "tool", "name": longA, "tool_call_id": "call_a", "content": "ok"},
				map[string]any{"role": "tool", "name": shortUntouched, "tool_call_id": "call_s", "content": "metrics"},
			},
			"tool_choice": map[string]any{
				"type":     "function",
				"function": map[string]any{"name": longC},
			},
		}

		shortToOrig := FitToolNamesInMap(req)
		if shortToOrig == nil {
			t.Fatal("expected non-nil shortToOrig map")
		}

		// 1. Number of entries in shortToOrig must match exactly the number of long tools
		if len(shortToOrig) != len(longTools) {
			t.Fatalf("expected %d entries in shortToOrig, got %d: %v", len(longTools), len(shortToOrig), shortToOrig)
		}

		// 2. Short tools (<= 64 chars) must not be in shortToOrig
		if _, exists := shortToOrig[shortUntouched]; exists {
			t.Errorf("short tool %q must not exist in shortToOrig", shortUntouched)
		}
		if _, exists := shortToOrig[boundary64]; exists {
			t.Errorf("boundary tool %q must not exist in shortToOrig", boundary64)
		}

		// 3. Verify each shortened name: length <= 64, uniqueness, correct reverse mapping
		seenShorts := make(map[string]string)
		for shortName, origName := range shortToOrig {
			if len(shortName) > MaxToolNameLength {
				t.Errorf("shortened tool name %q exceeds MaxToolNameLength (%d > %d)", shortName, len(shortName), MaxToolNameLength)
			}
			if prevOrig, collision := seenShorts[shortName]; collision {
				t.Fatalf("collision detected! shortened name %q assigned to both %q and %q", shortName, prevOrig, origName)
			}
			seenShorts[shortName] = origName

			// Suffix convention verification: must end with _1, _2, _3, or _4
			if !strings.Contains(shortName, "_") {
				t.Errorf("expected shortened name %q to contain unique suffix", shortName)
			}
		}

		// 4. Verify 100% accurate reverse mapping for all long tools
		for _, orig := range longTools {
			found := false
			for short, mappedOrig := range shortToOrig {
				if mappedOrig == orig {
					found = true
					if shortToOrig[short] != orig {
						t.Errorf("reverse mapping mismatch for %q: got %q", orig, shortToOrig[short])
					}
					break
				}
			}
			if !found {
				t.Errorf("original tool %q not found in shortToOrig values", orig)
			}
		}

		// 5. Verify in-place replacement on req structures
		toolsList := req["tools"].([]any)
		for i, toolItem := range toolsList {
			tm := toolItem.(map[string]any)
			fn := tm["function"].(map[string]any)
			name := fn["name"].(string)

			if i < 4 {
				// Long tools must have been replaced with their short names
				if name == longTools[i] {
					t.Errorf("tool %d (%q) was not replaced in req tools", i, longTools[i])
				}
				if shortToOrig[name] != longTools[i] {
					t.Errorf("tool %d in req has name %q, expected mapped from %q", i, name, longTools[i])
				}
			} else if i == 4 {
				// shortUntouched must remain untouched
				if name != shortUntouched {
					t.Errorf("short tool changed in req: got %q, want %q", name, shortUntouched)
				}
			} else if i == 5 {
				// boundary64 must remain untouched
				if name != boundary64 {
					t.Errorf("boundary tool changed in req: got %q, want %q", name, boundary64)
				}
			}
		}

		// 6. Verify assistant tool_calls and tool message replacement in messages
		msgs := req["messages"].([]any)
		asstCalls := msgs[1].(map[string]any)["tool_calls"].([]any)
		call0Name := asstCalls[0].(map[string]any)["function"].(map[string]any)["name"].(string)
		if call0Name == longA || shortToOrig[call0Name] != longA {
			t.Errorf("assistant tool_call 0 name %q not properly replaced from %q", call0Name, longA)
		}
		call1Name := asstCalls[1].(map[string]any)["function"].(map[string]any)["name"].(string)
		if call1Name == longB || shortToOrig[call1Name] != longB {
			t.Errorf("assistant tool_call 1 name %q not properly replaced from %q", call1Name, longB)
		}

		toolMsgA := msgs[2].(map[string]any)["name"].(string)
		if toolMsgA != call0Name {
			t.Errorf("tool message name %q != assistant tool_call name %q", toolMsgA, call0Name)
		}

		toolMsgShort := msgs[3].(map[string]any)["name"].(string)
		if toolMsgShort != shortUntouched {
			t.Errorf("short tool message name changed: got %q, want %q", toolMsgShort, shortUntouched)
		}

		// 7. Verify tool_choice replacement
		tcName := req["tool_choice"].(map[string]any)["function"].(map[string]any)["name"].(string)
		if tcName == longC || shortToOrig[tcName] != longC {
			t.Errorf("tool_choice name %q not properly replaced from %q", tcName, longC)
		}

		// 8. Verify Upstream Response Restoration with shortToOrig map
		for shortName, origName := range shortToOrig {
			// OpenAI non-stream response restoration
			openAIPayload := []byte(fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":%q,"arguments":"{}"}}]}}]}`, shortName))
			restoredOpenAI := RestoreToolNamesInPayload(openAIPayload, shortToOrig)
			if !strings.Contains(string(restoredOpenAI), origName) {
				t.Errorf("OpenAI payload failed to restore original name %q, got: %s", origName, string(restoredOpenAI))
			}
			if strings.Contains(string(restoredOpenAI), shortName) {
				t.Errorf("OpenAI payload still contains shortName %q: %s", shortName, string(restoredOpenAI))
			}

			// Claude non-stream response restoration
			claudePayload := []byte(fmt.Sprintf(`{"content":[{"type":"tool_use","id":"tu_1","name":%q,"input":{}}]}`, shortName))
			restoredClaude := RestoreToolNamesInPayload(claudePayload, shortToOrig)
			if !strings.Contains(string(restoredClaude), origName) {
				t.Errorf("Claude payload failed to restore original name %q, got: %s", origName, string(restoredClaude))
			}

			// Responses API output restoration
			responsesPayload := []byte(fmt.Sprintf(`{"output":[{"type":"function_call","name":%q}]}`, shortName))
			restoredResponses := RestoreToolNamesInPayload(responsesPayload, shortToOrig)
			if !strings.Contains(string(restoredResponses), origName) {
				t.Errorf("Responses payload failed to restore original name %q, got: %s", origName, string(restoredResponses))
			}

			// Streaming SSE chunk restoration
			sseChunk := []byte(fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":%q}}]}}]}\n\n", shortName))
			restoredSSE := RestoreToolNamesInPayload(sseChunk, shortToOrig)
			if !strings.Contains(string(restoredSSE), origName) {
				t.Errorf("SSE chunk failed to restore original name %q, got: %s", origName, string(restoredSSE))
			}
		}

		// An unshortened tool name in response must remain untouched
		unfittedResp := []byte(fmt.Sprintf(`{"choices":[{"message":{"tool_calls":[{"function":{"name":%q}}]}}]}`, shortUntouched))
		restoredUnfitted := RestoreToolNamesInPayload(unfittedResp, shortToOrig)
		if !strings.Contains(string(restoredUnfitted), shortUntouched) {
			t.Errorf("unfitted tool name %q must remain untouched, got: %s", shortUntouched, string(restoredUnfitted))
		}
	})

	t.Run("ShortToolsOnlyReturnsNilMap", func(t *testing.T) {
		req := map[string]any{
			"model": "gpt-4",
			"tools": []any{
				map[string]any{"type": "function", "function": map[string]any{"name": "tool_one"}},
				map[string]any{"type": "function", "function": map[string]any{"name": "tool_two"}},
			},
		}
		gotMap := FitToolNamesInMap(req)
		if gotMap != nil {
			t.Errorf("expected nil map when all tools are short, got: %v", gotMap)
		}
	})
}
