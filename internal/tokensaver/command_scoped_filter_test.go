package tokensaver

import (
	"encoding/json"
	"strings"
	"testing"
)

// A tool result is the output of one command, and the filter set knows which
// command each of its rules was written for: 53 of the 55 filters declare the
// commands they belong to. The command is in the request body — the tool call
// carries it in its arguments — so the filter can be chosen by the command
// that produced the output.
//
// Chosen by content alone, the same rules fire on a file that merely mentions
// what the filter keys on. A 5,638-byte `sed -n 1,45p` result whose file
// happened to mention git came back as the 24-byte string
// "git: already on branch", and the model had no way to tell that the content
// had been replaced.
func TestCommandScopedFilterIsNotChosenByContentAlone(t *testing.T) {
	cfg := DefaultRTKConfig()

	// The output of `sed -n 1,45p notes.txt`, where the file mentions git.
	content := "  build ok\n* item one\n  item two\nAlready on\n" +
		strings.Repeat("some file content line\n", 200)

	body, err := json.Marshal(map[string]any{
		"messages": []any{
			map[string]any{
				"role": "assistant",
				"tool_calls": []any{
					map[string]any{
						"id":   "call_1",
						"type": "function",
						"function": map[string]any{
							"name":      "bash",
							"arguments": `{"command":"sed -n 1,45p notes.txt"}`,
						},
					},
				},
			},
			map[string]any{
				"role":         "tool",
				"tool_call_id": "call_1",
				"content":      content,
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	out, _ := CompressMessagesWithConfig(body, cfg)
	got := string(out)
	if strings.Contains(got, "git: already on branch") {
		t.Errorf("a sed result was replaced by the git-branch summary: %s", truncateForMessage(got))
	}
	if !strings.Contains(got, "some file content line") {
		t.Errorf("the file content was lost: %s", truncateForMessage(got))
	}
}

// The same output, produced by the command the filter was written for, is
// still summarised. The fix is not "compress less"; it is "compress the
// output that belongs to the filter".
func TestCommandScopedFilterStillAppliesToItsOwnCommand(t *testing.T) {
	cfg := DefaultRTKConfig()

	content := "  main\n* feature/rtk\n  release\n" + strings.Repeat("noise line\n", 200)

	body, err := json.Marshal(map[string]any{
		"messages": []any{
			map[string]any{
				"role": "assistant",
				"tool_calls": []any{
					map[string]any{
						"id":   "call_1",
						"type": "function",
						"function": map[string]any{
							"name":      "bash",
							"arguments": `{"command":"git branch"}`,
						},
					},
				},
			},
			map[string]any{
				"role":         "tool",
				"tool_call_id": "call_1",
				"content":      content,
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	out, changed := CompressMessagesWithConfig(body, cfg)
	if !changed {
		t.Fatal("a git branch result must still be compressed")
	}
	got := string(out)
	if strings.Contains(got, "noise line") {
		t.Errorf("the git branch result was not filtered: %s", truncateForMessage(got))
	}
	if !strings.Contains(got, "feature/rtk") {
		t.Errorf("the branch name was lost: %s", truncateForMessage(got))
	}
}

// The command is read from the tool call's arguments, in the shapes the
// three protocols use.
func TestCommandIsResolvedFromEveryToolCallShape(t *testing.T) {
	cases := []struct {
		name string
		item map[string]any
		want string
	}{
		{
			name: "chat completions, arguments as a JSON string",
			item: map[string]any{
				"role": "assistant",
				"tool_calls": []any{
					map[string]any{
						"id":       "call_1",
						"function": map[string]any{"name": "bash", "arguments": `{"command":"git status"}`},
					},
				},
			},
			want: "git status",
		},
		{
			name: "responses, arguments as a JSON string",
			item: map[string]any{
				"type":      "function_call",
				"call_id":   "call_2",
				"name":      "bash",
				"arguments": `{"command":"git diff"}`,
			},
			want: "git diff",
		},
		{
			name: "claude tool_use, input as an object",
			item: map[string]any{
				"role": "assistant",
				"content": []any{
					map[string]any{
						"type":  "tool_use",
						"id":    "call_3",
						"name":  "bash",
						"input": map[string]any{"command": "git log"},
					},
				},
			},
			want: "git log",
		},
		{
			name: "the cmd spelling the translator's sanitiser accepts",
			item: map[string]any{
				"role": "assistant",
				"tool_calls": []any{
					map[string]any{
						"id":       "call_4",
						"function": map[string]any{"name": "bash", "arguments": `{"cmd":"git status"}`},
					},
				},
			},
			want: "git status",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, cmdMap := buildToolCallMap([]map[string]any{tc.item})
			var got string
			for _, v := range cmdMap {
				got = v
			}
			if got != tc.want {
				t.Errorf("command = %q, want %q", got, tc.want)
			}
		})
	}
}

// A tool call that carries no command leaves the command unknown, and the
// content-scoped filters are all that may apply. That is the case the filter
// set is written for.
func TestCommandlessToolCallResolvesNoCommand(t *testing.T) {
	_, cmdMap := buildToolCallMap([]map[string]any{
		{
			"role": "assistant",
			"tool_calls": []any{
				map[string]any{
					"id":       "call_1",
					"function": map[string]any{"name": "read", "arguments": `{"path":"/tmp/x"}`},
				},
			},
		},
	})
	if len(cmdMap) != 0 {
		t.Errorf("cmdMap = %v, want empty", cmdMap)
	}
}

// A banner line ahead of the output does not hide what the output is. Upstream
// anchors every autodetect regex with /m (open-sse/rtk/autodetect.js:18-26)
// precisely so a leading "Here is the diff:" or a blank line does not defeat
// detection; Go compiled the same patterns without it, so `^diff --git ` only
// ever matched at offset zero.
func TestAnchoredPatternsMatchAfterALeadingBanner(t *testing.T) {
	cfg := DefaultRTKConfig()

	var diff strings.Builder
	diff.WriteString("diff --git a/big.go b/big.go\n--- a/big.go\n+++ b/big.go\n@@ -1,60 +1,60 @@\n")
	for i := 0; i < 60; i++ {
		diff.WriteString("-old line content here number 1234567890\n+new line content here number 1234567890\n")
	}

	body := diff.String()
	cases := []struct {
		name string
		text string
	}{
		{"no banner", body},
		{"prose banner", "Here is the diff:\n" + body},
		{"leading blank line", "\n" + body},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := MatchFilter(tc.text, "", cfg)
			if f == nil {
				t.Fatal("no filter matched")
			}
			if f.ID != "git-diff" {
				t.Errorf("filter = %q, want git-diff", f.ID)
			}
		})
	}
}

// Git accepts global options before the subcommand, and an agent that runs
// `git -C /repo diff` produces exactly the output `git diff` produces. The
// command patterns are written against the bare form, so the wrapper has to be
// folded away before matching — otherwise the result falls through to the
// generic filter and loses the diff-specific rules.
func TestGitGlobalOptionsDoNotHideTheSubcommand(t *testing.T) {
	cases := map[string]string{
		"git -C /repo diff":        "git diff",
		"git -C /repo -c a=b diff": "git diff",
		"git --no-pager log":       "git log",
		"git --git-dir=/x status":  "git status",
		"git diff":                 "git diff",
		"ls -la":                   "ls -la",
	}
	for in, want := range cases {
		if got := normalizeCommand(in); got != want {
			t.Errorf("normalizeCommand(%q) = %q, want %q", in, got, want)
		}
	}

	cfg := DefaultRTKConfig()
	var diff strings.Builder
	diff.WriteString("diff --git a/big.go b/big.go\n--- a/big.go\n+++ b/big.go\n@@ -1,60 +1,60 @@\n")
	for i := 0; i < 60; i++ {
		diff.WriteString("-old line content here number 1234567890\n+new line content here number 1234567890\n")
	}
	f := MatchFilter(diff.String(), "git -C /repo diff", cfg)
	if f == nil {
		t.Fatal("a wrapped git diff matched no filter")
	}
	if f.ID != "git-diff" {
		t.Errorf("filter = %q, want git-diff", f.ID)
	}
}

func truncateForMessage(s string) string {
	if len(s) > 400 {
		return s[:400] + "..."
	}
	return s
}
