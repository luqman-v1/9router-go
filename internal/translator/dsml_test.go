package translator

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExtractDSMLToolCalls(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		tools  []string
		want   string // expected cleaned prose
		calls  []DSMLToolRequest
		noDSML bool // true when no DSML marker expected at all
	}{
		{
			name: "single DSML tool call parsed into tool_call",
			text: `<|DSML| invoke name="run_command">
<|DSML| parameter name="command" string="true">ls -la</|DSML| parameter>
</|DSML| invoke>`,
			tools: []string{"run_command"},
			calls: []DSMLToolRequest{
				{Name: "run_command", Arguments: `{"command":"ls -la"}`},
			},
			want: "",
		},
		{
			name: "multiple DSML parameters parsed into JSON arguments object",
			text: `<|DSML| invoke name="read">
<|DSML| parameter name="path" string="true">/tmp/x</|DSML| parameter>
<|DSML| parameter name="i" string="true">Fetching Jakarta weather</|DSML| parameter>
</|DSML| invoke>`,
			tools: []string{"read"},
			calls: []DSMLToolRequest{
				{Name: "read", Arguments: `{"i":"Fetching Jakarta weather","path":"/tmp/x"}`},
			},
			want: "",
		},
		{
			name:  "text surrounding DSML block is preserved while tags removed",
			text:  "Let me check that.\n<｜｜DSML｜｜ invoke name=\"read\">\n<｜｜DSML｜｜ parameter name=\"path\" string=\"true\">https://wttr.in/Jakarta</｜｜DSML｜｜ parameter>\n</｜｜DSML｜｜ invoke>\nThen I will summarise.",
			tools: []string{"read"},
			calls: []DSMLToolRequest{
				{Name: "read", Arguments: `{"path":"https://wttr.in/Jakarta"}`},
			},
			want: "Let me check that.\n\nThen I will summarise.",
		},
		{
			name:   "normal text without DSML passes through untouched",
			text:   "Halo! Ada yang bisa dibantu?",
			tools:  []string{"read"},
			want:   "Halo! Ada yang bisa dibantu?",
			noDSML: true,
		},
		{
			name:   "text with no DSML marker returns unchanged",
			text:   "Just a regular sentence with no markup.",
			tools:  []string{"read"},
			want:   "Just a regular sentence with no markup.",
			noDSML: true,
		},
		{
			name: "undeclared tool name is rejected and markup stripped",
			text: "<｜｜DSML｜｜ invoke name=\"delete_everything\">" +
				"<｜｜DSML｜｜ parameter name=\"path\" string=\"true\">/</｜｜DSML｜｜ parameter>" +
				"</｜｜DSML｜｜ invoke>",
			tools: []string{"read"},
			want:  "",
		},
		{
			name: "both ASCII and full-width pipe spellings are recognised",
			text: "<||DSML|| invoke name=\"read\">" +
				"<||DSML|| parameter name=\"path\" string=\"true\">/tmp/x</||DSML|| parameter>" +
				"</||DSML|| invoke>",
			tools: []string{"read"},
			calls: []DSMLToolRequest{
				{Name: "read", Arguments: `{"path":"/tmp/x"}`},
			},
			want: "",
		},
		{
			name: "parameter quotes are preserved verbatim",
			text: "<｜｜DSML｜｜ invoke name=\"read\">" +
				"<｜｜DSML｜｜ parameter name=\"path\" string=\"true\">a \"quoted\" value</｜｜DSML｜｜ parameter>" +
				"</｜｜DSML｜｜ invoke>",
			tools: []string{"read"},
			calls: []DSMLToolRequest{
				{Name: "read", Arguments: `{"path":"a \"quoted\" value"}`},
			},
			want: "",
		},
		{
			name: "truncated block is not parsed and stripped entirely",
			text: "<｜｜DSML｜｜ calls>\n<｜｜DSML｜｜ invoke name=\"read\">\n" +
				"<｜｜DSML｜｜ parameter name=\"path\" string=\"true\">half",
			tools: []string{"read"},
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prose, calls := ExtractDSMLToolCalls(tt.text, tt.tools)

			if tt.noDSML {
				if prose != tt.text {
					t.Errorf("plain text altered: %q", prose)
				}
				if len(calls) != 0 {
					t.Errorf("expected no calls, got %d", len(calls))
				}
				return
			}

			if prose != tt.want {
				t.Errorf("prose = %q, want %q", prose, tt.want)
			}
			if len(calls) != len(tt.calls) {
				t.Fatalf("got %d calls, want %d", len(calls), len(tt.calls))
			}
			for i, want := range tt.calls {
				got := calls[i]
				if got.Name != want.Name {
					t.Errorf("call %d name = %q, want %q", i, got.Name, want.Name)
				}
				if got.Arguments != want.Arguments {
					t.Errorf("call %d arguments = %q, want %q", i, got.Arguments, want.Arguments)
				}
			}
		})
	}
}

