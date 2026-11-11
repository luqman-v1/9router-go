package executor

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

func TestCodexToolArgumentSnapshots(t *testing.T) {
	const added = `{"type":"response.output_item.added","output_index":3,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"read"}}`
	const itemDone = `{"type":"response.output_item.done","output_index":3,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"read","arguments":"{\"x\":1}"}}`
	tests := []struct {
		name   string
		events []string
		want   string
	}{
		{"delta then snapshots with different identifiers", []string{added,
			`{"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{\"x\":1}"}`,
			`{"type":"response.function_call_arguments.done","item_id":"fc_1","call_id":"call_1","arguments":"{\"x\":1}"}`, itemDone}, `{"x":1}`},
		{"only arguments done and item done", []string{added,
			`{"type":"response.function_call_arguments.done","item_id":"fc_1","arguments":"{\"x\":1}"}`, itemDone}, `{"x":1}`},
		{"only item done", []string{added, itemDone}, `{"x":1}`},
		{"partial delta then snapshot suffix", []string{added,
			`{"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{\"x\":"}`,
			`{"type":"response.function_call_arguments.done","item_id":"fc_1","arguments":"{\"x\":1}"}`, itemDone}, `{"x":1}`},
		{"equal deltas are not duplicates", []string{added,
			`{"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"1"}`,
			`{"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"1"}`,
			`{"type":"response.function_call_arguments.done","item_id":"fc_1","arguments":"11"}`}, `11`},
		{"output index maps around reasoning items", []string{added,
			`{"type":"response.function_call_arguments.delta","output_index":3,"delta":"{\"x\":1}"}`,
			`{"type":"response.function_call_arguments.done","output_index":3,"arguments":"{\"x\":1}"}`, itemDone}, `{"x":1}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &CodexStreamState{}
			var got strings.Builder
			for _, event := range tt.events {
				for _, frame := range ProcessCodexEvent(event, state, "chatcmpl-test", 1) {
					var chunk struct {
						Choices []struct {
							Delta struct {
								ToolCalls []struct {
									Index    int `json:"index"`
									Function struct {
										Arguments string `json:"arguments"`
									} `json:"function"`
								} `json:"tool_calls"`
							} `json:"delta"`
						} `json:"choices"`
					}
					if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(frame, "data: "))), &chunk); err != nil {
						t.Fatalf("decode chunk: %v", err)
					}
					for _, choice := range chunk.Choices {
						for _, call := range choice.Delta.ToolCalls {
							if call.Index != 0 {
								t.Errorf("tool index = %d, want 0", call.Index)
							}
							got.WriteString(call.Function.Arguments)
						}
					}
				}
			}
			if got.String() != tt.want {
				t.Errorf("arguments = %q, want %q", got.String(), tt.want)
			}
		})
	}
}

func TestCodexToolArgumentsUseOutputIndexForInterleavedCalls(t *testing.T) {
	state := &CodexStreamState{}
	events := []string{
		`{"type":"response.output_item.added","output_index":2,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"read"}}`,
		`{"type":"response.output_item.added","output_index":5,"item":{"id":"fc_2","type":"function_call","call_id":"call_2","name":"glob"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":2,"delta":"{\"a\":1}"}`,
		`{"type":"response.function_call_arguments.delta","output_index":5,"delta":"{\"b\":2}"}`,
		`{"type":"response.function_call_arguments.done","output_index":2,"arguments":"{\"a\":1}"}`,
		`{"type":"response.function_call_arguments.done","output_index":5,"arguments":"{\"b\":2}"}`,
	}
	got := make(map[int]string)
	for _, event := range events {
		for _, frame := range ProcessCodexEvent(event, state, "chatcmpl-test", 1) {
			var chunk struct {
				Choices []struct {
					Delta struct {
						ToolCalls []struct {
							Index    int `json:"index"`
							Function struct {
								Arguments string `json:"arguments"`
							} `json:"function"`
						} `json:"tool_calls"`
					} `json:"delta"`
				} `json:"choices"`
			}
			if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(frame, "data: "))), &chunk); err != nil {
				t.Fatalf("decode chunk: %v", err)
			}
			for _, choice := range chunk.Choices {
				for _, call := range choice.Delta.ToolCalls {
					got[call.Index] += call.Function.Arguments
				}
			}
		}
	}
	if len(got) != 2 || got[0] != `{"a":1}` || got[1] != `{"b":2}` {
		t.Errorf("arguments by index = %#v, want separate calls", got)
	}
}
