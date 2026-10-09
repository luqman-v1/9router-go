package translator

import (
	"bytes"
	json "encoding/json/v2"
	"testing"
)

// decodeOpenAISSE parses the "data: {...}" lines emitted by the stream
// translator into maps, skipping blank lines and the [DONE] sentinel.
func decodeOpenAISSE(t *testing.T, out []byte) []map[string]any {
	t.Helper()
	var chunks []map[string]any
	for _, line := range bytes.Split(out, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(payload) == 0 || string(payload) == "[DONE]" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(payload, &m); err != nil {
			t.Fatalf("bad SSE payload %q: %v", payload, err)
		}
		chunks = append(chunks, m)
	}
	return chunks
}

// Gemini reports finishReason STOP even when the turn ended in functionCall
// parts. OpenAI clients (Zed, among others) only execute tools when
// finish_reason is "tool_calls", and parallel calls must carry distinct
// indexes or the client merges them into one.
func TestTranslateGeminiChunkToOpenAI_ParallelToolCalls(t *testing.T) {
	chunkJSON := `{
		"candidates": [{
			"content": {"role": "model", "parts": [
				{"functionCall": {"name": "read_file", "args": {"path": "a.go"}}},
				{"functionCall": {"name": "read_file", "args": {"path": "b.go"}}}
			]},
			"finishReason": "STOP"
		}],
		"usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 5}
	}`

	state := &GeminiStreamState{MessageId: "msg-tools", Model: "gemini-test"}
	out, err := TranslateGeminiChunkToOpenAI([]byte(chunkJSON), state)
	if err != nil {
		t.Fatalf("TranslateGeminiChunkToOpenAI failed: %v", err)
	}

	var indexes []float64
	finish := ""
	for _, c := range decodeOpenAISSE(t, out) {
		choices, _ := c["choices"].([]any)
		for _, ch := range choices {
			choice, _ := ch.(map[string]any)
			if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
				finish = fr
			}
			delta, _ := choice["delta"].(map[string]any)
			calls, _ := delta["tool_calls"].([]any)
			for _, tc := range calls {
				call, _ := tc.(map[string]any)
				idx, _ := call["index"].(float64)
				indexes = append(indexes, idx)
			}
		}
	}

	if len(indexes) != 2 || indexes[0] != 0 || indexes[1] != 1 {
		t.Errorf("tool_call indexes = %v, want [0 1]", indexes)
	}
	if finish != "tool_calls" {
		t.Errorf("finish_reason = %q, want %q", finish, "tool_calls")
	}
	if state.ToolCallCount != 2 {
		t.Errorf("state.ToolCallCount = %d, want 2", state.ToolCallCount)
	}
}

// A plain text turn must still finish with "stop".
func TestTranslateGeminiChunkToOpenAI_TextStopUnchanged(t *testing.T) {
	chunkJSON := `{"candidates": [{"content": {"parts": [{"text": "hi"}]}, "finishReason": "STOP"}]}`
	state := &GeminiStreamState{MessageId: "msg-text", Model: "gemini-test"}
	out, err := TranslateGeminiChunkToOpenAI([]byte(chunkJSON), state)
	if err != nil {
		t.Fatalf("TranslateGeminiChunkToOpenAI failed: %v", err)
	}
	finish := ""
	for _, c := range decodeOpenAISSE(t, out) {
		choices, _ := c["choices"].([]any)
		for _, ch := range choices {
			choice, _ := ch.(map[string]any)
			if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
				finish = fr
			}
		}
	}
	if finish != "stop" {
		t.Errorf("finish_reason = %q, want %q", finish, "stop")
	}
}
