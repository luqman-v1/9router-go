package executor

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

// Qoder's event stream wraps every OpenAI chunk in a
// {statusCodeValue, body} envelope, and the body is itself an escaped JSON
// string. Handing those frames straight to the shared SSE folder finds no
// `choices`, which is how a working upstream surfaced as
// "200 without a completion" (HTTP 502).

// envelope builds one `data:` line the way Qoder writes it.
func envelope(t *testing.T, status int, body string) string {
	t.Helper()
	frame, err := json.Marshal(map[string]any{
		"headers":         map[string]any{"Content-Type": []string{"application/json"}},
		"body":            body,
		"statusCodeValue": status,
		"statusCode":      "OK",
	})
	if err != nil {
		t.Fatal(err)
	}
	return "data: " + string(frame) + "\n\n"
}

// rewrite runs the unwrapper over raw input and returns what a client sees.
func rewrite(t *testing.T, raw string) string {
	t.Helper()
	var out strings.Builder
	if err := qoderSSERewrite(strings.NewReader(raw), &out, "qfmodel"); err != nil {
		t.Fatalf("qoderSSERewrite: %v", err)
	}
	return out.String()
}

// frames parses the rewritten stream back into JSON chunks.
func frames(t *testing.T, sse string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, frame := range sseDataFrames([]byte(sse)) {
		var chunk map[string]any
		if err := json.Unmarshal(frame, &chunk); err != nil {
			t.Fatalf("rewritten frame is not JSON: %v\n%s", err, frame)
		}
		out = append(out, chunk)
	}
	return out
}

func TestQoderSSERewrite_UnwrapsNestedEnvelope(t *testing.T) {
	raw := envelope(t, 200, `{"id":"c1","object":"chat.completion.chunk","created":7,`+
		`"choices":[{"index":0,"delta":{"content":"pong"},"finish_reason":null}]}`)

	got := frames(t, rewrite(t, raw))
	if len(got) != 1 {
		t.Fatalf("got %d frames, want 1", len(got))
	}
	choices, _ := got[0]["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("envelope was not unwrapped: choices = %v", got[0]["choices"])
	}
	choice, _ := choices[0].(map[string]any)
	delta, _ := choice["delta"].(map[string]any)
	if delta["content"] != "pong" {
		t.Errorf("delta.content = %v, want pong", delta["content"])
	}
	if got[0]["id"] != "c1" {
		t.Errorf("id = %v, want c1", got[0]["id"])
	}
}

// The wire form is pinned here rather than built from a helper: the envelope's
// `body` is an escaped JSON string, so the frame itself is not the chunk. A
// re-serialized fixture would hide exactly the escaping this has to survive.
func TestQoderSSERewrite_UnwrapsEscapedStringBody(t *testing.T) {
	raw := "data: " + `{"headers":{"Content-Type":["application/json"]},` +
		`"body":"{\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"pong\"}}]}",` +
		`"statusCodeValue":200,"statusCode":"OK"}` + "\n\n"

	got := frames(t, rewrite(t, raw))
	if len(got) != 1 {
		t.Fatalf("got %d frames, want 1", len(got))
	}
	choices, _ := got[0]["choices"].([]any)
	if len(choices) == 0 {
		t.Fatalf("escaped body was not unwrapped: %v", got[0])
	}
	choice, _ := choices[0].(map[string]any)
	delta, _ := choice["delta"].(map[string]any)
	if delta["content"] != "pong" {
		t.Errorf("delta.content = %v, want pong", delta["content"])
	}
}

// Qoder sends an empty finish chunk and then a separate `choices: []` frame
// carrying usage. Downstream reads usage off the finish chunk and drops
// `choices: []`, so both are coalesced into one terminal chunk.
func TestQoderSSERewrite_CoalescesFinishAndUsage(t *testing.T) {
	raw := envelope(t, 200, `{"id":"c1","created":7,"choices":[{"index":0,"delta":{"content":"pong"}}]}`) +
		envelope(t, 200, `{"id":"c1","created":7,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`) +
		envelope(t, 200, `{"id":"c1","created":7,"choices":[],"usage":{"prompt_tokens":66,"completion_tokens":26,"total_tokens":92}}`) +
		"data: [DONE]\n\n"

	got := frames(t, rewrite(t, raw))
	if len(got) != 2 {
		t.Fatalf("got %d chunks, want 2 (content + one coalesced terminal)", len(got))
	}
	terminal := got[1]
	usage, ok := terminal["usage"].(map[string]any)
	if !ok {
		t.Fatalf("terminal chunk carries no usage: %v", terminal)
	}
	if usage["prompt_tokens"] != 66.0 || usage["completion_tokens"] != 26.0 {
		t.Errorf("usage = %v, want prompt 66 / completion 26", usage)
	}
	choices, _ := terminal["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("terminal chunk has no choice: %v", terminal)
	}
	choice, _ := choices[0].(map[string]any)
	if choice["finish_reason"] != "stop" {
		t.Errorf("finish_reason = %v, want stop", choice["finish_reason"])
	}
}

