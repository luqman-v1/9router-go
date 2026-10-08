package proxy

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"strings"

	"9router/proxy/internal/handlerutil"
)

// A 200 response is not proof of an answer. Upstreams return 200 with an
// empty body, an HTML error page, or a `{"error": ...}` envelope whenever a
// gateway or WAF in front of them fails, and the router used to read all
// three as a completed turn: no fallback, and — worse — a success that
// cleared the account's cooldown. These helpers turn "technically successful,
// actually empty" into the 502 the fallback layer already knows how to act
// on (providers.RetryableStatusCodes), so combo routing moves on to the next
// model instead of sticking to the one that just said nothing.

// NoCompletionInStream is the one wording for the failure every event-stream
// fold shares: the stream carried no completion chunk, so there is no answer
// to serve. It names neither provider nor model — those are already fields on
// the failure row and on the `fallback` log line, and a model name spelled
// into the message only goes stale when the model id changes. One constant so
// four call sites cannot drift into four different sentences.
const NoCompletionInStream = "upstream event stream carried no completion chunk"

// UpstreamFailure builds the error an unusable 200 body is reported as.
// Pass a status that providers.RetryableStatusCodes lists (502) so the
// fallback layer both retries the next connection and records the lock.
func UpstreamFailure(code int, message string) error {
	body, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "upstream_error",
			"code":    code,
		},
	})
	if err != nil {
		body = []byte(`{"error":{"message":"upstream returned an unusable body","type":"upstream_error","code":502}}`)
	}
	return &UpstreamError{StatusCode: code, Body: body}
}

// LooksLikeSSE reports whether a body is an event stream rather than a single
// JSON document. Only the leading field name decides: a JSON document starts
// with `{`, so requiring an SSE prefix there cannot misfire on one.
func LooksLikeSSE(body []byte) bool {
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	return bytes.HasPrefix(trimmed, []byte("data:")) || bytes.HasPrefix(trimmed, []byte("event:"))
}

// EmptyUpstreamError reports whether a body that arrived with HTTP 200
// carries a completion the client can read. It returns nil for a body that
// does, and an *UpstreamError for an empty body, a non-JSON body, a 200
// error envelope, or a completion whose content is blank.
//
// Callers must fold an event stream into a single document first; a stream
// that is still SSE here is left to the caller, which is the only place that
// knows how to aggregate it.
func EmptyUpstreamError(body []byte) error {
	trimmed := bytes.TrimSpace(body)
	switch {
	case len(trimmed) == 0:
		return UpstreamFailure(http.StatusBadGateway, "upstream returned an empty body with status 200")
	case LooksLikeSSE(trimmed):
		return nil
	case trimmed[0] != '{':
		return UpstreamFailure(http.StatusBadGateway,
			fmt.Sprintf("upstream returned %s instead of a completion with status 200", describeBodyKind(trimmed)))
	}

	var probe completionProbe
	if err := json.Unmarshal(trimmed, &probe, handlerutil.UpstreamBody); err != nil {
		return UpstreamFailure(http.StatusBadGateway, "upstream returned an unparseable body with status 200")
	}
	if msg := errorEnvelopeMessage(probe.Error); msg != "" {
		return UpstreamFailure(http.StatusBadGateway, "upstream answered 200 with an error: "+msg)
	}
	if reason := missingCompletion(probe); reason != "" {
		return UpstreamFailure(http.StatusBadGateway, "upstream answered 200 without a completion: "+reason)
	}
	return nil
}

// completionProbe reads only the envelope fields that decide whether a
// response carries an answer. The gateway serves three wire shapes — Chat
// Completions, Claude Messages and Responses — and each carries its content
// under a different key.
type completionProbe struct {
	Error   jsontext.Value `json:"error"`
	Choices jsontext.Value `json:"choices"`
	Content jsontext.Value `json:"content"`
	Output  jsontext.Value `json:"output"`
	Status  string         `json:"status"`
}

type choiceProbe struct {
	Message messageProbe `json:"message"`
	Delta   messageProbe `json:"delta"`
	Text    string       `json:"text"`
}

