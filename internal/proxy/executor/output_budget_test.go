package executor

import (
	json "encoding/json/v2"
	"testing"
)

// The floor exists for one failure: a reasoning model given a small budget
// spends it all on chain-of-thought and answers 200 with empty content and
// finish_reason "length". These pin the boundary and, just as importantly, that
// nothing else about the caller's budget changes.
func TestClampOutputBudget(t *testing.T) {
	tests := []struct {
		name      string
		body      map[string]any
		reasoning bool
		ceiling   int
		want      map[string]any
		wantRaise bool
	}{
		{
			name:      "reasoning model below the floor is raised",
			body:      map[string]any{"max_tokens": int64(50)},
			reasoning: true,
			want:      map[string]any{"max_tokens": outputReasoningFloor},
			wantRaise: true,
		},
		{
			name:      "reasoning model exactly at the floor is untouched",
			body:      map[string]any{"max_tokens": int64(outputReasoningFloor)},
			reasoning: true,
			want:      map[string]any{"max_tokens": int64(outputReasoningFloor)},
		},
		{
			name:      "reasoning model above the floor keeps its budget",
			body:      map[string]any{"max_tokens": int64(128000)},
			reasoning: true,
			want:      map[string]any{"max_tokens": int64(128000)},
		},
		{
			name:      "non-reasoning model gets the smaller floor",
			body:      map[string]any{"max_tokens": int64(50)},
			reasoning: false,
			want:      map[string]any{"max_tokens": outputMinPlainTokens},
			wantRaise: true,
		},
		{
			name:      "no budget is never synthesized",
			body:      map[string]any{"input": "hi"},
			reasoning: true,
			want:      map[string]any{"input": "hi"},
		},
		{
			name:      "zero is not a budget to raise",
			body:      map[string]any{"max_tokens": int64(0)},
			reasoning: true,
			want:      map[string]any{"max_tokens": outputReasoningFloor},
			wantRaise: true,
		},
		{
			name:      "a string budget is not a budget",
			body:      map[string]any{"max_tokens": "50"},
			reasoning: true,
			want:      map[string]any{"max_tokens": "50"},
		},
		{
			name:      "all three spellings are raised together",
			body:      map[string]any{"max_tokens": int64(10), "max_output_tokens": float64(20), "max_completion_tokens": int(30)},
			reasoning: true,
			want: map[string]any{
				"max_tokens":            outputReasoningFloor,
				"max_output_tokens":     outputReasoningFloor,
				"max_completion_tokens": outputReasoningFloor,
			},
			wantRaise: true,
		},
		{
			// A model with less output room than the floor cannot honour it;
			// asking anyway would turn a recovered request into a 400.
			name:      "the model ceiling caps the floor",
			body:      map[string]any{"max_tokens": int64(50)},
			reasoning: true,
			ceiling:   2048,
			want:      map[string]any{"max_tokens": 2048},
			wantRaise: true,
		},
		{
			name:      "unknown ceiling falls back to the ecosystem default",
			body:      map[string]any{"max_tokens": int64(50)},
			reasoning: true,
			ceiling:   0,
			want:      map[string]any{"max_tokens": outputReasoningFloor},
			wantRaise: true,
		},
		{
			name:      "nil body is a no-op",
			body:      nil,
			reasoning: true,
			want:      nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raised := clampOutputBudget(tt.body, tt.reasoning, tt.ceiling)
			if tt.wantRaise != (len(raised) > 0) {
				t.Errorf("raised = %v, wantRaise = %v", raised, tt.wantRaise)
			}
			if len(tt.body) != len(tt.want) {
				t.Fatalf("body = %v, want %v", tt.body, tt.want)
			}
			for key, want := range tt.want {
				if !sameValue(tt.body[key], want) {
					t.Errorf("body[%q] = %#v, want %#v", key, tt.body[key], want)
				}
			}
		})
	}
}

// Every free OpenCode id with a capability row reasons, so the lookup must
// resolve them to the reasoning floor rather than the smaller plain one.
// beam-free is deliberately absent: it is catalogued but has no capability row,
// so it resolves as non-reasoning. Adding that row changes GetThinkingLevels
// for ocg/beam-free and breaks the upstream fixtures test, so it belongs to its
// own change rather than riding along here.
func TestClampOpenCodeOutputBudgetResolvesFreeModels(t *testing.T) {
	for _, model := range []string{
		"mimo-v2.6-flash-free", "glm-5.3-flash-free", "space-bunny-free",
		"big-pickle", "longcat-2.5-preview-free", "nemotron-3-ultra-free",
		"deepseek-v4.1-flash-free",
	} {
		t.Run(model, func(t *testing.T) {
			body := map[string]any{"max_tokens": int64(50)}
			raised := clampOpenCodeOutputBudget(body, "opencode", model)
			if len(raised) != 1 || raised[0] != "max_tokens" {
				t.Fatalf("raised = %v, want [max_tokens]", raised)
			}
			budget, _ := outputBudgetOf(body["max_tokens"])
			if budget != outputReasoningFloor {
				t.Errorf("budget = %d, want the reasoning floor %d", budget, outputReasoningFloor)
			}
		})
	}
}

// A caller-supplied stream_options is theirs; only an absent one is filled.
func TestIncludeStreamUsage(t *testing.T) {
	t.Run("absent is filled", func(t *testing.T) {
		body := map[string]any{"stream": true}
		includeStreamUsage(body)
		opts, ok := body["stream_options"].(map[string]any)
		if !ok || opts["include_usage"] != true {
			t.Errorf("stream_options = %v, want include_usage true", body["stream_options"])
		}
	})
	t.Run("a caller setting is not overridden", func(t *testing.T) {
		body := map[string]any{"stream_options": map[string]any{"include_usage": false}}
		includeStreamUsage(body)
		opts := body["stream_options"].(map[string]any)
		if opts["include_usage"] != false {
			t.Errorf("include_usage = %v, want the caller's false", opts["include_usage"])
		}
	})
	t.Run("nil body is a no-op", func(t *testing.T) {
		includeStreamUsage(nil)
	})
}

// include_usage alone fixes nothing: the counts ride a final chunk with an empty
// choices array, which the aggregator used to skip entirely.
func TestSSEToOpenAIJSONKeepsUsageFromAnEmptyChoicesChunk(t *testing.T) {
	raw := []byte(
		`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}` + "\n\n" +
			`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
			`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}` + "\n\n" +
			`data: [DONE]` + "\n\n")

	out, ok := sseToOpenAIJSON(raw)
	if !ok {
		t.Fatal("expected an aggregated chat completion")
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal aggregated body: %v", err)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "hi" {
		t.Fatalf("choices = %+v, want the content preserved", resp.Choices)
	}
	if resp.Usage == nil {
		t.Fatal("usage missing: the trailing empty-choices chunk must be read")
	}
	if resp.Usage.PromptTokens != 11 || resp.Usage.CompletionTokens != 7 || resp.Usage.TotalTokens != 18 {
		t.Errorf("usage = %+v, want 11/7/18", resp.Usage)
	}
}
