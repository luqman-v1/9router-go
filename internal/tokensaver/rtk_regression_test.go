package tokensaver

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// reTruncationMarker matches the count a truncation marker reports:
// "... (520 lines truncated by generic-output)".
var reTruncationMarker = regexp.MustCompile(`\((\d+) lines truncated`)

// rtkBodyLineCount counts the lines of a body, without counting the empty
// string a trailing newline leaves behind.
func rtkBodyLineCount(body string) int {
	if body == "" {
		return 0
	}
	return len(strings.Split(strings.TrimSuffix(body, "\n"), "\n"))
}

// rtkToolResult drives the public request path — CompressMessagesWithConfig,
// the function the chat handler calls — with an OpenAI-shaped body: an
// assistant tool call carrying the command, and the tool result it produced.
//
// The command matters: it is what a filter is chosen by, so a test that does
// not carry one exercises the content-scoped path only.
func rtkToolResult(t *testing.T, command string, content string) string {
	t.Helper()
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
							"arguments": fmt.Sprintf(`{"command":%q}`, command),
						},
					},
				},
			},
			map[string]any{
				"role":         "tool",
				"tool_call_id": "call_1",
				"name":         "bash",
				"content":      content,
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	out, _ := CompressMessagesWithConfig(body, DefaultRTKConfig())
	return rtkToolContent(t, string(out))
}

// rtkToolContent extracts the tool result's text from a compressed body.
func rtkToolContent(t *testing.T, body string) string {
	t.Helper()
	var decoded struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	for _, m := range decoded.Messages {
		if m.Role == "tool" {
			return m.Content
		}
	}
	t.Fatal("no tool message in the compressed body")
	return ""
}

// rtkPackageJSON builds a package.json of the requested size, with the
// "engines" object last so it carries no trailing comma — the line that a
// filter keeping only named keys would drop.
func rtkPackageJSON(entries int) string {
	var b strings.Builder
	b.WriteString("{\n")
	b.WriteString("  \"name\": \"demo-service\",\n")
	b.WriteString("  \"dependencies\": {\n")
	for i := range entries {
		sep := ","
		if i == entries-1 {
			sep = ""
		}
		fmt.Fprintf(&b, "    \"package%02d\": \"^1.0.0\"%s\n", i, sep)
	}
	b.WriteString("  },\n")
	b.WriteString("  \"engines\": {\n    \"node\": \">=20.0.0\"\n  }\n}\n")
	return b.String()
}

// rtkLogBody builds a log body of the requested length with distinct lines,
// so the deduplication step does not collapse them before the truncation.
//
// The line at index `at` is the diagnostic one.
func rtkLogBody(lines int, at int, diagnostic string) string {
	var b strings.Builder
	for i := range lines {
		if i == at {
			b.WriteString(diagnostic + "\n")
			continue
		}
		fmt.Fprintf(&b, "service %d healthy\n", i)
	}
	return b.String()
}

// A JSON document must survive RTK byte for byte, whichever command produced
// it.
//
// Every filter reduces its input line by line, so applying one to structured
// output deletes the lines no includePattern matches and leaves unparseable
// JSON behind. The json-output filter is the one that reaches a JSON body:
// its command list names `jq` and `cat *.json`, and its includePatterns keep
// the structural lines and a few named keys.
//
// The guard that prevents this has to run before every selection path. When
// the command matching ran first, `cat package.json` selected json-output by
// command and the guard — which sat after it — never ran.
func TestRTK_NeverCorruptsStructuredOutputSelectedByCommand(t *testing.T) {
	commands := []string{"cat package.json", "jq . package.json", "cat ./config.json"}
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			content := rtkPackageJSON(30)
			got := rtkToolResult(t, command, content)

			if got != content {
				t.Fatalf("a JSON document was rewritten.\ncommand: %s\ninput:\n%s\noutput:\n%s",
					command, content, got)
			}
			if !json.Valid([]byte(got)) {
				t.Fatalf("the tool result no longer parses as JSON:\n%s", got)
			}
		})
	}
}

// The same guard, on the path that has no command at all: the content-scoped
// filters still refuse a JSON body.
func TestRTK_NeverCorruptsStructuredOutputWithoutACommand(t *testing.T) {
	content := rtkPackageJSON(30)
	got := CompressTextDetailed(content, DefaultRTKConfig()).Text
	if got != content {
		t.Fatalf("a JSON document was rewritten without a command.\ninput:\n%s\noutput:\n%s", content, got)
	}
}

