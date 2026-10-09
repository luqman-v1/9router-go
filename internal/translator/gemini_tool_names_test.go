package translator

import (
	json "encoding/json/v2"
	"testing"
)

// emittedNames translates an OpenAI body and returns the functionCall names
// and functionResponse names, in document order.
func emittedNames(t *testing.T, body string) (calls, resps []string) {
	t.Helper()
	out, err := TranslateOpenAIToGemini([]byte(body))
	if err != nil {
		t.Fatalf("TranslateOpenAIToGemini: %v", err)
	}
	var req GeminiRequest
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatalf("unmarshal gemini request: %v", err)
	}
	for _, c := range req.Contents {
		for _, p := range c.Parts {
			if p.FunctionCall != nil {
				calls = append(calls, p.FunctionCall.Name)
			}
			if p.FunctionResponse != nil {
				resps = append(resps, p.FunctionResponse.Name)
			}
		}
	}
	return calls, resps
}

// TestTranslateOpenAIToGemini_ToolResultNamePairsWithItsCall is the Go port of
// the name-pairing half of upstream #4589. An OpenAI tool_call_id is only unique
// within its own assistant turn, so a long agent session can replay one id; the
// functionResponse must then still carry the name of the call it answers, not
// the name of the later tool that reused the id.
func TestTranslateOpenAIToGemini_ToolResultNamePairsWithItsCall(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantCall []string
		wantResp []string
	}{
		{
			name: "repeated id across turns keeps each result on its own tool",
			body: `{"messages":[
				{"role":"assistant","tool_calls":[{"id":"call_x","type":"function","function":{"name":"edit","arguments":"{}"}}]},
				{"role":"tool","tool_call_id":"call_x","content":"1"},
				{"role":"assistant","tool_calls":[{"id":"call_x","type":"function","function":{"name":"bash","arguments":"{}"}}]},
				{"role":"tool","tool_call_id":"call_x","content":"2"}
			]}`,
			wantCall: []string{"edit", "bash"},
			wantResp: []string{"edit", "bash"},
		},
		{
			name: "repeated id within one turn",
			body: `{"messages":[
				{"role":"assistant","tool_calls":[
					{"id":"dup","type":"function","function":{"name":"one","arguments":"{}"}},
					{"id":"dup","type":"function","function":{"name":"two","arguments":"{}"}}]},
				{"role":"tool","tool_call_id":"dup","content":"1"},
				{"role":"tool","tool_call_id":"dup","content":"2"}
			]}`,
			wantCall: []string{"one", "two"},
			wantResp: []string{"one", "two"},
		},
		{
			name: "already-unique ids are unchanged",
			body: `{"messages":[
				{"role":"assistant","tool_calls":[{"id":"call_a","type":"function","function":{"name":"alpha","arguments":"{}"}}]},
				{"role":"tool","tool_call_id":"call_a","content":"1"},
				{"role":"assistant","tool_calls":[{"id":"call_b","type":"function","function":{"name":"beta","arguments":"{}"}}]},
				{"role":"tool","tool_call_id":"call_b","content":"2"}
			]}`,
			wantCall: []string{"alpha", "beta"},
			wantResp: []string{"alpha", "beta"},
		},
		{
			name: "extra result for a known id falls back to that id's tool name",
			body: `{"messages":[
				{"role":"assistant","tool_calls":[{"id":"call_z","type":"function","function":{"name":"alpha","arguments":"{}"}}]},
				{"role":"tool","tool_call_id":"call_z","content":"1"},
				{"role":"tool","tool_call_id":"call_z","content":"2"}
			]}`,
			wantCall: []string{"alpha"},
			wantResp: []string{"alpha", "alpha"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls, resps := emittedNames(t, tt.body)
			if len(calls) != len(tt.wantCall) {
				t.Fatalf("functionCall names = %v, want %v", calls, tt.wantCall)
			}
			for i := range calls {
				if calls[i] != tt.wantCall[i] {
					t.Errorf("functionCall[%d] name = %q, want %q", i, calls[i], tt.wantCall[i])
				}
			}
			if len(resps) != len(tt.wantResp) {
				t.Fatalf("functionResponse names = %v, want %v", resps, tt.wantResp)
			}
			for i := range resps {
				if resps[i] != tt.wantResp[i] {
					t.Errorf("functionResponse[%d] name = %q, want %q", i, resps[i], tt.wantResp[i])
				}
			}
		})
	}
}

// TestGeminiFinishToOpenAI is the Go port of the finish-reason residual of
// upstream #4571: both the non-stream and the streaming Gemini translator route
// through toOpenAIFinish(reason, "gemini"), which maps MAX_TOKENS to "length"
// and the blocked reasons to "content_filter" instead of a bare "stop".
func TestGeminiFinishToOpenAI(t *testing.T) {
	tests := []struct {
		reason string
		want   string
	}{
		{"STOP", "stop"},
		{"MAX_TOKENS", "length"},
		{"SAFETY", "content_filter"},
		{"RECITATION", "content_filter"},
		{"BLOCKLIST", "content_filter"},
		{"PROHIBITED_CONTENT", "content_filter"},
		{"OTHER", "stop"},
		{"FINISH_REASON_UNSPECIFIED", "stop"},
		{"", "stop"},
		{"stop", "stop"},
		{"max_tokens", "length"},
	}

	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			if got := geminiFinishToOpenAI(tt.reason); got != tt.want {
				t.Errorf("geminiFinishToOpenAI(%q) = %q, want %q", tt.reason, got, tt.want)
			}
		})
	}
}
