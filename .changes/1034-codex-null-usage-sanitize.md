### fix(codex): sanitize null usage token fields on response.completed event (#1033)

Sanitize token count fields in Responses usage objects from JSON `null` to
integer `0` on `response.completed` events and non-streaming responses,
preventing deserialization failures (`invalid type: null, expected i64`) in typed
clients such as Codex CLI and Rust SDKs.

- `SanitizeResponsesNullUsage` sanitizes `null` values in token counter fields
  (`total_tokens`, `input_tokens`, `output_tokens`, `prompt_tokens`,
  `completion_tokens`, `cached_tokens`, etc.) to `0` when a usage object is
  present, while preserving non-null usage numbers untouched.
- `responsesSanitizingWriter` wraps client-facing Responses writers to ensure
  both SSE stream frames and JSON responses never emit `null` token counters.
- Table-driven unit tests and end-to-end handler tests in `internal/handlers/chat`.
