package chat

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestParityRepeatedArgumentDelta(t *testing.T) {
	var raw bytes.Buffer
	for _, part := range []string{`{"value":"`, "abc", "abc", `"}`} {
		payload, err := json.Marshal(map[string]any{"choices": []any{map[string]any{
			"delta": map[string]any{"tool_calls": []any{map[string]any{
				"index": 0, "id": "call-1", "function": map[string]any{"arguments": part},
			}}},
		}}})
		if err != nil {
			t.Fatal(err)
		}
		raw.WriteString("data: ")
		raw.Write(payload)
		raw.WriteString("\n\n")
	}
	raw.WriteString("data: [DONE]\n\n")
	folded, ok := sseToClaudeJSON(raw.Bytes())
	if !ok {
		t.Fatal("fold failed")
	}
	var result struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					Function struct {
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(folded, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Choices) != 1 || len(result.Choices[0].Message.ToolCalls) != 1 {
		t.Fatal("missing tool call")
	}
	var args struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(result.Choices[0].Message.ToolCalls[0].Function.Arguments), &args); err != nil {
		t.Fatal(err)
	}
	if args.Value != "abcabc" {
		t.Fatalf("value=%q", args.Value)
	}
}

func TestParityEscapedJSONArgumentDelta(t *testing.T) {
	var raw bytes.Buffer
	parts := []string{
		`{"cmd":"`,
		`echo \"hello\"`,
		` && echo \"hello\"`,
		`\nexit 0`,
		`"}`,
	}
	for _, part := range parts {
		payload, err := json.Marshal(map[string]any{"choices": []any{map[string]any{
			"delta": map[string]any{"tool_calls": []any{map[string]any{
				"index": 0, "id": "call-escaped", "function": map[string]any{"arguments": part},
			}}},
		}}})
		if err != nil {
			t.Fatal(err)
		}
		raw.WriteString("data: ")
		raw.Write(payload)
		raw.WriteString("\n\n")
	}
	raw.WriteString("data: [DONE]\n\n")

	folded, ok := sseToClaudeJSON(raw.Bytes())
	if !ok {
		t.Fatal("fold failed")
	}

	var result struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					Function struct {
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(folded, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Choices) != 1 || len(result.Choices[0].Message.ToolCalls) != 1 {
		t.Fatal("missing tool call")
	}
	var args struct {
		Cmd string `json:"cmd"`
	}
	if err := json.Unmarshal([]byte(result.Choices[0].Message.ToolCalls[0].Function.Arguments), &args); err != nil {
		t.Fatalf("unmarshal arguments failed: %v, raw arguments: %s", err, result.Choices[0].Message.ToolCalls[0].Function.Arguments)
	}
	expected := "echo \"hello\" && echo \"hello\"\nexit 0"
	if args.Cmd != expected {
		t.Fatalf("got %q, want %q", args.Cmd, expected)
	}
}

func TestParityInterleavedToolIndices(t *testing.T) {
	type chunkDef struct {
		index int
		id    string
		name  string
		arg   string
	}
	chunks := []chunkDef{
		{index: 0, id: "call-0", name: "search", arg: `{"query":"`},
		{index: 1, id: "call-1", name: "calc", arg: `{"expr":"`},
		{index: 0, arg: `dup`},
		{index: 0, arg: `dup`},
		{index: 1, arg: `100`},
		{index: 1, arg: `100`},
		{index: 0, arg: `"}`},
		{index: 1, arg: `"}`},
	}

	var raw bytes.Buffer
	for _, c := range chunks {
		tc := map[string]any{
			"index": c.index,
		}
		if c.id != "" {
			tc["id"] = c.id
		}
		fn := map[string]any{}
		if c.name != "" {
			fn["name"] = c.name
		}
		if c.arg != "" {
			fn["arguments"] = c.arg
		}
		tc["function"] = fn

		payload, err := json.Marshal(map[string]any{"choices": []any{map[string]any{
			"delta": map[string]any{"tool_calls": []any{tc}},
		}}})
		if err != nil {
			t.Fatal(err)
		}
		raw.WriteString("data: ")
		raw.Write(payload)
		raw.WriteString("\n\n")
	}
	raw.WriteString("data: [DONE]\n\n")

	folded, ok := sseToClaudeJSON(raw.Bytes())
	if !ok {
		t.Fatal("fold failed")
	}

	var result struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(folded, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Choices) != 1 || len(result.Choices[0].Message.ToolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(result.Choices[0].Message.ToolCalls))
	}

	tc0 := result.Choices[0].Message.ToolCalls[0]
	tc1 := result.Choices[0].Message.ToolCalls[1]

	if tc0.ID != "call-0" || tc0.Function.Name != "search" {
		t.Fatalf("tc0 mismatch: %+v", tc0)
	}
	var args0 struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal([]byte(tc0.Function.Arguments), &args0); err != nil {
		t.Fatalf("unmarshal tc0 args failed: %v (%s)", err, tc0.Function.Arguments)
	}
	if args0.Query != "dupdup" {
		t.Fatalf("tc0 query got %q, want %q", args0.Query, "dupdup")
	}

	if tc1.ID != "call-1" || tc1.Function.Name != "calc" {
		t.Fatalf("tc1 mismatch: %+v", tc1)
	}
	var args1 struct {
		Expr string `json:"expr"`
	}
	if err := json.Unmarshal([]byte(tc1.Function.Arguments), &args1); err != nil {
		t.Fatalf("unmarshal tc1 args failed: %v (%s)", err, tc1.Function.Arguments)
	}
	if args1.Expr != "100100" {
		t.Fatalf("tc1 expr got %q, want %q", args1.Expr, "100100")
	}
}
