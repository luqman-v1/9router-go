package translator

import (
	"strings"
	"testing"
)

func TestTranslateClaudeChunkToOpenAI(t *testing.T) {
	state := &ClaudeToOpenAIStreamState{}

	t.Run("message_start emits role assistant chunk", func(t *testing.T) {
		payload := []byte(`{"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"union-alpha","usage":{"input_tokens":15,"output_tokens":0}}}`)
		out, err := TranslateClaudeChunkToOpenAI(payload, state)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		outStr := string(out)
		if !strings.Contains(outStr, `"role":"assistant"`) {
			t.Errorf("expected role assistant in output, got: %s", outStr)
		}
		if !strings.Contains(outStr, `"id":"chatcmpl-msg_123"`) {
			t.Errorf("expected chatcmpl ID, got: %s", outStr)
		}
	})

	t.Run("content_block_delta emits text content", func(t *testing.T) {
		payload := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello world"}}`)
		out, err := TranslateClaudeChunkToOpenAI(payload, state)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		outStr := string(out)
		if !strings.Contains(outStr, `"content":"hello world"`) {
			t.Errorf("expected content in output, got: %s", outStr)
		}
	})

	t.Run("thinking_delta emits reasoning_content", func(t *testing.T) {
		payload := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"thinking deep"}}`)
		out, err := TranslateClaudeChunkToOpenAI(payload, state)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		outStr := string(out)
		if !strings.Contains(outStr, `"reasoning_content":"thinking deep"`) {
			t.Errorf("expected reasoning_content in output, got: %s", outStr)
		}
	})

	t.Run("tool_use block start and delta", func(t *testing.T) {
		startPayload := []byte(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_abc","name":"calculator","input":{}}}`)
		out1, err := TranslateClaudeChunkToOpenAI(startPayload, state)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(string(out1), `"name":"calculator"`) {
			t.Errorf("expected tool name in start chunk, got: %s", string(out1))
		}

		deltaPayload := []byte(`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"expr\":\"1+1\"}"}}`)
		out2, err := TranslateClaudeChunkToOpenAI(deltaPayload, state)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(string(out2), `{\"expr\":\"1+1\"}`) {
			t.Errorf("expected partial json in delta chunk, got: %s", string(out2))
		}
	})

	t.Run("message_delta emits finish_reason and usage", func(t *testing.T) {
		payload := []byte(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":20}}`)
		out, err := TranslateClaudeChunkToOpenAI(payload, state)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		outStr := string(out)
		if !strings.Contains(outStr, `"finish_reason":"stop"`) {
			t.Errorf("expected finish_reason stop, got: %s", outStr)
		}
		if !strings.Contains(outStr, `"completion_tokens":20`) {
			t.Errorf("expected usage completion_tokens 20, got: %s", outStr)
		}
	})

	t.Run("message_stop emits [DONE]", func(t *testing.T) {
		payload := []byte(`{"type":"message_stop"}`)
		out, err := TranslateClaudeChunkToOpenAI(payload, state)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		outStr := string(out)
		if !strings.Contains(outStr, "data: [DONE]\n\n") {
			t.Errorf("expected [DONE], got: %s", outStr)
		}
	})
}

