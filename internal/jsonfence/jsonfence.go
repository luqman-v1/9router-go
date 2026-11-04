// Package jsonfence unwraps a ```json code fence from a model answer when the
// client asked for machine-readable JSON.
//
// A model asked for JSON often answers with a fenced block anyway, because
// that is how the same text is formatted for a human. The fence makes the body
// unparseable for a client that expected `{...}`, so the wrapper is removed
// before the answer reaches the client.
//
// Two conditions guard the strip, and both are load-bearing:
//
//   - The request must have asked for JSON. A text request keeps the fence the
//     model produced: stripping it there would corrupt prose that opens with a
//     code fence.
//   - The fenced contents must parse as JSON. A body whose contents do not
//     parse is left exactly as the model wrote it, so a prose answer that
//     happens to start with a fence survives verbatim.
//
// An unclosed fence is left alone for the same reason: what follows it is a
// half-open JSON string the client could not parse either way.
package jsonfence

import (
	"context"
	"encoding/json"
	"strings"
)

type ctxKey struct{}

// With returns a child context recording that the client asked for JSON.
func With(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxKey{}, true)
}

// Requested reports whether the client asked for a JSON answer.
func Requested(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(ctxKey{}).(bool)
	return v
}

// Apply unwraps a fenced body when the request asked for JSON, and is a no-op
// for every request that did not.
func Apply(ctx context.Context, body []byte) []byte {
	if !Requested(ctx) || len(body) == 0 {
		return body
	}
	return Strip(body)
}

// ApplyContents unwraps fenced text inside a chat completion's message
// contents, in addition to the whole body.
//
// The whole-body case is the exception: a provider that returns the fenced
// document as the entire response. The common case is a model that wraps its
// answer in a fence while the envelope stays well-formed, so a client that
// asked for JSON receives `{"city":...}` in `content` rather than a fenced
// block it has to unwrap itself.
//
// Only a content whose fenced text parses as JSON is unwrapped; anything else
// is left exactly as the model wrote it. A body that is not a chat completion
// is returned unchanged.
func ApplyContents(ctx context.Context, body []byte) []byte {
	if !Requested(ctx) || len(body) == 0 {
		return body
	}
	body = Strip(body)
	var probe struct {
		Choices []struct {
			Message *struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &probe); err != nil || len(probe.Choices) == 0 {
		return body
	}
	// Re-marshal through a map so fields the probe does not model — id, usage,
	// finish_reason — survive: round-tripping the probe struct alone would
	// drop them from the answer.
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return body
	}
	changed := false
	for i := range probe.Choices {
		msg := probe.Choices[i].Message
		if msg == nil || len(msg.Content) == 0 {
			continue
		}
		var text string
		if err := json.Unmarshal(msg.Content, &text); err != nil {
			continue
		}
		stripped := Strip([]byte(text))
		if string(stripped) == text {
			continue
		}
		replaced, err := json.Marshal(string(stripped))
		if err != nil {
			continue
		}
		msg.Content = replaced
		changed = true
	}
	if !changed {
		return body
	}
	// Write the rewritten contents back into the untouched top-level map.
	var choices []json.RawMessage
	if err := json.Unmarshal(top["choices"], &choices); err != nil || len(choices) != len(probe.Choices) {
		return body
	}
	for i := range probe.Choices {
		msg := probe.Choices[i].Message
		if msg == nil || len(msg.Content) == 0 {
			continue
		}
		var choice map[string]json.RawMessage
		if err := json.Unmarshal(choices[i], &choice); err != nil {
			continue
		}
		inner, ok := choice["message"]
		if !ok {
			continue
		}
		var message map[string]json.RawMessage
		if err := json.Unmarshal(inner, &message); err != nil {
			continue
		}
		message["content"] = msg.Content
		newMessage, err := json.Marshal(message)
		if err != nil {
			continue
		}
		choice["message"] = newMessage
		newChoice, err := json.Marshal(choice)
		if err != nil {
			continue
		}
		choices[i] = newChoice
	}
	newChoices, err := json.Marshal(choices)
	if err != nil {
		return body
	}
	top["choices"] = newChoices
	out, err := json.Marshal(top)
	if err != nil {
		return body
	}
	return out
}

// lastBacktickRun returns the start index and length of the last run of
// backticks in s that is at least minLen long, or (-1, 0) when there is none.
//
// A closing delimiter must be at least as long as the opening one: a block
// opened with four backticks is closed by four, and a three-backtick run
// inside it is content, not the end of the block.
func lastBacktickRun(s string, minLen int) (int, int) {
	end, length := -1, 0
	for i := 0; i < len(s); {
		if s[i] != '`' {
			i++
			continue
		}
		start := i
		for i < len(s) && s[i] == '`' {
			i++
		}
		if run := i - start; run >= minLen {
			end, length = start, run
		}
	}
	return end, length
}

// Strip removes a ```json / ``` code fence wrapping an otherwise valid JSON
// document. Anything that is not a fenced JSON document is returned unchanged.
func Strip(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	s := strings.TrimSpace(string(body))
	// The opening run of backticks, measured rather than assumed to be three: a
	// model wrapping a block that itself contains a fence opens with four or
	// more, and hardcoding three leaves that answer fenced for a client that
	// asked for JSON. A shorter run is not a fence at all.
	fenceLen := 0
	for fenceLen < len(s) && s[fenceLen] == '`' {
		fenceLen++
	}
	if fenceLen < 3 {
		// Not a fence at all. A mid-text fence is left as the model wrote it:
		// unwrapping it would drop the prose around it.
		return body
	}
	// The opening line is the fence plus an optional language tag.
	rest := s[fenceLen:]
	if i := strings.IndexByte(rest, '\n'); i >= 0 {
		rest = rest[i+1:]
	} else {
		return body
	}
	// The closing fence is the last ``` in the remainder. Anything after it
	// other than whitespace means the answer is not a fenced document — a
	// fenced block followed by prose — so the whole body is left as written:
	// unwrapping it would drop that prose.
	tail := strings.TrimRight(rest, "\r\n \t")
	end, closeLen := lastBacktickRun(tail, fenceLen)
	if end < 0 {
		// Unclosed fence: the client would receive a half-open JSON string
		// either way, so the answer is left exactly as produced.
		return body
	}
	if strings.TrimSpace(tail[end+closeLen:]) != "" {
		return body
	}
	inner := strings.TrimSpace(rest[:end])
	if inner == "" || !json.Valid([]byte(inner)) {
		return body
	}
	return []byte(inner)
}

// Wants reports whether a request body asked for a structured answer, which is
// the only case where stripping a code fence is safe.
//
// response_format is read from the raw body rather than a typed field so an
// unknown or future shape does not fail the request: a body carrying
// `"response_format":{"type":"json_schema","json_schema":{...}}` and one
// carrying `"response_format":"json_object"` are both honoured, and a
// malformed one is simply not.
func Wants(body []byte) bool {
	var probe struct {
		ResponseFormat json.RawMessage `json:"response_format"`
	}
	if err := json.Unmarshal(body, &probe); err != nil || len(probe.ResponseFormat) == 0 {
		return false
	}
	trimmed := strings.TrimSpace(string(probe.ResponseFormat))
	if trimmed == "" || trimmed == "null" {
		return false
	}
	// The string form: "json_object" asks for JSON, "text" (the default) does
	// not, and any other vendor string is left alone.
	if trimmed[0] != '{' && trimmed[0] != '[' {
		return strings.Contains(trimmed, "json")
	}
	var typed struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(probe.ResponseFormat, &typed); err != nil {
		return false
	}
	return typed.Type == "json_object" || typed.Type == "json_schema"
}
