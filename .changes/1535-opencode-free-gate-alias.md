### Fixed

- OpenCode free traffic from tool-calling CLIs such as hermes/omp no longer gets
  a misleading `429 FreeUsageLimitError` just because a tool declaration is
  named `terminal` / `run_code` or uses `old_string` / `new_string`. Upstream
  issue decolua/9router#5319 rejects a tool name containing `terminal`, #5690
  rejects `ReportFindings` together with `ScheduleWakeup` or
  `DeferredToolPlaceholder`, and #5275 reports snake_case patch keys as the
  trigger. The free lane now aliases those declaration names and keys to
  neutral spellings before forwarding, then restores the caller's originals on
  the response, including fragmented SSE tool calls and non-stream JSON.
- Only `ReportFindings` is aliased for the #5690 pair; renaming its companion
  tools would change callers without changing the gate result. Body shaping
  keeps `shapeOpenCodeFreeBody` and `zenShapedBody` signatures compatible, and
  SSE detection no longer mistakes a JSON message containing the text `data:`
  for a stream.

Verification: `go build ./...`, `go vet ./internal/translator/
./internal/proxy/executor/`, and `go test ./internal/translator/
./internal/proxy/executor/ -count=1` pass. New tests cover declaration
aliasing, full payload restore, SSE fragments, and free-lane shaping. Live
bridge verification remains pending because the local egress quota is exhausted
and every variant returns 429 before the gate can be distinguished.
