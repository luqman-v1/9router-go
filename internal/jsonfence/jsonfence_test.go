package jsonfence

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

func TestWants(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"json_object object form", `{"response_format":{"type":"json_object"}}`, true},
		{"json_schema object form", `{"response_format":{"type":"json_schema","json_schema":{}}}`, true},
		{"string form", `{"response_format":"json_object"}`, true},
		{"string form json_schema", `{"response_format":"json_schema"}`, true},
		{"text default", `{"response_format":{"type":"text"}}`, false},
		{"string text", `{"response_format":"text"}`, false},
		{"absent", `{"messages":[]}`, false},
		{"null", `{"response_format":null}`, false},
		{"empty string", `{"response_format":""}`, false},
		{"empty object", `{"response_format":{}}`, false},
		{"malformed object", `{"response_format":{"type":`, false},
		{"unknown vendor string", `{"response_format":"yaml"}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Wants([]byte(tt.body)); got != tt.want {
				t.Errorf("Wants(%s) = %v, want %v", tt.body, got, tt.want)
			}
		})
	}
}

func TestStrip(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "fenced json object",
			body: "```json\n{\"city\":\"Bandung\"}\n```",
			want: `{"city":"Bandung"}`,
		},
		{
			name: "fenced json array",
			body: "```json\n[1,2,3]\n```",
			want: `[1,2,3]`,
		},
		{
			name: "bare fence without language tag",
			body: "```\n{\"a\":1}\n```",
			want: `{"a":1}`,
		},
		{
			name: "uppercase language tag",
			body: "```JSON\n{\"a\":1}\n```",
			want: `{"a":1}`,
		},
		{
			name: "surrounding whitespace",
			body: "\n\n```json\n{\"a\":1}\n```\n\n",
			want: `{"a":1}`,
		},
		{
			name: "nested fence inside the json",
			body: "```json\n{\"snippet\":\"```\\ncode\\n```\"}\n```",
			want: "{\"snippet\":\"```\\ncode\\n```\"}",
		},
		{
			name: "prose is left alone",
			body: "Here is the answer:\n```json\n{\"a\":1}\n```",
			want: "Here is the answer:\n```json\n{\"a\":1}\n```",
		},
		{
			name: "unclosed fence is left alone",
			body: "```json\n{\"a\":1}",
			want: "```json\n{\"a\":1}",
		},
		{
			name: "empty fence is left alone",
			body: "```json\n```",
			want: "```json\n```",
		},
		{
			name: "non-json fence contents are left alone",
			body: "```json\nnot json at all\n```",
			want: "```json\nnot json at all\n```",
		},
		{
			name: "plain json is untouched",
			body: `{"a":1}`,
			want: `{"a":1}`,
		},
		{
			name: "fence with prose after is left alone",
			body: "```json\n{\"a\":1}\n```\nHope that helps",
			want: "```json\n{\"a\":1}\n```\nHope that helps",
		},
		{
			// A block whose contents carry a fence of their own is opened
			// with a longer delimiter than three. Hardcoding three left that
			// answer fenced for a client that had asked for JSON.
			name: "four backtick fence",
			body: "````json\n{\"a\":1}\n````",
			want: `{"a":1}`,
		},
		{
			name: "five backtick fence",
			body: "`````json\n{\"a\":1}\n`````",
			want: `{"a":1}`,
		},
		{
			// A three-backtick run inside a four-backtick block is content,
			// not the end of the block: the closing delimiter has to be at
			// least as long as the opening one.
			name: "short closing run inside a long block is content",
			body: "````json\n{\"snippet\":\"```\\ncode\\n```\"}\n````",
			want: "{\"snippet\":\"```\\ncode\\n```\"}",
		},
		{
			name: "four backtick fence with prose after is left alone",
			body: "````json\n{\"a\":1}\n````\nHope that helps",
			want: "````json\n{\"a\":1}\n````\nHope that helps",
		},
		{
			// An unclosed long fence is left alone, exactly as a three is.
			name: "unclosed four backtick fence is left alone",
			body: "````json\n{\"a\":1}",
			want: "````json\n{\"a\":1}",
		},
		{
			// Two backticks is not a fence at all.
			name: "two backticks is not a fence",
			body: "``json\n{\"a\":1}\n``",
			want: "``json\n{\"a\":1}\n``",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(Strip([]byte(tt.body))); got != tt.want {
				t.Errorf("Strip() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestApplyGate(t *testing.T) {
	fenced := []byte("```json\n{\"a\":1}\n```")
	if got := Apply(context.Background(), fenced); string(got) != string(fenced) {
		t.Errorf("Apply without the context flag stripped the fence: %q", got)
	}
	if got := Apply(With(context.Background()), fenced); string(got) != `{"a":1}` {
		t.Errorf("Apply with the context flag = %q, want unwrapped", got)
	}
	if got := Apply(With(context.Background()), nil); got != nil {
		t.Errorf("Apply on an empty body = %q, want nil", got)
	}
	if got := Apply(nil, fenced); string(got) != string(fenced) { //nolint:staticcheck // nil context is a supported input
		t.Errorf("Apply on a nil context stripped the fence: %q", got)
	}
}

func TestApplyContents(t *testing.T) {
	ctx := With(context.Background())
	// fence is spelled via a helper so the test source carries no literal
	// backticks: Go raw strings cannot contain them, and escaping them inside
	// an interpreted string makes the cases unreadable.
	fence := func(inner string) string { return "```json\n" + inner + "\n```" }
	envelope := func(content string) string {
		return `{"choices":[{"message":{"content":` + strconv.Quote(content) + `}}]}`
	}
	t.Run("unwraps a fence inside message.content", func(t *testing.T) {
		body := []byte(envelope(fence(`{"city":"Bandung"}`)))
		got := string(ApplyContents(ctx, body))
		want := envelope(`{"city":"Bandung"}`)
		if got != want {
			t.Errorf("ApplyContents() = %s, want %s", got, want)
		}
	})
	t.Run("leaves prose content alone", func(t *testing.T) {
		body := []byte(envelope("Here is the answer:\n" + fence(`{"a":1}`)))
		if got := string(ApplyContents(ctx, body)); got != string(body) {
			t.Errorf("ApplyContents() rewrote prose content: %s", got)
		}
	})
	t.Run("leaves a non-JSON fence inside content alone", func(t *testing.T) {
		body := []byte(envelope(fence("not json")))
		if got := string(ApplyContents(ctx, body)); got != string(body) {
			t.Errorf("ApplyContents() rewrote a non-JSON fence: %s", got)
		}
	})
	t.Run("leaves plain content alone", func(t *testing.T) {
		body := []byte(envelope(`{"a":1}`))
		if got := string(ApplyContents(ctx, body)); got != string(body) {
			t.Errorf("ApplyContents() = %s, want unchanged", got)
		}
	})
	t.Run("leaves a non-chat body alone", func(t *testing.T) {
		body := []byte(`{"result":` + strconv.Quote(fence(`{"a":1}`)) + `}`)
		if got := string(ApplyContents(ctx, body)); got != string(body) {
			t.Errorf("ApplyContents() rewrote a non-chat body: %s", got)
		}
	})
	t.Run("unwraps every choice", func(t *testing.T) {
		body := []byte(`{"choices":[{"message":{"content":` + strconv.Quote(fence(`{"a":1}`)) +
			`}},{"message":{"content":` + strconv.Quote(fence(`{"b":2}`)) + `}}]}`)
		got := string(ApplyContents(ctx, body))
		want := `{"choices":[{"message":{"content":"{\"a\":1}"}},{"message":{"content":"{\"b\":2}"}}]}`
		if got != want {
			t.Errorf("ApplyContents() = %s, want %s", got, want)
		}
	})
	t.Run("unwraps a whole-body fence too", func(t *testing.T) {
		got := string(ApplyContents(ctx, []byte(fence(`{"a":1}`))))
		if got != `{"a":1}` {
			t.Errorf("ApplyContents() = %s, want unwrapped whole body", got)
		}
	})
	t.Run("no-op without the context flag", func(t *testing.T) {
		body := []byte(envelope(fence(`{"a":1}`)))
		if got := string(ApplyContents(context.Background(), body)); got != string(body) {
			t.Errorf("ApplyContents() rewrote without the context flag: %s", got)
		}
	})
	t.Run("preserves the rest of the envelope", func(t *testing.T) {
		body := []byte(`{"id":"x","usage":{"total_tokens":12},"choices":[{"message":{"content":` +
			strconv.Quote(fence(`{"a":1}`)) + `},"finish_reason":"stop"}]}`)
		got := string(ApplyContents(ctx, body))
		for _, want := range []string{`"id":"x"`, `"total_tokens":12`, `"finish_reason":"stop"`, `"content":"{\"a\":1}"`} {
			if !strings.Contains(got, want) {
				t.Errorf("ApplyContents() lost %s: %s", want, got)
			}
		}
	})
}

func TestStripDoesNotMutateInput(t *testing.T) {
	body := []byte("```json\n{\"a\":1}\n```")
	before := string(body)
	Strip(body)
	if string(body) != before {
		t.Errorf("Strip mutated its input: %q, want %q", body, before)
	}
}