// A diagnostic line must survive a truncation.
//
// The middle of a body is where a single failure usually sits — the run
// looked fine for two hundred lines and then broke once — and a head/tail
// truncation drops exactly that line. The agent then reads a body with no
// error in it.
func TestRTK_TruncationKeepsTheDiagnosticLine(t *testing.T) {
	content := rtkLogBody(401, 200, "ERROR failed to connect to database")

	got := rtkToolResult(t, "docker compose up", content)
	if !strings.Contains(got, "ERROR failed to connect to database") {
		t.Fatalf("the error line was truncated away; the model sees a body with no error in it.\noutput:\n%s",
			truncateForMessage(got))
	}
}

// The diagnostics that survive a truncation are the ones the severity
// vocabulary names. A line that is not one is still dropped, so the
// truncation keeps doing its job.
func TestRTK_TruncationKeepsOnlyTheDiagnostics(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		content  string
		mustKeep string
		mustDrop string
	}{
		{
			name:     "the severity vocabulary",
			command:  "docker compose up",
			content:  rtkLogBody(401, 200, "ERROR database unreachable"),
			mustKeep: "ERROR database unreachable",
		},
		{
			name:     "a line that is not a diagnostic is still dropped",
			command:  "docker compose up",
			content:  rtkLogBody(401, 200, "DEBUG tracing span 42"),
			mustDrop: "DEBUG tracing span 42",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rtkToolResult(t, tt.command, tt.content)
			if tt.mustKeep != "" && !strings.Contains(got, tt.mustKeep) {
				t.Fatalf("the diagnostic line was dropped.\noutput:\n%s", truncateForMessage(got))
			}
			if tt.mustDrop != "" && strings.Contains(got, tt.mustDrop) {
				t.Fatalf("a line that is not a diagnostic was kept, so the truncation does not shrink the body.\noutput:\n%s",
					truncateForMessage(got))
			}
		})
	}
}

// The preserved lines must not make the body grow: a body in which every line
// is a diagnostic is bounded by the cap, and the marker still reports how many
// lines were really dropped.
func TestRTK_TruncationStaysBounded(t *testing.T) {
	var b strings.Builder
	for i := range 400 {
		fmt.Fprintf(&b, "ERROR service %d failed\n", i)
	}
	content := b.String()

	got := rtkToolResult(t, "docker compose up", content)
	if len(got) >= len(content) {
		t.Fatalf("the truncation did not shrink the body: %d bytes in, %d bytes out", len(content), len(got))
	}
	if !strings.Contains(got, "lines truncated") {
		t.Fatalf("the truncation marker is missing:\n%s", truncateForMessage(got))
	}
}

// The marker reports the lines that were really dropped, so a reader can tell
// how much of the body it is not seeing.
//
// The count is compared against the body itself — the input's lines minus the
// ones the result still carries — rather than against a fixed number, so the
// assertion measures what the marker says about the body and stays true when
// the head/tail split or the body size changes.
func TestRTK_TruncationMarkerCountsTheDroppedLines(t *testing.T) {
	content := rtkLogBody(601, 300, "ERROR database unreachable")

	got := rtkToolResult(t, "docker compose up", content)
	match := reTruncationMarker.FindStringSubmatch(got)
	if match == nil {
		t.Fatalf("the truncation marker is missing:\n%s", truncateForMessage(got))
	}
	claimed, err := strconv.Atoi(match[1])
	if err != nil {
		t.Fatalf("the marker count %q does not parse: %v", match[1], err)
	}
	// The marker line itself is not one of the body's lines.
	dropped := rtkBodyLineCount(content) - (rtkBodyLineCount(got) - 1)
	if claimed != dropped {
		t.Fatalf("the marker claims %d dropped lines, the body is missing %d:\n%s",
			claimed, dropped, truncateForMessage(got))
	}
	// The error line is kept, so the marker must not claim it was dropped.
	if !strings.Contains(got, "ERROR database unreachable") {
		t.Fatalf("the error line was truncated away; the marker counts it as dropped:\n%s",
			truncateForMessage(got))
	}
}