func TestTranslateClaudeChunkToOpenAI_Refusal(t *testing.T) {
	// Port of decolua/9router#4210: a refusal (zero output tokens, no content
	// blocks) must surface as finish_reason "content_filter" carrying
	// Anthropic's explanation — not a clean, empty "stop".
	const explanation = "This request was blocked as it seems to violate Anthropic's Terms of Service restrictions."
	state := &ClaudeToOpenAIStreamState{}

	start := []byte(`{"type":"message_start","message":{"id":"msg_refusal","role":"assistant","model":"claude-opus-5","content":[],"usage":{"input_tokens":637,"cache_creation_input_tokens":206779,"cache_read_input_tokens":0,"output_tokens":0}}}`)
	if _, err := TranslateClaudeChunkToOpenAI(start, state); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	delta := []byte(`{"type":"message_delta","delta":{"stop_reason":"refusal","stop_sequence":null,"stop_details":{"type":"refusal","category":"reasoning_extraction","explanation":"` + explanation + `"}},"usage":{"input_tokens":637,"output_tokens":0}}`)
	out, err := TranslateClaudeChunkToOpenAI(delta, state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	outStr := string(out)
	if !strings.Contains(outStr, `"finish_reason":"content_filter"`) {
		t.Errorf("expected finish_reason content_filter, got: %s", outStr)
	}
	if strings.Contains(outStr, `"finish_reason":"stop"`) {
		t.Errorf("refusal must not map to stop, got: %s", outStr)
	}
	if !strings.Contains(outStr, explanation) {
		t.Errorf("expected refusal explanation as content, got: %s", outStr)
	}
	// Prompt tokens were billed (637 + 206779 cache creation).
	if !strings.Contains(outStr, `"prompt_tokens":207416`) {
		t.Errorf("expected usage prompt_tokens 207416 on final chunk, got: %s", outStr)
	}
}

func TestTranslateClaudeResponseToOpenAI_Refusal(t *testing.T) {
	claudeJSON := []byte(`{
		"id": "msg_refusal",
		"type": "message",
		"role": "assistant",
		"model": "claude-opus-5",
		"content": [],
		"stop_reason": "refusal",
		"usage": {"input_tokens": 637, "output_tokens": 0}
	}`)
	out, err := TranslateClaudeResponseToOpenAI(claudeJSON)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outStr := string(out); !strings.Contains(outStr, `"finish_reason":"content_filter"`) {
		t.Errorf("expected finish_reason content_filter, got: %s", outStr)
	}
}

func TestTranslateOpenAIToClaude_ContentFilterRoundTrip(t *testing.T) {
	// Reverse mapping parity with #4210: content_filter -> refusal.
	openaiJSON := []byte(`{"id":"chatcmpl-x","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"content_filter"}],"usage":{"prompt_tokens":1,"completion_tokens":0,"total_tokens":1}}`)
	out, _, err := TranslateOpenAIToClaude(openaiJSON)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outStr := string(out); !strings.Contains(outStr, `"stop_reason":"refusal"`) {
		t.Errorf("expected stop_reason refusal, got: %s", outStr)
	}
}

func TestTranslateClaudeResponseToOpenAI(t *testing.T) {
	claudeJSON := []byte(`{
		"id": "msg_xyz",
		"type": "message",
		"role": "assistant",
		"model": "union-alpha",
		"content": [
			{"type": "thinking", "thinking": "Let me calculate."},
			{"type": "text", "text": "The answer is 42."},
			{"type": "tool_use", "id": "call_1", "name": "echo", "input": {"val": 42}}
		],
		"stop_reason": "tool_use",
		"usage": {
			"input_tokens": 10,
			"output_tokens": 5,
			"cache_read_input_tokens": 2
		}
	}`)

	out, err := TranslateClaudeResponseToOpenAI(claudeJSON)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	outStr := string(out)
	if !strings.Contains(outStr, `"id":"chatcmpl-msg_xyz"`) {
		t.Errorf("expected chatcmpl ID, got: %s", outStr)
	}
	if !strings.Contains(outStr, `"content":"The answer is 42."`) {
		t.Errorf("expected content, got: %s", outStr)
	}
	if !strings.Contains(outStr, `"reasoning_content":"Let me calculate."`) {
		t.Errorf("expected reasoning_content, got: %s", outStr)
	}
	if !strings.Contains(outStr, `"finish_reason":"tool_calls"`) {
		t.Errorf("expected finish_reason tool_calls, got: %s", outStr)
	}
	if !strings.Contains(outStr, `"prompt_tokens":12`) {
		t.Errorf("expected prompt_tokens 12 (10+2), got: %s", outStr)
	}
}
