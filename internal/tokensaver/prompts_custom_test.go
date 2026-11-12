package tokensaver

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

func TestInjectPrefixSystemPrompt(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		text         string
		claudeNative bool
		skip         bool // true: already injected, body must come back unchanged
		want         []string
		wantAbsent   []string
	}{
		{
			name: "chat prepends to existing system message",
			body: `{"messages":[{"role":"system","content":"base"},{"role":"user","content":"hi"}]}`,
			text: "HEAD",
			want: []string{`"content":"HEAD\n\nbase"`, `"role":"user"`},
		},
		{
			name: "chat inserts system message at index 0 when none exists",
			body: `{"messages":[{"role":"user","content":"hi"}]}`,
			text: "HEAD",
			want: []string{`"content":"HEAD"`, `"role":"system"`, `"content":"hi"`},
		},
		{
			name:       "chat skips when text already present",
			body:       `{"messages":[{"role":"system","content":"HEAD\n\nbase"}]}`,
			text:       "HEAD",
			skip:       true,
			wantAbsent: []string{`HEAD\n\nHEAD`},
		},
		{
			name:         "claude prepends to system string",
			body:         `{"system":"base","messages":[{"role":"user","content":"hi"}]}`,
			text:         "HEAD",
			claudeNative: true,
			want:         []string{`"system":"HEAD\n\nbase"`},
		},
		{
			name:         "claude prepends text block to block array",
			body:         `{"system":[{"type":"text","text":"base"}],"messages":[]}`,
			text:         "HEAD",
			claudeNative: true,
			want:         []string{`"text":"HEAD"`, `"text":"base"`},
		},
		{
			name:         "claude sets system when absent",
			body:         `{"messages":[{"role":"user","content":"hi"}]}`,
			text:         "HEAD",
			claudeNative: true,
			want:         []string{`"system":"HEAD"`},
		},
		{
			name: "responses prepends to instructions",
			body: `{"instructions":"base","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`,
			text: "HEAD",
			want: []string{`"instructions":"HEAD\n\nbase"`},
		},
		{
			name: "responses prepends block to system item content",
			body: `{"input":[{"type":"message","role":"system","content":[{"type":"input_text","text":"base"}]}]}`,
			text: "HEAD",
			want: []string{`"text":"HEAD"`, `"text":"base"`},
		},
		{
			name: "responses inserts system item at index 0",
			body: `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`,
			text: "HEAD",
			want: []string{`"text":"HEAD"`, `"role":"system"`, `"role":"user"`},
		},
		{
			name: "blank text is a no-op",
			body: `{"messages":[{"role":"user","content":"hi"}]}`,
			text: "   ",
			want: []string{`{"role":"user","content":"hi"}`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, did := InjectPrefixSystemPrompt([]byte(tt.body), tt.text, tt.claudeNative)
			if tt.text == "   " || tt.skip {
				if did {
					t.Fatalf("expected no injection, got %s", got)
				}
			} else if !did {
				t.Fatalf("InjectPrefixSystemPrompt() did = false, want true")
			}
			assertBodyContains(t, got, tt.want, tt.wantAbsent)
		})
	}
}

func TestInjectSuffixSystemPrompt(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		text         string
		claudeNative bool
		want         []string
		wantAbsent   []string
	}{
		{
			name: "chat appends to system message",
			body: `{"messages":[{"role":"system","content":"base"},{"role":"user","content":"hi"}]}`,
			text: "TAIL",
			want: []string{`"content":"base\n\nTAIL"`},
		},
		{
			name: "chat appends to the last of several system messages",
			body: `{"messages":[{"role":"system","content":"one"},{"role":"system","content":"two"}]}`,
			text: "TAIL",
			want: []string{`"content":"one"`, `"content":"two\n\nTAIL"`},
		},
		{
			name: "chat creates system message when none exists",
			body: `{"messages":[{"role":"user","content":"hi"}]}`,
			text: "TAIL",
			want: []string{`"content":"TAIL"`, `"role":"system"`},
		},
		{
			name: "chat inserts a new message instead of clobbering block content",
			body: `{"messages":[{"role":"system","content":[{"type":"text","text":"base"}]}]}`,
			text: "TAIL",
			want: []string{`"content":"TAIL"`, `"role":"system"`, `"text":"base"`},
		},
		{
			name:         "claude appends to system string",
			body:         `{"system":"base","messages":[]}`,
			text:         "TAIL",
			claudeNative: true,
			want:         []string{`"system":"base\n\nTAIL"`},
		},
		{
			name:         "claude appends text block to block array",
			body:         `{"system":[{"type":"text","text":"base"}],"messages":[]}`,
			text:         "TAIL",
			claudeNative: true,
			want:         []string{`"text":"base"`, `"text":"TAIL"`},
		},
		{
			name: "responses appends to instructions",
			body: `{"instructions":"base","input":[]}`,
			text: "TAIL",
			want: []string{`"instructions":"base\n\nTAIL"`},
		},
		{
			name: "responses appends block to system item content",
			body: `{"input":[{"type":"message","role":"system","content":[{"type":"input_text","text":"base"}]}]}`,
			text: "TAIL",
			want: []string{`"text":"base"`, `"text":"TAIL"`},
		},
		{
			name: "responses adds a system item when none exists",
			body: `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`,
			text: "TAIL",
			want: []string{`"text":"TAIL"`, `"role":"system"`, `"role":"user"`},
		},
		{
			name: "non-json body is left alone",
			body: `not json`,
			text: "TAIL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, did := InjectSuffixSystemPrompt([]byte(tt.body), tt.text, tt.claudeNative)
			if tt.name == "non-json body is left alone" {
				if did {
					t.Fatalf("unparseable body must not report a change, got %s", got)
				}
				if string(got) != tt.body {
					t.Fatalf("unparseable body = %s, want unchanged %s", got, tt.body)
				}
				return
			}
			if !did {
				t.Fatalf("InjectSuffixSystemPrompt() did = false, want true")
			}
			assertBodyContains(t, got, tt.want, tt.wantAbsent)
		})
	}
}

// TestPrefixThenSuffixOrder pins the dual-slot contract: the prefix text leads
// the system prompt and the suffix text is its last word, whatever the order
// the two injectors run in.
func TestPrefixThenSuffixOrder(t *testing.T) {
	bodies := map[string]struct {
		body         string
		claudeNative bool
	}{
		"chat":      {body: `{"messages":[{"role":"system","content":"base"},{"role":"user","content":"hi"}]}`},
		"claude":    {body: `{"system":"base","messages":[]}`, claudeNative: true},
		"responses": {body: `{"instructions":"base","input":[]}`},
		"empty":     {body: `{"messages":[{"role":"user","content":"hi"}]}`},
	}

	for name, tc := range bodies {
		t.Run(name, func(t *testing.T) {
			got, did := InjectPrefixSystemPrompt([]byte(tc.body), "HEAD", tc.claudeNative)
			if !did {
				t.Fatalf("prefix inject did = false")
			}
			got, did = InjectSuffixSystemPrompt(got, "TAIL", tc.claudeNative)
			if !did {
				t.Fatalf("suffix inject did = false")
			}
			text := systemTextOf(t, got, tc.claudeNative)
			if !strings.HasPrefix(text, "HEAD") {
				t.Errorf("system prompt %q does not start with the prefix", text)
			}
			if !strings.HasSuffix(text, "TAIL") {
				t.Errorf("system prompt %q does not end with the suffix", text)
			}
		})
	}
}

// systemTextOf flattens whatever shape the system prompt ended up in, so the
// ordering assertions do not depend on JSON key order.
func systemTextOf(t *testing.T, body []byte, claudeNative bool) string {
	t.Helper()
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if claudeNative {
		switch sys := req["system"].(type) {
		case string:
			return sys
		case []any:
			return flattenBlocks(sys, "text")
		}
	}
	if instr, ok := req["instructions"].(string); ok && instr != "" {
		return instr
	}
	if items := toMessageArray(req["input"]); items != nil {
		for _, item := range items {
			im, ok := item.(map[string]any)
			if !ok {
				continue
			}
			role, _ := im["role"].(string)
			if role != "system" && role != "developer" {
				continue
			}
			if s, ok := im["content"].(string); ok {
				return s
			}
			if blocks := toMessageArray(im["content"]); blocks != nil {
				return flattenBlocks(blocks, "text")
			}
		}
	}
	arr := toMessageArray(req["messages"])
	if arr == nil {
		return ""
	}
	var parts []string
	for _, m := range arr {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		role, _ := mm["role"].(string)
		if role != "system" && role != "developer" {
			continue
		}
		if s, ok := mm["content"].(string); ok {
			parts = append(parts, s)
			continue
		}
		if blocks := toMessageArray(mm["content"]); blocks != nil {
			parts = append(parts, flattenBlocks(blocks, "text"))
		}
	}
	return strings.Join(parts, "\n\n")
}

func flattenBlocks(blocks []any, key string) string {
	var parts []string
	for _, b := range blocks {
		bm, ok := b.(map[string]any)
		if !ok {
			continue
		}
		if s, ok := bm[key].(string); ok {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n\n")
}

func assertBodyContains(t *testing.T, got []byte, want, absent []string) {
	t.Helper()
	s := string(got)
	for _, w := range want {
		if !strings.Contains(s, w) {
			t.Errorf("result missing %q\ngot: %s", w, s)
		}
	}
	for _, a := range absent {
		if strings.Contains(s, a) {
			t.Errorf("result must not contain %q\ngot: %s", a, s)
		}
	}
}

// TestPonytailLevelsShareTheUpstreamDoctrine pins that every level carries the
// v5.3.3 skill body: a level that drops the scope rule or the shortcut format
// silently stops behaving like upstream's ponytail.
func TestPonytailLevelsShareTheUpstreamDoctrine(t *testing.T) {
	for _, level := range []string{"lite", "full", "ultra"} {
		got := GetPonytailPrompt(level)
		for _, want := range []string{
			"shortcut: <the limit>, <when to upgrade>",
			"That is scope. Extra features are not.",
			"what you skipped or did not check",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("level %q lost %q", level, want)
			}
		}
	}
	if GetPonytailPrompt("ultra") == GetPonytailPrompt("full") {
		t.Error("ultra level lost its push-back line")
	}
	if GetPonytailPrompt("lite") == GetPonytailPrompt("full") {
		t.Error("lite level lost its smaller-option line")
	}
}