type messageProbe struct {
	Content          jsontext.Value `json:"content"`
	ToolCalls        jsontext.Value `json:"tool_calls"`
	FunctionCall     jsontext.Value `json:"function_call"`
	ReasoningContent jsontext.Value `json:"reasoning_content"`
	Reasoning        jsontext.Value `json:"reasoning"`
	Refusal          jsontext.Value `json:"refusal"`
}

// carriesAnswer reports whether a message holds anything a client can act
// on. A blank content string is a real answer when the model also returned
// tool calls or reasoning — the text simply is not the whole reply — so
// those carriers count.
func (m messageProbe) carriesAnswer() bool {
	return carriesValue(m.Content) || carriesValue(m.ToolCalls) || carriesValue(m.FunctionCall) ||
		carriesValue(m.ReasoningContent) || carriesValue(m.Reasoning) || carriesValue(m.Refusal)
}

// missingCompletion names the specific gap in a 200 body, or "" when it
// carries a completion.
func missingCompletion(p completionProbe) string {
	switch {
	case p.Choices != nil:
		var choices []choiceProbe
		if err := json.Unmarshal(p.Choices, &choices, handlerutil.UpstreamBody); err != nil {
			return "" // a shape this probe does not model: relay, do not guess
		}
		if len(choices) == 0 {
			return "choices is empty"
		}
		if choices[0].Text != "" {
			return "" // legacy completions carry the answer under text
		}
		if choices[0].Message.carriesAnswer() || choices[0].Delta.carriesAnswer() {
			return ""
		}
		return "the first choice carries no content, tool call or reasoning"
	case p.Content != nil:
		if carriesValue(p.Content) {
			return ""
		}
		return "content is empty"
	case p.Output != nil:
		var output []jsontext.Value
		if err := json.Unmarshal(p.Output, &output, handlerutil.UpstreamBody); err != nil {
			return ""
		}
		if len(output) == 0 && !terminalOutputStatus(p.Status) {
			return "output is empty"
		}
		return ""
	}
	return "no choices, content or output"
}

// terminalOutputStatus reports a Responses status that ends the turn on its
// own, so an empty output array under it is the answer rather than a gap.
func terminalOutputStatus(status string) bool {
	switch status {
	case "incomplete", "failed", "cancelled", "cancelling":
		return true
	}
	return false
}

// carriesValue reports whether a raw JSON value holds content rather than
// null, an empty string, or an empty array/object.
func carriesValue(raw jsontext.Value) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return false
	}
	switch trimmed[0] {
	case '"':
		var s string
		if err := json.Unmarshal(trimmed, &s, handlerutil.UpstreamBody); err != nil {
			return false
		}
		return strings.TrimSpace(s) != ""
	case '[':
		var items []jsontext.Value
		if err := json.Unmarshal(trimmed, &items, handlerutil.UpstreamBody); err != nil {
			return false
		}
		return len(items) > 0
	case '{':
		var fields map[string]jsontext.Value
		if err := json.Unmarshal(trimmed, &fields, handlerutil.UpstreamBody); err != nil {
			return false
		}
		return len(fields) > 0
	}
	return true
}

// errorEnvelopeMessage returns a human-readable message from an `error`
// field, or "" when there is no error to report. A JSON null is not an
// error, so it reads as absent.
func errorEnvelopeMessage(raw jsontext.Value) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	var envelope struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	}
	if err := json.Unmarshal(trimmed, &envelope, handlerutil.UpstreamBody); err == nil {
		switch {
		case envelope.Message != "":
			return envelope.Message
		case envelope.Type != "":
			return envelope.Type
		}
		return "error object"
	}
	var message string
	if err := json.Unmarshal(trimmed, &message, handlerutil.UpstreamBody); err == nil && message != "" {
		return message
	}
	return "error object"
}

// describeBodyKind names a non-JSON body for the log, since "non-JSON" and
// "HTML error page" point at different faults.
func describeBodyKind(trimmed []byte) string {
	head := trimmed[:min(len(trimmed), 512)]
	lower := bytes.ToLower(head)
	if bytes.Contains(lower, []byte("<html")) || bytes.Contains(lower, []byte("<!doctype")) {
		return "an HTML error page"
	}
	return "a non-JSON body"
}
