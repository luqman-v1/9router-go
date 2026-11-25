package executor

// Zed's completion stream is JSON Lines, not Server-Sent Events.
//
// Each line is one complete JSON object — the provider's native event
// ("message_start", "content_block_delta", …) — and the lines are separated
// by a single "\n". There is no "data:" prefix and no blank-line event
// terminator, so the repository's SSE scanner reads the whole stream as one
// payload and every translation fails on it.
//
// The adapter below rewrites the JSON Lines stream into the SSE framing the
// existing bridges expect, without changing a single byte of the event
// payloads themselves. The provider-native events are then translated by the
// stock Claude and Codex bridges.

import (
	"bufio"
	"bytes"
	"encoding/json/jsontext"
	"io"

	json "encoding/json/v2"
)

// zedJSONLToSSE rewrites a JSON Lines stream as SSE. It also unwraps Zed's
// own envelope: a line may be {"event":<provider chunk>} or
// {"status":<status object>} (upstream unwrapZedLine). Status frames are not
// provider events — a `failed` status becomes an OpenAI error chunk the
// bridges can translate, and `stream_ended` / [DONE] terminate the stream.
func zedJSONLToSSE(r io.Reader) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		scanner := bufio.NewScanner(r)
		buf := make([]byte, 64*1024)
		scanner.Buffer(buf, 10*1024*1024) // tool calls and multimodal payloads run large
		for scanner.Scan() {
			// scanner.Bytes() returns a slice the scanner reuses on the next
			// Scan, so the line is copied before anything is appended to it.
			line := bytes.TrimSpace(bytes.Clone(scanner.Bytes()))
			if len(line) == 0 {
				continue
			}
			if bytes.HasPrefix(line, []byte("data:")) || bytes.HasPrefix(line, []byte("event:")) {
				// Already SSE framing: keep the whole line verbatim and only add
				// the blank-line terminator. Adding `data: ` again produces
				// `data: data: {...}`, which no bridge can parse.
				if !writeRawFrame(pw, line) {
					return
				}
				continue
			}
			if line[0] != '{' && line[0] != '[' {
				continue
			}
			if bytes.Equal(line, []byte("[DONE]")) {
				writeSSEFrame(pw, []byte("[DONE]"))
				continue
			}
			payload := zedUnwrapLine(line)
			if payload == nil {
				continue
			}
			writeSSEFrame(pw, payload)
		}
		_ = pw.CloseWithError(scanner.Err())
	}()
	return pr
}

func writeSSEFrame(w io.Writer, payload []byte) bool {
	frame := make([]byte, 0, len(payload)+8)
	frame = append(frame, "data: "...)
	frame = append(frame, payload...)
	frame = append(frame, '\n', '\n')
	_, err := w.Write(frame)
	return err == nil
}

func writeRawFrame(w io.Writer, line []byte) bool {
	frame := make([]byte, 0, len(line)+2)
	frame = append(frame, line...)
	frame = append(frame, '\n', '\n')
	_, err := w.Write(frame)
	return err == nil
}

// zedUnwrapLine returns the JSON payload to forward for one upstream line.
// Non-event/non-status lines pass through; a failed status becomes an OpenAI
// chat chunk with the error text; a stream-ended status becomes [DONE].
func zedUnwrapLine(line []byte) []byte {
	var obj map[string]jsontext.Value
	if err := json.Unmarshal(line, &obj); err != nil {
		return line
	}
	if raw, ok := obj["event"]; ok {
		return bytes.TrimSpace([]byte(raw))
	}
	statusRaw, ok := obj["status"]
	if !ok {
		return line
	}
	var status map[string]any
	if err := json.Unmarshal([]byte(statusRaw), &status); err != nil {
		// Upstream normalizeStatus accepts a bare string as {type: string}.
		var kind string
		if err := json.Unmarshal([]byte(statusRaw), &kind); err == nil {
			if kind == "stream_ended" {
				return []byte("[DONE]")
			}
			if kind == "failed" {
				return []byte(`{"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"[Zed error] request failed"},"finish_reason":"stop"}]}`)
			}
		}
		return nil
	}
	var kind string
	if typed, ok := status["type"].(string); ok {
		kind = typed
	} else {
		for k, v := range status {
			if m, isMap := v.(map[string]any); isMap {
				kind, status = k, m
				break
			}
		}
	}
	if kind == "failed" || status["failed"] == true || status["failed"] == "true" {
		message := firstZedStatusString(status, "message", "error", "code")
		if message == "" {
			message = "request failed"
		}
		chunk := map[string]any{
			"object": "chat.completion.chunk",
			"choices": []any{map[string]any{
				"index":         0,
				"delta":         map[string]any{"content": "[Zed error] " + message},
				"finish_reason": "stop",
			}},
		}
		if out, err := json.Marshal(chunk); err == nil {
			return out
		}
		return nil
	}
	if kind == "stream_ended" || status["stream_ended"] == true || status["stream_ended"] == "true" {
		return []byte("[DONE]")
	}
	return nil
}

func firstZedStatusString(status map[string]any, keys ...string) string {
	for _, key := range keys {
		if s, ok := status[key].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// zedLooksLikeJSONL reports whether a body is a JSON Lines stream rather than
// an SSE one. A JSON Lines body starts with a JSON object; an SSE body starts
// with "data:" or "event:".
func zedLooksLikeJSONL(body []byte) bool {
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	if len(trimmed) == 0 {
		return false
	}
	if bytes.HasPrefix(trimmed, []byte("data:")) || bytes.HasPrefix(trimmed, []byte("event:")) {
		return false
	}
	return trimmed[0] == '{' || trimmed[0] == '['
}

// readAllFramed re-frames a complete JSON Lines body as SSE.
func readAllFramed(body []byte) []byte {
	framed, err := io.ReadAll(zedJSONLToSSE(bytes.NewReader(body)))
	if err != nil {
		// The adapter only fails when the source reader fails, and a byte
		// slice reader does not. Returning the original body keeps the caller
		// on its own error path instead of losing the payload.
		return body
	}
	return framed
}
