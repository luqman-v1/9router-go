### fix(combo): answer a failed streaming combo with parseable SSE frames

A combo whose every member fails answers the client through
`passRetry.writeError`, which wrote the terminal error as a bare JSON
document regardless of what the client asked for. A client that sent
`stream: true` therefore received a JSON body where it expected an event
stream: no `data: ` prefix, no `[DONE]`, and no newline it could split
events on. An SSE reader parsing that body reports the first backslash it
meets as `JSON Parse error: Unrecognized token '\'` and never sees the
status, type or code — the real failure is invisible.

This is not a double-escape in the frame builder. `BuildStreamErrorBytes`
and every executor path already emit real 0x0A bytes; the defect is that
the pre-commit combo error path never framed its body as SSE at all.

`writeError` now frames the same JSON body as `data: <json>` + two real
newline bytes, followed by `data: [DONE]`, when the client asked for a
stream and no byte has been written yet. The non-streaming path is
untouched, as are combo routing, model locking and backoff.

- `committedResponseWriter` gains a `stream` flag recording what the
  client asked for, set by both combo loops from their own `isStream`.
- `writeError` branches on it before any other work, so a stream client
  gets an event stream and a JSON client gets the document it always got.

Verified: `TestStreamingComboFailureFramesUseRealNewlines` drives the real
handler with a combo whose members all answer 503 and asserts the response
carries real double newlines, no literal backslash-n escape, and a
`data: ` prefix on every event. Against the pre-fix code it fails on all
three. `go vet ./...` is clean and `go test ./internal/handlers/chat/
./internal/proxy/...` passes.

Also verified against luqman-v1/9router-go#665 (combo streaming
zero-content failover) applied on top: both suites pass with no
overlapping lines, so the two changes are independent.
