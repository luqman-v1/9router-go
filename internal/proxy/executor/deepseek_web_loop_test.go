package executor

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"

	json "encoding/json/v2"
	"testing"
)

// The agent loop: without the call rendered into the transcript, the replay
// shows a tool result whose cause is invisible, and the model concludes it never
// called anything and calls again on every turn.
func TestDeepSeekPromptRendersPriorToolCall(t *testing.T) {
	body := `{"model":"deepseek-v4-pro","messages":[
	  {"role":"user","content":"weather in Jakarta?"},
	  {"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function",
	    "function":{"name":"get_weather","arguments":"{\"city\": \"Jakarta\"}"}}]},
	  {"role":"tool","tool_call_id":"call_1","name":"get_weather","content":"Sunny, 31C"}
	]}`

	var env deepSeekRequestEnvelope
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("parse: %v", err)
	}
	prompt := deepSeekMessagesToPrompt(env.Messages, 0, false)

	if !strings.Contains(prompt, "get_weather") {
		t.Fatalf("prompt drops the tool call entirely:\n%s", prompt)
	}
	if !strings.Contains(prompt, "[called get_weather") {
		t.Errorf("prompt does not record that the call was already made:\n%s", prompt)
	}
	// The result must come after the call, or the transcript reads backwards.
	call := strings.Index(prompt, "[called get_weather")
	result := strings.Index(prompt, "Sunny, 31C")
	if call < 0 || result < 0 || call > result {
		t.Errorf("call at %d must precede result at %d:\n%s", call, result, prompt)
	}
}

// The tool instructions have to tell the model the call is already made,
// because the transcript alone does not stop a model that reads it as context
// rather than as history.
func TestDeepSeekToolInstructionsForbidRepeating(t *testing.T) {
	var tool deepSeekToolSpec
	tool.Function.Name = "get_weather"
	tool.Function.Description = "weather"

	out := deepSeekRenderTools([]deepSeekToolSpec{tool})
	for _, want := range []string{"Do not repeat it", "Calling a tool again", "already"} {
		if !strings.Contains(out, want) {
			t.Errorf("tool instructions missing %q:\n%s", want, out)
		}
	}
}

// Prose written around the object must reach the client: dropping it produced
// content:null, which an agent reads as an empty stop and retries.
func TestDeepSeekToolCallKeepsSurroundingText(t *testing.T) {
	tools := []deepSeekToolSpec{func() deepSeekToolSpec {
		var t deepSeekToolSpec
		t.Function.Name = "get_weather"
		return t
	}()}

	answer := `Let me check that for you.` + "\n" +
		`{"name":"get_weather","arguments":{"city":"Jakarta"}}`

	call, ok := extractOne(answer, tools)
	if !ok {
		t.Fatal("a call with prose around it must still be recognised")
	}
	if got := deepSeekSurroundingText(answer, call.Raw); got != "Let me check that for you." {
		t.Errorf("surrounding text = %q, want the prose the model wrote", got)
	}
}

// A bare object with nothing around it yields no preamble, so the client still
// gets content:null — which is correct, because there was nothing else said.
func TestDeepSeekToolCallWithoutProseHasNoPreamble(t *testing.T) {
	tools := []deepSeekToolSpec{func() deepSeekToolSpec {
		var t deepSeekToolSpec
		t.Function.Name = "get_weather"
		return t
	}()}

	answer := `{"name":"get_weather","arguments":{"city":"Jakarta"}}`
	call, ok := extractOne(answer, tools)
	if !ok {
		t.Fatal("call not recognised")
	}
	if got := deepSeekSurroundingText(answer, call.Raw); got != "" {
		t.Errorf("preamble = %q, want empty when the model said nothing else", got)
	}
}

// An undeclared tool name must never be forwarded: the client would execute a
// call it never authorised.
func TestDeepSeekUndeclaredToolIsRejected(t *testing.T) {
	var declared deepSeekToolSpec
	declared.Function.Name = "get_weather"
	declared.Function.Description = "weather"

	answer := `{"name":"delete_everything","arguments":{"path":"/"}}`
	if _, ok := extractOne(answer, []deepSeekToolSpec{declared}); ok {
		t.Error("a tool the client never declared must not be returned as a call")
	}
}

// A tool turn the upstream cut off must answer with a status, not by writing
// through a writer that does not exist. The buffered tool path had no writer
// yet — it is still able to answer — and dereferencing nil there panicked and
// took the process down, so the client saw a request that never finished.
func TestDeepSeekToolTurnAbortsWithoutPanic(t *testing.T) {
	var env deepSeekRequestEnvelope
	env.Model = "deepseek-v4-pro"
	env.Messages = []deepSeekMessage{{Role: "user", Content: jsontext.Value("weather?")}}

	var spec deepSeekToolSpec
	spec.Function.Name = "get_weather"
	env.Tools = []deepSeekToolSpec{spec}

	req := &Request{IsStream: true}
	rec := httptest.NewRecorder()

	// The upstream opens and sends fragments, then signs off with nothing: a
	// session cut mid-turn.
	upstream := strings.NewReader(
		`{"v":{"response":{"fragments":[{"type":"RESPONSE","content":"Let me ch` +
			`eck","status":1}]}}}` + "\n" +
			`{"p":"response/fragments/-1/content","o":"APPEND","v":"eck that"}` + "\n")

	err := deepSeekToolTurnToClient(rec, req, upstream, env, newDeepSeekStreamAccumulator(env.Model))
	if err == nil {
		t.Fatal("a cut-off tool turn must return an error, not a completion")
	}
	if !strings.Contains(err.Error(), "cut off") {
		t.Errorf("error = %v, want it to name the cut-off session", err)
	}

	// The failure must travel as a status, not as SSE bytes. A half-written SSE
	// frame here would be read by a streaming client as a normal turn that simply
	// produced nothing.
	if body := rec.Body.String(); strings.Contains(body, "data:") {
		t.Errorf("abort wrote SSE bytes to a client that expects a status: %q", body)
	}
}

