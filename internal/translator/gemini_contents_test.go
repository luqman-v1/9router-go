package translator

import "testing"

// TestNormalizeGeminiContents_TerminalTurnGuards is the Go port of upstream's
// tests/unit/gemini-contents-normalization.test.js. Gemini rejects a contents
// array that neither starts on a user turn nor ends on one, and a synthesized
// functionResponse has to carry the call's id or Gemini refuses to match it.
func TestNormalizeGeminiContents_TerminalTurnGuards(t *testing.T) {
	user := func(text string) GeminiContent {
		return GeminiContent{Role: "user", Parts: []GeminiPart{{Text: text}}}
	}
	model := func(parts ...GeminiPart) GeminiContent {
		return GeminiContent{Role: "model", Parts: parts}
	}
	text := func(s string) GeminiPart { return GeminiPart{Text: s} }
	call := func(id, name string) GeminiPart {
		return GeminiPart{FunctionCall: &GeminiFunctionCall{ID: id, Name: name}}
	}

	tests := []struct {
		name     string
		contents []GeminiContent
		want     []GeminiContent
	}{
		{
			name:     "appends Continue when ending on a model text turn",
			contents: []GeminiContent{user("hi"), model(text("hello"))},
			want: []GeminiContent{
				user("hi"), model(text("hello")), user("Continue."),
			},
		},
		{
			name:     "answers a functionCall with its id",
			contents: []GeminiContent{user("run"), model(call("call_1", "search"))},
			want: []GeminiContent{
				user("run"), model(call("call_1", "search")),
				{Role: "user", Parts: []GeminiPart{{FunctionResponse: &GeminiFunctionResp{
					Name: "search", ID: "call_1", Response: &GeminiFuncResp{Result: "Continue."},
				}}}},
			},
		},
		{
			name:     "answers every functionCall in the turn",
			contents: []GeminiContent{user("run"), model(call("call_1", "fn_1"), call("call_2", "fn_2"))},
			want: []GeminiContent{
				user("run"), model(call("call_1", "fn_1"), call("call_2", "fn_2")),
				{Role: "user", Parts: []GeminiPart{
					{FunctionResponse: &GeminiFunctionResp{
						Name: "fn_1", ID: "call_1", Response: &GeminiFuncResp{Result: "Continue."},
					}},
					{FunctionResponse: &GeminiFunctionResp{
						Name: "fn_2", ID: "call_2", Response: &GeminiFuncResp{Result: "Continue."},
					}},
				}},
			},
		},
		{
			name:     "a model turn can carry text and a call at once",
			contents: []GeminiContent{user("run"), model(text("Executing..."), call("call_3", "exec"))},
			want: []GeminiContent{
				user("run"), model(text("Executing..."), call("call_3", "exec")),
				{Role: "user", Parts: []GeminiPart{{FunctionResponse: &GeminiFunctionResp{
					Name: "exec", ID: "call_3", Response: &GeminiFuncResp{Result: "Continue."},
				}}}},
			},
		},
		{
			name:     "a lone model turn gets both brackets",
			contents: []GeminiContent{model(text("prefill"))},
			want: []GeminiContent{
				user("..."), model(text("prefill")), user("Continue."),
			},
		},
		{
			name:     "a payload already ending on user is left alone",
			contents: []GeminiContent{user("question")},
			want:     []GeminiContent{user("question")},
		},
		{
			name:     "a nameless idless call falls back",
			contents: []GeminiContent{user("Go"), model(GeminiPart{FunctionCall: &GeminiFunctionCall{}})},
			want: []GeminiContent{
				user("Go"), model(GeminiPart{FunctionCall: &GeminiFunctionCall{}}),
				{Role: "user", Parts: []GeminiPart{{FunctionResponse: &GeminiFunctionResp{
					Name: "tool", Response: &GeminiFuncResp{Result: "Continue."},
				}}}},
			},
		},
		{
			name:     "adjacent model turns merge before the guard runs",
			contents: []GeminiContent{user("Prompt"), model(text("Part A")), model(text("Part B"))},
			want: []GeminiContent{
				user("Prompt"), model(text("Part A"), text("Part B")), user("Continue."),
			},
		},
		{
			name: "thought parts still count as a model turn",
			contents: []GeminiContent{user("Solve math"), model(
				GeminiPart{Text: "Let 2x = 4...", Thought: new(true)},
				GeminiPart{Text: "", ThoughtSignature: "sig123"},
			)},
			want: []GeminiContent{
				user("Solve math"),
				model(
					GeminiPart{Text: "Let 2x = 4...", Thought: new(true)},
					GeminiPart{Text: "", ThoughtSignature: "sig123"},
				),
				user("Continue."),
			},
		},
		{
			name:     "empty input stays empty",
			contents: nil,
			want:     []GeminiContent{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeGeminiContents(tt.contents)

			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d\ngot  %+v\nwant %+v", len(got), len(tt.want), got, tt.want)
			}
			for i := range got {
				if got[i].Role != tt.want[i].Role || len(got[i].Parts) != len(tt.want[i].Parts) {
					t.Fatalf("turn %d = %+v, want %+v", i, got[i], tt.want[i])
				}
				for j := range got[i].Parts {
					g, w := got[i].Parts[j], tt.want[i].Parts[j]
					if g.Text != w.Text || g.ThoughtSignature != w.ThoughtSignature {
						t.Errorf("turn %d part %d = %+v, want %+v", i, j, g, w)
					}
					if (g.FunctionCall == nil) != (w.FunctionCall == nil) ||
						(g.FunctionResponse == nil) != (w.FunctionResponse == nil) {
						t.Fatalf("turn %d part %d call/response shape = %+v, want %+v", i, j, g, w)
					}
					if g.FunctionCall != nil && (g.FunctionCall.ID != w.FunctionCall.ID || g.FunctionCall.Name != w.FunctionCall.Name) {
						t.Errorf("turn %d call = %+v, want %+v", i, *g.FunctionCall, *w.FunctionCall)
					}
					if g.FunctionResponse != nil {
						wf := *w.FunctionResponse
						if g.FunctionResponse.ID != wf.ID || g.FunctionResponse.Name != wf.Name {
							t.Errorf("turn %d response = %+v, want %+v", i, *g.FunctionResponse, wf)
						}
						if g.FunctionResponse.Response == nil || g.FunctionResponse.Response.Result != "Continue." {
							t.Errorf("turn %d response payload = %+v, want result Continue.", i, g.FunctionResponse.Response)
						}
					}
				}
			}
		})
	}
}
