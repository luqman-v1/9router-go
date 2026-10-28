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
	"io"
)

// zedJSONLToSSE rewrites a JSON Lines stream as SSE.
//
// Every non-empty line becomes one "data: <line>\n\n" event. A line that is
// already an SSE "data:" or "event:" line is passed through untouched, so a
// server that switches to real SSE keeps working. A line that is neither is
// dropped rather than forwarded: the translators parse the payload as JSON,
// and a malformed line would abort the whole turn.
func zedJSONLToSSE(r io.Reader) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		scanner := bufio.NewScanner(r)
		buf := make([]byte, 64*1024)
		scanner.Buffer(buf, 10*1024*1024) // tool calls and multimodal payloads run large
		for scanner.Scan() {
			// scanner.Bytes() returns a slice the scanner reuses on the next
			// Scan, so the line is copied before anything is appended to it:
			// writing into that buffer would corrupt the frame just sent.
			line := bytes.TrimSpace(bytes.Clone(scanner.Bytes()))
			if len(line) == 0 {
				continue
			}
			frame := make([]byte, 0, len(line)+8)
			if bytes.HasPrefix(line, []byte("data:")) || bytes.HasPrefix(line, []byte("event:")) {
				// Already SSE framing: keep the event, and terminate it.
				frame = append(frame, line...)
			} else {
				if line[0] != '{' && line[0] != '[' {
					continue
				}
				frame = append(frame, "data: "...)
				frame = append(frame, line...)
			}
			frame = append(frame, '\n', '\n')
			if _, err := pw.Write(frame); err != nil {
				return
			}
		}
		_ = pw.CloseWithError(scanner.Err())
	}()
	return pr
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