func TestExtractDSMLToolCallsReadsEveryInvoke(t *testing.T) {
	const twoCity = "<｜｜DSML｜｜ calls>\n" +
		"<｜｜DSML｜｜ invoke name=\"read\">\n" +
		"<｜｜DSML｜｜ parameter name=\"path\" string=\"true\">https://wttr.in/Jakarta?format=j1</｜｜DSML｜｜ parameter>\n" +
		"</｜｜DSML｜｜ invoke>\n" +
		"<｜｜DSML｜｜ invoke name=\"read\">\n" +
		"<｜｜DSML｜｜ parameter name=\"path\" string=\"true\">https://wttr.in/Tokyo?format=j1</｜｜DSML｜｜ parameter>\n" +
		"</｜｜DSML｜｜ invoke>\n" +
		"</｜｜DSML｜｜ calls>"

	prose, calls := ExtractDSMLToolCalls(twoCity, []string{"read"})
	if len(calls) != 2 {
		t.Fatalf("extracted %d calls, want 2", len(calls))
	}
	if strings.Contains(prose, "DSML") {
		t.Fatalf("calls wrapper leaked into prose: %q", prose)
	}
	if !strings.Contains(calls[0].Arguments, "Jakarta") {
		t.Errorf("first call missing Jakarta: %s", calls[0].Arguments)
	}
	if !strings.Contains(calls[1].Arguments, "Tokyo") {
		t.Errorf("second call missing Tokyo: %s", calls[1].Arguments)
	}
}

func TestExtractDSMLToolCallsRepeatedInvokeRunsOnce(t *testing.T) {
	answer := "<｜｜DSML｜｜ invoke name=\"read\">" +
		"<｜｜DSML｜｜ parameter name=\"path\" string=\"true\">/tmp/x</｜｜DSML｜｜ parameter>" +
		"</｜｜DSML｜｜ invoke>" +
		"<｜｜DSML｜｜ invoke name=\"read\">" +
		"<｜｜DSML｜｜ parameter name=\"path\" string=\"true\">/tmp/x</｜｜DSML｜｜ parameter>" +
		"</｜｜DSML｜｜ invoke>"

	_, calls := ExtractDSMLToolCalls(answer, []string{"read"})
	if len(calls) != 2 {
		t.Fatalf("extracted %d calls, want 2 (repeats are distinct invokes)", len(calls))
	}
}

func TestDSMLValueJSONEscapes(t *testing.T) {
	if got := dsmlValue(`he said "hi" and walked\n`); got != `"he said \"hi\" and walked\\n"` {
		t.Errorf("dsmlValue = %q, want escaped JSON string", got)
	}
}

