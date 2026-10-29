package executor

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/translator"
)

// toolArgsStream builds a Chat Completions SSE stream carrying one tool call.
// args is the argument text split across at most n fragments, so the test
// controls both how much arrived and whether it parses. finish appends the
// terminal frames a healthy upstream sends; an empty finish is a stream that
// died. It returns the number of argument fragments it emitted.
func toolArgsStream(args string, n int, finish string) (string, int) {
	var b strings.Builder
	b.WriteString(`data: {"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"exec_command","arguments":""}}]}}]}` + "\n\n")
	sent := 0
	if n > 0 && args != "" {
		size := (len(args) + n - 1) / n
		for start := 0; start < len(args); start += size {
			end := min(start+size, len(args))
			b.WriteString(`data: {"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":` +
				jsonString(args[start:end]) + `}}]}}]}` + "\n\n")
			sent++
		}
	}
	b.WriteString(finish)
	return b.String(), sent
}

func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

const toolFinishFrame = `data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}` + "\n\n"

// TestStreamChatToResponses_TruncatedToolArgumentsReportIncomplete is the
// contract #747 asks for: when the upstream stream is cut mid-argument, the
// terminal event must say the turn is incomplete. Before this, the bridge
// closed every stream with `response.completed` / `status: "completed"`, so
// Codex read a turn whose tool arguments were cut mid-token as a success and
// failed to parse the arguments with no signal that anything had gone wrong.
func TestStreamChatToResponses_TruncatedToolArgumentsReportIncomplete(t *testing.T) {
	// A ~1000-byte argument payload that the upstream cut mid-token: the closing
	// quote and brace never arrive, so the buffer cannot parse. That is the shape
	// a provider that drops the connection mid-arguments leaves (no
	// finish_reason, no [DONE]), and the size the issue reports the full
	// arguments reaching.
	full := `{"cmd":"python3 - <<'PY'\n` + strings.Repeat(`# line: some varied sentence about robots\n`, 20) + `PY"}`
	truncated := full[:len(full)-8]
	upstream, fragments := toolArgsStream(truncated, 130, "")

	rec := httptest.NewRecorder()
	ctx := translator.WithRequestedModel(context.Background(), "gpt-x")

	if err := StreamChatToResponses(ctx, rec, strings.NewReader(upstream), time.Time{}, nil, nil); err != nil {
		t.Fatalf("StreamChatToResponses: %v", err)
	}
	body := rec.Body.String()

	if !strings.Contains(body, "event: response.incomplete") {
		t.Errorf("truncated stream was not closed with response.incomplete:\n%s", tail(body))
	}
	if strings.Contains(body, `"type":"response.completed"`) {
		t.Errorf("truncated stream was reported as a completed turn:\n%s", tail(body))
	}
	if !strings.Contains(body, `"status":"incomplete"`) {
		t.Errorf("terminal response has no incomplete status:\n%s", tail(body))
	}
	if !strings.Contains(body, `"reason":"tool_call_arguments_incomplete"`) {
		t.Errorf("terminal response does not name the reason:\n%s", tail(body))
	}
	// The fragments that did arrive must still reach the client: the client is
	// the only party that can decide what to do with a partial argument.
	if n := countDeltas(body); n != fragments {
		t.Errorf("argument delta events = %d, want %d", n, fragments)
	}
}

// countDeltas counts argument-delta events by their `event:` label. Counting
// the bare substring would double every event, because the type also appears in
// the payload it labels.
func countDeltas(body string) int {
	return strings.Count(body, "event: response.function_call_arguments.delta")
}

// A healthy tool-call turn must still end with response.completed. This is the
// regression guard for the fix above: an argument buffer that parses is not a
// truncation, and reporting it as incomplete would break every working turn.
func TestStreamChatToResponses_CompleteToolArgumentsStayCompleted(t *testing.T) {
	upstream, _ := toolArgsStream(`{"cmd":"ls -la /tmp"}`, 3, toolFinishFrame)

	rec := httptest.NewRecorder()
	ctx := translator.WithRequestedModel(context.Background(), "gpt-x")

	if err := StreamChatToResponses(ctx, rec, strings.NewReader(upstream), time.Time{}, nil, nil); err != nil {
		t.Fatalf("StreamChatToResponses: %v", err)
	}
	body := rec.Body.String()

	if !strings.Contains(body, "event: response.completed") {
		t.Errorf("a complete turn was not closed with response.completed:\n%s", tail(body))
	}
	if strings.Contains(body, "response.incomplete") {
		t.Errorf("a complete turn was reported as incomplete:\n%s", tail(body))
	}
	if !strings.Contains(body, `"arguments":"{\"cmd\":\"ls -la /tmp\"}"`) {
		t.Errorf("the arguments did not survive the bridge:\n%s", tail(body))
	}
}

// tail returns the last few events of a stream, so a failure message shows the
// terminal rather than 40 KB of deltas.
func tail(body string) string {
	const keep = 3
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n\n")
	if len(lines) > keep {
		lines = lines[len(lines)-keep:]
	}
	return strings.Join(lines, "\n\n")
}
