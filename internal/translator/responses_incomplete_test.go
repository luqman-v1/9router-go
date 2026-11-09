package translator

import (
	"testing"
)

// A tool call whose argument buffer does not parse is what a stream cut
// mid-arguments leaves behind (#747). The turn must be reported as
// response.incomplete: a `status: "completed"` terminal is what told Codex the
// turn had succeeded while the arguments it was about to run were cut
// mid-token, and the client had no signal to retry on.
func TestTranslateOpenAIToResponses_TruncatedToolArgumentsAreIncomplete(t *testing.T) {
	tests := []struct {
		name         string
		args         string
		customTool   bool
		finishReason string
		wantEvent    string
		wantStatus   string
		wantReason   string
	}{
		{
			name:       "arguments cut mid-token",
			args:       `{"cmd":"python3 - <<'PY'\n# line 1`,
			wantEvent:  "response.incomplete",
			wantStatus: responsesStatusIncomplete,
			wantReason: responsesIncompleteToolArguments,
		},
		{
			name:       "arguments cut before any token",
			args:       `{`,
			wantEvent:  "response.incomplete",
			wantStatus: responsesStatusIncomplete,
			wantReason: responsesIncompleteToolArguments,
		},
		{
			// The upstream said why: finish_reason "length" means the output
			// limit cut the call, which the Responses API already names.
			name:         "arguments cut by the output limit",
			args:         `{"cmd":"python3 - <<'PY'\n# line 1`,
			finishReason: "length",
			wantEvent:    "response.incomplete",
			wantStatus:   responsesStatusIncomplete,
			wantReason:   responsesIncompleteMaxOutputTokens,
		},
		{
			name:       "complete arguments stay completed",
			args:       `{"cmd":"ls"}`,
			wantEvent:  "response.completed",
			wantStatus: responsesStatusCompleted,
		},
		{
			// A provider that never sent arguments is well-formed, not
			// truncated: closeToolCall substitutes "{}".
			name:       "no arguments at all stay completed",
			args:       "",
			wantEvent:  "response.completed",
			wantStatus: responsesStatusCompleted,
		},
		{
			// A custom tool's input is a freeform program, not JSON, so a
			// buffer that does not parse is the normal case and must not be
			// mistaken for truncation.
			name:       "custom tool freeform input stays completed",
			args:       `{"input":"python3 - <<'PY'\nprint(1)`,
			customTool: true,
			wantEvent:  "response.completed",
			wantStatus: responsesStatusCompleted,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := respState()
			if tt.customTool {
				s.SetCustomToolNames([]string{"exec"})
			}
			name := "exec_command"
			if tt.customTool {
				name = "exec"
			}

			TranslateOpenAIToResponses(&OpenAIChunk{
				Choices: []OpenAIChoice{{Delta: OpenAIDelta{ToolCalls: []OpenAIToolCallStream{{
					Index:    intPtr(0),
					ID:       "call_1",
					Function: &OpenAIFunctionStream{Name: name, Arguments: tt.args},
				}}}}},
			}, s)

			// Without a finish_reason the stream simply ended; a case that
			// supplies one is a truncation the upstream named.
			if tt.finishReason != "" {
				TranslateOpenAIToResponses(&OpenAIChunk{
					Choices: []OpenAIChoice{{FinishReason: &tt.finishReason}},
				}, s)
			}
			events := FlushResponses(s)

			terminal := findEvent(t, events, tt.wantEvent)
			response := terminal["response"].(map[string]any)
			if got := response["status"]; got != tt.wantStatus {
				t.Errorf("status = %v, want %v", got, tt.wantStatus)
			}
			if terminal["type"] != tt.wantEvent {
				t.Errorf("type = %v, want %v", terminal["type"], tt.wantEvent)
			}

			details, hasDetails := response["incomplete_details"].(map[string]any)
			if tt.wantReason == "" {
				if hasDetails {
					t.Errorf("incomplete_details = %v on a complete turn", details)
				}
				return
			}
			if !hasDetails {
				t.Fatalf("no incomplete_details on an incomplete turn: %v", response)
			}
			if details["reason"] != tt.wantReason {
				t.Errorf("incomplete_details.reason = %v, want %v", details["reason"], tt.wantReason)
			}
		})
	}
}

// The truncated item still closes, and the client still receives the bytes that
// arrived: withholding them would leave the client with a function_call item it
// never got an end for, which is a worse failure than an unparsable one it can
// report.
func TestTranslateOpenAIToResponses_TruncatedToolArgumentsStillCloseTheItem(t *testing.T) {
	s := respState()
	const truncated = `{"cmd":"python3 - <<'PY'\n# line 1`

	TranslateOpenAIToResponses(&OpenAIChunk{
		Choices: []OpenAIChoice{{Delta: OpenAIDelta{ToolCalls: []OpenAIToolCallStream{{
			Index:    intPtr(0),
			ID:       "call_1",
			Function: &OpenAIFunctionStream{Name: "exec_command", Arguments: truncated},
		}}}}},
	}, s)

	events := FlushResponses(s)

	done := findEvent(t, events, "response.function_call_arguments.done")
	if done["arguments"] != truncated {
		t.Errorf("done arguments = %q, want the bytes that arrived", done["arguments"])
	}
	item := findEvent(t, events, "response.output_item.done")["item"].(map[string]any)
	if item["arguments"] != truncated {
		t.Errorf("closed item arguments = %q, want the bytes that arrived", item["arguments"])
	}
}

// One bad tool call makes the whole turn incomplete, and a second close must not
// overwrite the first reason or emit a second terminal event.
func TestTranslateOpenAIToResponses_IncompleteIsStickyAndTerminalIsSingle(t *testing.T) {
	s := respState()

	TranslateOpenAIToResponses(&OpenAIChunk{
		Choices: []OpenAIChoice{{Delta: OpenAIDelta{ToolCalls: []OpenAIToolCallStream{
			{Index: intPtr(0), ID: "call_1", Function: &OpenAIFunctionStream{Name: "a", Arguments: `{"x":`}},
			{Index: intPtr(1), ID: "call_2", Function: &OpenAIFunctionStream{Name: "b", Arguments: `{"y":1}`}},
		}}}},
	}, s)

	events := FlushResponses(s)

	if got := countEvents(events, "response.incomplete"); got != 1 {
		t.Errorf("response.incomplete count = %d, want 1", got)
	}
	if got := countEvents(events, "response.completed"); got != 0 {
		t.Errorf("response.completed count = %d, want 0", got)
	}

	// A flush after the terminal event is a no-op, as it always was.
	if again := FlushResponses(s); len(again) != 0 {
		t.Errorf("second flush emitted %v", eventNames(again))
	}
}

func countEvents(events []ResponsesEvent, name string) int {
	n := 0
	for _, ev := range events {
		if ev.Event == name {
			n++
		}
	}
	return n
}
