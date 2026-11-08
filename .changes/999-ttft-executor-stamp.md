### fix(executor): stamp TTFT + ResponseBuf on every streaming path so the dashboard detail shows time-to-first-token

Every streaming executor must record time-to-first-token on the dashboard
usage detail, but several paths never wrote `req.TTFT` or `req.ResponseBuf` —
so `latency.ttft` was saved as 0, which the UI renders as "— (non-stream)"
and which is excluded from the TTFT averages. The provider `commandcode`
was the reported case: `handleCommandcodeStream` wrote the SSE frames but
never stamped TTFT nor captured the response body for logging.

A shared `markTTFT(req, startTime)` helper (`executor.go`) now owns the
once-per-stream stamping: it guards nil req / nil TTFT / already-stamped,
falls back to `req.StartTime`, stamps `time.Since(start)` in milliseconds,
and is idempotent so only the first semantic chunk counts. All duplicated
inline patterns across the package are replaced with it:

- Previously missing, now patched: `handleCommandcodeStream` and
  `handleKiroStream` (`stream.go`), `geminiStream` (`gemini.go`, opencode-go
  gemini lane, now takes `req *Request`), `streamPplxAsOpenAI`
  (`perplexity_web.go`, gated on `content`/`reasoning_content` deltas so the
  role-only frame doesn't count), and `windsurf.go` (TTFT was set but
  `ResponseBuf` was never written).
- Refactored to the helper: Codex stream (both OpenAI and Claude-translated
  lanes), `handleClaudeMessagesStream` (both lanes), `sseStream` (all three
  TTFT sites), the Responses bridge writer, `deepseek_web`, `grok_web`,
  `trae`, `zed`, `zed_google`, `deepseek_web_toolturn`, and the gemini-web
  emitter's own `markTTFT` method. Non-streaming paths are untouched —
  TTFT stays 0 for them by contract.

Also fixes `geminiStream`'s payload handling: `proxy.ScanStream` already
strips the `data:` prefix, so the old `HasPrefix(cs, "data: ")` check
rejected every real chunk; the handler now normalizes both forms.

Verified: `go build ./internal/proxy/executor/`, `go vet` on
`executor` + `handlers/chat`, full executor suite
(`go test ./internal/proxy/executor/`) passing, and five new tests in
`ttft_stream_test.go` — `TestMarkTTFT_StampsOnce`,
`TestMarkTTFT_NilSafe`, and per-path tests for commandcode, kiro, and
gemini proving TTFT > 0 and a non-empty ResponseBuf.
