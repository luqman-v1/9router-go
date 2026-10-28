### Compress chat requests in flight through Headroom (G14)

- `internal/headroom/guard.go` (new): decides whether a body is safe to hand to
  a lossy compressor at all, before any network call. A request carrying an
  error tool result, a code-editing tool (`Read`/`Grep`/`Edit`/`Write`/`patch`/
  `anchor`), or a tool outside the known-command set is skipped whole. Ported
  from `open-sse/rtk/guard.js`. Tool names are matched case-insensitively, so
  `read` and `Read` are the same tool — the same rule `tokensaver.protectedTools`
  uses, which keeps the two guards from disagreeing about one request.
- `internal/headroom/client.go`: two holes closed in `validateMessagesPreserved`.
  A `text` block that came back empty passed the old check, which only tested
  for nil. And a result that was not meaningfully smaller could still be
  committed; it is now dropped unless the body shrinks past 95%.
- `internal/handlers/chat/fallback.go`: the saver chain calls Headroom after the
  in-process savers and before the global prompt, so it never sees a prompt this
  process adds afterwards. The compression round trip runs under the request's
  own context, so a client that disconnects cancels it instead of leaving the
  handler blocked until the deadline.
- `internal/db/settings.go`: `headroomEnabled`, off by default. Separate from
  `headroomUrl` because that URL has a default the panel fills in, so "a URL
  exists" cannot stand in for "the operator turned this on".
- `internal/handlers/shared/tokensaver.go`: the flag, endpoint and deadline
  travel together, so a caller cannot enable compression against a zero URL.
- Evidence: `go build ./...` and `go test ./... -count=1` pass offline
  (`GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off`), whole module.
- Honest limit: only the OpenAI-Chat and Claude shapes are exercised. The
  Responses/Kiro/Gemini branches the reference implementation also covers need a
  reverse translator each, which this change does not add. Those shapes fall
  through to "not compressed" today rather than to a wrong result.