// A client that asked for a stream must get SSE frames. The buffered tool path
// used to write one chat.completion document whatever was requested, so a
// stream:true client received no `data:` frames at all and waited for a
// terminator that never came — a request that hangs from the client's side.
func TestDeepSeekToolTurnStreamsWhenAsked(t *testing.T) {
	tests := []struct {
		name    string
		answer  string
		tool    bool
		wantFin string
	}{
		{name: "plain answer", answer: "Hi there!", wantFin: "stop"},
		{name: "tool call", tool: true, wantFin: "tool_calls"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var spec deepSeekToolSpec
			spec.Function.Name = "get_weather"

			var env deepSeekRequestEnvelope
			env.Model = "deepseek-v4-pro"
			env.Messages = []deepSeekMessage{{Role: "user", Content: jsontext.Value("hi")}}
			env.Tools = []deepSeekToolSpec{spec}

			answer := tt.answer
			if tt.tool {
				answer = `{"name":"get_weather","arguments":{"city":"Jakarta"}}`
			}

			upstream := newSignedOffTurn(t, answer)
			req := &Request{IsStream: true, Ctx: context.Background()}
			rec := httptest.NewRecorder()

			if err := deepSeekToolTurnToClient(rec, req, upstream, env, newDeepSeekStreamAccumulator(env.Model)); err != nil {
				t.Fatalf("write: %v", err)
			}

			body := rec.Body.String()
			if !strings.Contains(body, "data: ") {
				t.Fatalf("stream:true client got no SSE frames:\n%s", body)
			}
			if !strings.Contains(body, "[DONE]") {
				t.Errorf("stream is not terminated:\n%s", body)
			}
			if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "text/event-stream") {
				t.Errorf("Content-Type = %q, want text/event-stream", got)
			}
			if !strings.Contains(body, `"object":"chat.completion.chunk"`) {
				t.Errorf("streaming client got a non-chunk document:\n%s", body)
			}
			if !strings.Contains(body, `"finish_reason":"`+tt.wantFin+`"`) {
				t.Errorf("finish_reason is not %q:\n%s", tt.wantFin, body)
			}
		})
	}
}

// The same turn requested as non-streaming must stay a single document: the fix
// must not change the shape for clients that did not ask for SSE.
func TestDeepSeekToolTurnNonStreamingStaysJSON(t *testing.T) {
	var spec deepSeekToolSpec
	spec.Function.Name = "get_weather"

	var env deepSeekRequestEnvelope
	env.Model = "deepseek-v4-pro"
	env.Messages = []deepSeekMessage{{Role: "user", Content: jsontext.Value("hi")}}
	env.Tools = []deepSeekToolSpec{spec}

	req := &Request{IsStream: false, Ctx: context.Background()}
	rec := httptest.NewRecorder()

	if err := deepSeekToolTurnToClient(rec, req, newSignedOffTurn(t, "Hi there!"), env, newDeepSeekStreamAccumulator(env.Model)); err != nil {
		t.Fatalf("write: %v", err)
	}

	body := rec.Body.String()
	if strings.Contains(body, "data: ") {
		t.Errorf("a non-streaming request must not get SSE frames:\n%s", body)
	}
	if !strings.Contains(body, `"object":"chat.completion"`) {
		t.Errorf("not a chat.completion document:\n%s", body)
	}
}

// newSignedOffTurn builds an upstream body that carries an answer and signs it
// off, so the tool path reads it as a complete turn. The framing is the real one:
// SSE `data:` lines terminated by a blank line, which is what the live site sends
// and what the scanner is written against.
func newSignedOffTurn(t *testing.T, answer string) io.Reader {
	t.Helper()
	events := []string{
		fmt.Sprintf(`{"v":{"response":{"fragments":[{"type":"RESPONSE","content":%q,"status":1}]}}}`,
			answer),
		`{"p":"response/status","o":"SET","v":"FINISHED"}`,
	}
	return strings.NewReader("data: " + strings.Join(events, "\n\ndata: ") + "\n\n")
}

// deepSeekReportStreamAbort is shared by the streaming path, where 200 is
// already on the wire and the error has to travel in-band. A nil writer there
// means "nothing written yet" and must be a no-op, not a crash.
func TestDeepSeekReportStreamAbortNilWriterIsNoop(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("abort with no writer panicked: %v", r)
		}
	}()

	err := deepSeekReportStreamAbort(nil, nil, errors.New("upstream session was cut off"))
	if err == nil {
		t.Error("abort must still report the cause to the caller")
	}
}

// A transport error mid-body is the other way the buffered path can fail
// before anything has been written.
func TestDeepSeekAccumulateReportsTransportError(t *testing.T) {
	upstream := io.MultiReader(strings.NewReader(`{"v":{"response":{}}}`), errReader{})

	accumulator, err := deepSeekAccumulate(upstream)
	if err == nil {
		t.Fatal("a body that fails mid-read must be an error")
	}
	if accumulator != nil {
		t.Error("a failed accumulate must not hand back a partial accumulator")
	}
}

// errReader fails every read, standing in for a connection dropped by the
// upstream before the turn was signed off.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }
