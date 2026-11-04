### feat(stream): output generation rate (tok/s) and max gap

- Track token generation timing during SSE streaming in `internal/handlers/shared/types.go`:
  `StreamMetrics` now records `OutputTokens`, `FirstToken`, `LastToken` and `MaxGapMs`.
- `StreamMetrics.NoteOutput` stamps each streamed chunk and widens the max inter-token gap;
  `TokPerSec` reports output tokens divided by elapsed seconds (tok/s).
- Each streamed chunk is timed in the chat forwarder (`forward.go`, the Claude translation
  branch) and via a `streamNoteWriter` wrapper around the response buffer in `fallback.go`,
  so executor-backed providers are covered too without touching the SSE wire protocol.
- The stream summary log line (`logUsage` in `usage.go`) now carries `tok_per_sec` and
  `max_gap_ms` alongside the existing TTFT, latency and token figures.
- Added unit tests in `internal/handlers/shared/types_test.go`.