func TestTranslateOpenAIToClaudeDSMLNonStream(t *testing.T) {
	body := []byte(`{
		"id": "chatcmpl-dsml",
		"model": "deepseek-chat",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": "Running a command.\n<|DSML| invoke name=\"run_command\">\n<|DSML| parameter name=\"command\" string=\"true\">ls -la</|DSML| parameter>\n</|DSML| invoke>"
			},
			"finish_reason": "tool_calls"
		}],
		"usage": {"prompt_tokens": 10, "completion_tokens": 20}
	}`)
	out, _, err := TranslateOpenAIToClaude(body)
	if err != nil {
		t.Fatalf("TranslateOpenAIToClaude: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}

	content, ok := parsed["content"].([]any)
	if !ok || len(content) != 2 {
		t.Fatalf("expected 2 content blocks (text + tool_use), got %v", content)
	}

	textBlock := content[0].(map[string]any)
	if textBlock["type"] != "text" || strings.Contains(textBlock["text"].(string), "DSML") {
		t.Errorf("text block should not contain DSML markup: %v", textBlock)
	}

	toolBlock := content[1].(map[string]any)
	if toolBlock["type"] != "tool_use" || toolBlock["name"] != "run_command" {
		t.Errorf("expected tool_use block named run_command, got %v", toolBlock)
	}
	var args map[string]any
	rawInput, _ := json.Marshal(toolBlock["input"])
	if err := json.Unmarshal(rawInput, &args); err != nil {
		t.Fatalf("tool input not valid JSON: %v", err)
	}
	if args["command"] != "ls -la" {
		t.Errorf("command arg = %v, want ls -la", args["command"])
	}
}

func TestTranslateOpenAIToClaudeStreamDSML(t *testing.T) {
	session := "stream-dsml-1"
	defer ClearStreamState(session)

	// Chunk 1: role + content with DSML marker (buffered, not streamed as text)
	c1 := []byte(`{"id":"chatcmpl-dsml-s","model":"deepseek-chat","choices":[{"index":0,"delta":{"role":"assistant","content":"Running.<|DSML| invoke name=\"run_command\"><|DSML| parameter name=\"command\" string=\"true\">ls -la</|DSML| parameter></|DSML| invoke>"},"finish_reason":null}]}`)
	out1, err := TranslateOpenAIToClaudeStreamSession(session, c1)
	if err != nil {
		t.Fatalf("chunk1: %v", err)
	}
	if strings.Contains(string(out1), "DSML") {
		t.Errorf("DSML markup leaked into streamed output: %s", out1)
	}

	// Chunk 2: finish reason closes the stream and flushes DSML calls
	c2 := []byte(`{"id":"chatcmpl-dsml-s","model":"deepseek-chat","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
	out2, err := TranslateOpenAIToClaudeStreamSession(session, c2)
	if err != nil {
		t.Fatalf("chunk2: %v", err)
	}
	out2s := string(out2)
	if !strings.Contains(out2s, `"name":"run_command"`) {
		t.Errorf("expected run_command tool_use block, got: %s", out2s)
	}
	if !strings.Contains(out2s, `"partial_json"`) {
		t.Errorf("expected input_json_delta with arguments, got: %s", out2s)
	}

	// Terminal pair is deferred when no usage arrived; EnsureStreamClosed emits it.
	tail := EnsureStreamClosed(session)
	if !strings.Contains(string(tail), `"stop_reason":"tool_use"`) {
		t.Errorf("expected stop_reason tool_use in terminal, got: %s", tail)
	}
	if !strings.Contains(string(tail), `"type":"message_stop"`) {
		t.Errorf("expected message_stop in terminal, got: %s", tail)
	}
}

// Leading prose streamed before a DSML block must not be re-emitted at
// finish (no duplicated prose), and no DSML markup may leak.
func TestTranslateOpenAIToClaudeStreamDSMLLeadingProseNoDup(t *testing.T) {
	session := "stream-dsml-leading-prose"
	defer ClearStreamState(session)

	// Chunk 1: leading prose streamed immediately (not buffered).
	c1 := []byte(`{"id":"chatcmpl-dsml-lp","model":"deepseek-chat","choices":[{"index":0,"delta":{"role":"assistant","content":"Please run the command. "},"finish_reason":null}]}`)
	out1, err := TranslateOpenAIToClaudeStreamSession(session, c1)
	if err != nil {
		t.Fatalf("chunk1: %v", err)
	}
	out1s := string(out1)
	if !strings.Contains(out1s, "Please run the command.") {
		t.Errorf("expected leading prose streamed in chunk1, got: %s", out1s)
	}
	if strings.Contains(out1s, "DSML") {
		t.Errorf("DSML markup in chunk1: %s", out1s)
	}

	// Chunk 2: DSML block followed by trailing prose. The leading prose must
	// not be re-emitted, and no DSML markup may leak.
	c2 := []byte(`{"id":"chatcmpl-dsml-lp","model":"deepseek-chat","choices":[{"index":0,"delta":{"content":"<|DSML| invoke name=\"run_command\"><|DSML| parameter name=\"command\" string=\"true\">ls -la</|DSML| parameter></|DSML| invoke> All done."},"finish_reason":null}]}`)
	out2, err := TranslateOpenAIToClaudeStreamSession(session, c2)
	if err != nil {
		t.Fatalf("chunk2: %v", err)
	}
	out2s := string(out2)
	if strings.Contains(out2s, "DSML") {
		t.Errorf("DSML markup leaked in chunk2: %s", out2s)
	}
	if strings.Contains(out2s, "Please run the command.") {
		t.Errorf("leading prose re-emitted (duplication) in chunk2: %s", out2s)
	}

	// Chunk 3: finish flushes the tool call and trailing prose.
	c3 := []byte(`{"id":"chatcmpl-dsml-lp","model":"deepseek-chat","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
	out3, err := TranslateOpenAIToClaudeStreamSession(session, c3)
	if err != nil {
		t.Fatalf("chunk3: %v", err)
	}
	out3s := string(out3)
	if !strings.Contains(out3s, `"name":"run_command"`) {
		t.Errorf("expected run_command tool_use, got: %s", out3s)
	}
	if !strings.Contains(out3s, "All done.") {
		t.Errorf("expected trailing prose, got: %s", out3s)
	}
	if strings.Count(out3s, "Please run the command.") > 1 {
		t.Errorf("leading prose duplicated in output: %s", out3s)
	}
	if strings.Contains(out3s, "DSML") {
		t.Errorf("DSML markup leaked into final output: %s", out3s)
	}

	tail := EnsureStreamClosed(session)
	if !strings.Contains(string(tail), `"stop_reason":"tool_use"`) {
		t.Errorf("expected stop_reason tool_use in terminal, got: %s", tail)
	}
}

// A '<' at a chunk boundary must be held back until the next chunk decides
// whether it starts a DSML tag; it must never leak into streamed text.
func TestTranslateOpenAIToClaudeStreamDSMLSplitTag(t *testing.T) {
	session := "stream-dsml-split"
	defer ClearStreamState(session)

	// Chunk 1 ends with '<' which must be held back (not leaked as text).
	c1 := []byte(`{"id":"chatcmpl-dsml-split","model":"deepseek-chat","choices":[{"index":0,"delta":{"content":"Check <"},"finish_reason":null}]}`)
	out1, err := TranslateOpenAIToClaudeStreamSession(session, c1)
	if err != nil {
		t.Fatalf("chunk1: %v", err)
	}
	out1s := string(out1)
	if strings.Contains(out1s, "<") {
		t.Errorf("split '<' leaked into streamed output in chunk1: %s", out1s)
	}
	if !strings.Contains(out1s, "Check ") {
		t.Errorf("expected 'Check ' streamed in chunk1, got: %s", out1s)
	}

	// Chunk 2 completes the DSML tag.
	c2 := []byte(`{"id":"chatcmpl-dsml-split","model":"deepseek-chat","choices":[{"index":0,"delta":{"content":"|DSML| invoke name=\"read\"><|DSML| parameter name=\"path\" string=\"true\">/tmp/x</|DSML| parameter></|DSML| invoke>"},"finish_reason":null}]}`)
	out2, err := TranslateOpenAIToClaudeStreamSession(session, c2)
	if err != nil {
		t.Fatalf("chunk2: %v", err)
	}
	out2s := string(out2)
	if strings.Contains(out2s, "DSML") {
		t.Errorf("DSML markup leaked in chunk2: %s", out2s)
	}

	// Chunk 3: finish flushes the tool call.
	c3 := []byte(`{"id":"chatcmpl-dsml-split","model":"deepseek-chat","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
	out3, err := TranslateOpenAIToClaudeStreamSession(session, c3)
	if err != nil {
		t.Fatalf("chunk3: %v", err)
	}
	out3s := string(out3)
	if !strings.Contains(out3s, `"name":"read"`) {
		t.Errorf("expected read tool_use, got: %s", out3s)
	}
	if strings.Contains(out3s, "DSML") {
		t.Errorf("DSML markup leaked into final output: %s", out3s)
	}

	tail := EnsureStreamClosed(session)
	if !strings.Contains(string(tail), `"stop_reason":"tool_use"`) {
		t.Errorf("expected stop_reason tool_use in terminal, got: %s", tail)
	}
}

// A held-back split-tag probe (trailing "<", "<|", "<|DSML") must be emitted
// as text when the stream ends before the next chunk decides whether it
// starts a DSML tag, instead of being silently dropped by flushDSML.
func TestTranslateOpenAIToClaudeStreamDSMLSplitTagFlushedAtClose(t *testing.T) {
	cases := []struct {
		name string
		hold string // tail dsmlStreamSplit must hold back
		want string // tail that must be emitted as text at stream end
	}{
		{name: "trailing '<'", hold: "<", want: "<"},
		{name: "partial '<|'", hold: "<|", want: "<|"},
		{name: "partial '<|DSML'", hold: "<|DSML", want: "<|DSML"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session := "stream-dsml-flush-" + strings.ReplaceAll(tc.name, " ", "-")
			defer ClearStreamState(session)

			// Prose before the held-back tail streams immediately; the tail itself is held.
			c1 := []byte(`{"id":"chatcmpl-dsml-flush","model":"deepseek-chat","choices":[{"index":0,"delta":{"content":"Probe ` + tc.hold + `"},"finish_reason":null}]}`)
			out1, err := TranslateOpenAIToClaudeStreamSession(session, c1)
			if err != nil {
				t.Fatalf("chunk1: %v", err)
			}
			out1s := string(out1)
			if !strings.Contains(out1s, "Probe ") {
				t.Errorf("expected leading prose streamed in chunk1, got: %s", out1s)
			}
			if strings.Contains(out1s, tc.hold) {
				t.Errorf("held-back tail %q leaked into chunk1 output: %s", tc.hold, out1s)
			}

			// Stream ends while the split-tag probe is unresolved.
			c2 := []byte(`{"id":"chatcmpl-dsml-flush","model":"deepseek-chat","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
			out2, err := TranslateOpenAIToClaudeStreamSession(session, c2)
			if err != nil {
				t.Fatalf("chunk2: %v", err)
			}
			out2s := string(out2)
			if !strings.Contains(out2s, `"text":"`+tc.want+`"`) {
				t.Errorf("held-back tail %q lost at stream end, got: %s", tc.want, out2s)
			}

			tail := EnsureStreamClosed(session)
			if !strings.Contains(string(tail), `"stop_reason":"end_turn"`) {
				t.Errorf("expected end_turn terminal, got: %s", tail)
			}
			if !strings.Contains(string(tail), `"type":"message_stop"`) {
				t.Errorf("expected message_stop in terminal, got: %s", tail)
			}
		})
	}
}
