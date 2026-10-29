package executor

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

// The gateway answers 11128 on these sentences by content alone. Measured by
// CARVIN94/dsh-router-codebuddy: the same account, same body, 11128 for the
// original wording and 200 for the rewrite.
func TestScrubCodebuddyChannelPhrases_RewritesEveryRow(t *testing.T) {
	for _, pair := range codebuddyChannelGatePhrases {
		got := scrubCodebuddyChannelPhrases("prefix " + pair[0] + " suffix")
		if strings.Contains(got, pair[0]) {
			t.Errorf("needle survived: %q", pair[0])
		}
		if !strings.Contains(got, pair[1]) {
			t.Errorf("replacement missing for %q: got %q", pair[0], got)
		}
		if !strings.HasPrefix(got, "prefix ") || !strings.HasSuffix(got, " suffix") {
			t.Errorf("surrounding text not preserved: %q", got)
		}
	}
}

func TestScrubCodebuddyChannelPhrases_IdempotentAndInert(t *testing.T) {
	text := "Tokens prefixed with @ are paths the user explicitly referenced. Anything else is a file:"
	once := scrubCodebuddyChannelPhrases(text)
	if twice := scrubCodebuddyChannelPhrases(once); twice != once {
		t.Errorf("not idempotent: %q -> %q", once, twice)
	}
	// Nothing to rewrite: the input must come back untouched, byte for byte.
	plain := "Please summarise this file and keep the tone neutral.   "
	if got := scrubCodebuddyChannelPhrases(plain); got != plain {
		t.Errorf("text without a gate phrase changed: %q", got)
	}
	// The truncated shape the host actually sends (the paragraph is cut before
	// the closing semicolon) is covered by the substring row.
	truncated := "Relative paths resolve from the workspace root"
	if got := scrubCodebuddyChannelPhrases(truncated); strings.Contains(got, "resolve from") {
		t.Errorf("truncated form not rewritten: %q", got)
	}
}

// Only system and assistant text is rewritten. The user's own words are never
// touched — the gateway does not fingerprint that role, and rewriting it would
// be tampering with the request.
func TestTransformCodebuddyBody_ScrubsSystemAndAssistantOnly(t *testing.T) {
	phrase := "Tokens prefixed with @ are paths the user explicitly referenced."
	body := []byte(`{"model":"glm-5.3","messages":[
		{"role":"system","content":"` + phrase + `"},
		{"role":"user","content":"` + phrase + `"},
		{"role":"assistant","content":"` + phrase + `"}
	]}`)

	out, err := transformCodebuddyBody(body)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	var sent struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if err := jsonUnmarshalForTest(out, &sent); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The transform joins the CodeBuddy prompt and every caller system part into
	// one leading system message, so the shape is system + user + assistant.
	if len(sent.Messages) != 3 {
		t.Fatalf("messages = %d, want 3 (joined system + user + assistant)", len(sent.Messages))
	}
	system := flattenContentForTest(sent.Messages[0].Content)
	if strings.Contains(system, phrase) {
		t.Errorf("system still carries the gate phrase: %q", system)
	}
	if !strings.Contains(system, "Paths that begin with @") {
		t.Errorf("system phrase was not rewritten: %q", system)
	}
	if user := flattenContentForTest(sent.Messages[1].Content); !strings.Contains(user, phrase) {
		t.Errorf("user message was rewritten: %q", user)
	}
	if assistant := flattenContentForTest(sent.Messages[2].Content); strings.Contains(assistant, phrase) {
		t.Errorf("assistant still carries the gate phrase: %q", assistant)
	}
}

func jsonUnmarshalForTest(data []byte, v any) error { return json.Unmarshal(data, v) }

// flattenContentForTest joins a message content that is either a string or a
// list of typed text blocks.
func flattenContentForTest(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var sb []string
		for _, block := range v {
			if bm, ok := block.(map[string]any); ok {
				if s, ok := bm["text"].(string); ok {
					sb = append(sb, s)
				}
			}
		}
		return joinStringsForTest(sb)
	}
	return ""
}

func joinStringsForTest(parts []string) string { return extractMessageText(toAnySliceForTest(parts)) }

func toAnySliceForTest(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}