// A billing or quota refusal must never be handed back as assistant text:
// the client has to see it so it can fall back.
func TestQoderSSERewrite_BillingBlockIsNotAssistantText(t *testing.T) {
	raw := envelope(t, 403, `{"code":110,"message":"daily count exceeded","pricingUrl":"https://qoder.com/pricing"}`)

	got := frames(t, rewrite(t, raw))
	if len(got) != 1 {
		t.Fatalf("got %d frames, want 1", len(got))
	}
	errObj, ok := got[0]["error"].(map[string]any)
	if !ok {
		t.Fatalf("billing block was folded as an answer: %v", got[0])
	}
	if errObj["type"] != "quota_error" {
		t.Errorf("error.type = %v, want quota_error", errObj["type"])
	}
	if !strings.Contains(rewrite(t, raw), "data: [DONE]") {
		t.Error("rewound stream must terminate with [DONE]")
	}
}

// A non-billing upstream failure is marked so the non-streaming path can
// raise it as a status rather than answering 200 with the error text.
func TestQoderSSERewrite_MarksUpstreamFailure(t *testing.T) {
	raw := envelope(t, 400, `{"code":"400","message":"flow nodes not found"}`)

	got := frames(t, rewrite(t, raw))
	if len(got) != 1 {
		t.Fatalf("got %d frames, want 1", len(got))
	}
	marker, ok := got[0]["qoder_error"].(map[string]any)
	if !ok {
		t.Fatalf("failure was not marked: %v", got[0])
	}
	if marker["status"] != 400.0 {
		t.Errorf("marker.status = %v, want 400", marker["status"])
	}
	if !strings.Contains(marker["message"].(string), "flow nodes not found") {
		t.Errorf("upstream message lost: %v", marker["message"])
	}
}

// Qoder's terminal `event:finish` frame carries timings, not chat content.
// Forwarding it would put a non-chunk object into the client's stream.
func TestQoderSSERewrite_DropsFinishEvent(t *testing.T) {
	raw := envelope(t, 200, `{"id":"c1","choices":[{"index":0,"delta":{"content":"pong"}}]}`) +
		"event:finish\ndata: {\"firstTokenDuration\":922,\"totalDuration\":1272}\n\n"

	got := frames(t, rewrite(t, raw))
	if len(got) != 1 {
		t.Fatalf("got %d chunks, want 1 — the timings frame must be dropped: %v", len(got), got)
	}
}

// An empty stream must still terminate, or the client waits forever.
func TestQoderSSERewrite_EmptyStreamStillTerminates(t *testing.T) {
	got := rewrite(t, "")
	if !strings.Contains(got, "data: [DONE]") {
		t.Errorf("empty stream produced %q, want a [DONE] terminator", got)
	}
}

func TestQoderCanonicalUsage_NormalizesAlternateSpellings(t *testing.T) {
	tests := []struct {
		name       string
		raw        map[string]any
		wantPrompt int
		wantCached int
		wantReason int
		wantTotal  int
	}{
		{
			name:       "openai spellings",
			raw:        map[string]any{"prompt_tokens": 10.0, "completion_tokens": 5.0, "total_tokens": 15.0},
			wantPrompt: 10, wantTotal: 15,
		},
		{
			name:       "responses spellings",
			raw:        map[string]any{"input_tokens": 20.0, "output_tokens": 8.0},
			wantPrompt: 20, wantTotal: 28,
		},
		{
			name: "cached and reasoning tokens are carried through",
			raw: map[string]any{
				"prompt_tokens": 100.0, "completion_tokens": 20.0, "total_tokens": 120.0,
				"prompt_tokens_details":     map[string]any{"cached_tokens": 40.0},
				"completion_tokens_details": map[string]any{"reasoning_tokens": 12.0},
			},
			wantPrompt: 100, wantTotal: 120, wantCached: 40, wantReason: 12,
		},
		{name: "empty is not usage", raw: map[string]any{}, wantPrompt: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := qoderCanonicalUsage(tt.raw)
			if tt.wantPrompt == 0 {
				if got != nil {
					t.Fatalf("expected no usage, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected usage, got nil")
			}
			if got.PromptTokens != tt.wantPrompt || got.TotalTokens != tt.wantTotal {
				t.Errorf("prompt/total = %d/%d, want %d/%d", got.PromptTokens, got.TotalTokens, tt.wantPrompt, tt.wantTotal)
			}
			if got.CachedTokens != tt.wantCached {
				t.Errorf("cached = %d, want %d", got.CachedTokens, tt.wantCached)
			}
			if got.ReasoningTokens != tt.wantReason {
				t.Errorf("reasoning = %d, want %d", got.ReasoningTokens, tt.wantReason)
			}
		})
	}
}

func TestQoderIsBillingBlock(t *testing.T) {
	tests := []struct {
		name  string
		inner string
		want  bool
	}{
		{"daily billing count", `{"code":110,"message":"exceeded"}`, true},
		{"quota exhausted", `{"code":112,"message":"quota"}`, true},
		{"queue throttle", `{"code":10605,"message":"busy"}`, true},
		{"pricing page pointer", `{"message":"see pricingUrl"}`, true},
		{"ordinary model error", `{"code":400,"message":"flow nodes not found"}`, false},
		{"empty", ``, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := qoderIsBillingBlock(tt.inner); got != tt.want {
				t.Errorf("qoderIsBillingBlock(%q) = %v, want %v", tt.inner, got, tt.want)
			}
		})
	}
}
