### fix(chat): concatenate repeated tool-call argument deltas instead of deduplicating them

- The forced-SSE JSON path folds an OpenAI stream back into a single Claude
  message, and it merged the `function.arguments` deltas with a heuristic
  rather than by appending them: a delta was dropped when it equalled the
  whole accumulated value, when it appeared anywhere as a substring of that
  value, or when the accumulated value already ended with it
  (`internal/handlers/chat/forward.go`, `sseToClaudeJSON`). The first case
  loses a genuinely repeated chunk — two identical deltas `"a"`, `"a"` folded
  to `"a"` instead of `"aa"` — and the second loses real content, so an
  argument whose payload repeated an earlier fragment arrived with that
  fragment missing and the tool call's arguments stopped parsing.
- This is a **divergence fix**, not a port of a new upstream change. The plain
  append rule was already upstream (`01e4a28f`, #379) at
  `open-sse/handlers/chatCore/sseToJsonHandler.js:158` — `arguments += delta`.
  The Go port was the side that had drifted, carrying a heuristic upstream
  never had. A stream is an ordered sequence of partials, not a set of
  replacements, so concatenation is the only merge that can be correct for
  every stream.
- The sibling `function.name` merge had the same `strings.Contains` guard and
  is now a plain append too, in both `forward.go` and
  `internal/proxy/executor/codebuddy.go`. The codebuddy executor is registered
  for `codebuddy-cn` and `codebuddy-intl` and was reachable through
  `fallback.go` with the old drop-a-delta logic still live.
- `internal/handlers/chat/parity_forced_sse_test.go` pins the behaviour with
  two genuine fix tests — a repeated delta and two interleaved tool indices
  each carrying a repeated delta. Both fail on `origin/main`.
  `internal/proxy/executor/codebuddy_test.go` adds
  `TestSSEToOpenAIJSON_RepeatedArgumentDeltaAppends`, whose repeated fragment
  *is* a substring of what has accumulated (the old guard's firing condition);
  it fails on the pre-fix executor with
  `arguments = "{\"cmd\":\"run echo done\"}"` against the expected
  `"{\"cmd\":\"run run echo done\"}"`.
- Verified by `go build ./...` and `go test ./internal/proxy/executor/
  ./internal/handlers/chat/ -count=1`, both clean.
