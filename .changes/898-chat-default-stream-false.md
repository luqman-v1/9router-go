### fix(chat): default stream to false when omitted and strip trailing SSE framing

- Defensively extract leading balanced JSON object when upstream appends stray trailing SSE frames (e.g. `data: [DONE]`) to non-streaming responses.
- Prevents client JSON parse failures and ensures usage accounting is retained.
- Verified with unit tests in `internal/handlers/chat/nonstream_trailing_sse_test.go` (upstream `decolua/9router#5384` parity).
