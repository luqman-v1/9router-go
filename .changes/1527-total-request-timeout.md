### Added

- **Total request timeout**: an optional end-to-end bound on one chat request
  (headers plus body), set with `totalRequestTimeoutMs` in Settings and off
  (0) by default. It exists to reclaim a hung upstream, not to cut a slow one:
  reasoning models legitimately stream for many minutes, so the dashboard text
  and the default both keep it out of the way until an operator asks for it. A
  request cut by it answers HTTP 504 with `code: gateway_timeout` instead of the
  499 a client disconnect produces, because only one of those is worth retrying
  unchanged. The bound is applied once at both chat entry points
  (`HandleChatCompletions` and `HandleMessages`) and shares its deadline with
  every later stage, so a request parked in a combo fallback expires too. A
  request that already carries a tighter deadline keeps it, and a request cut
  by the bound does not park the connection for a cooldown: the account was
  never exercised, so the 504 carries `Local` and the cooldown recorder skips
  it.

Provenance: operator feature from `9router-pro` `v0.5.109-pro.28`
(`TOTAL_REQUEST_TIMEOUT_MS`, default 30 minutes, measured from request start in
`open-sse/handlers/chatCore/{streaming,nonStreaming}Handler.js`). No counterpart
in `decolua/9router`; Pro-only reference, not upstream parity. Two deliberate
divergences: this defaults to off rather than 30 minutes, and the bound is
applied in the handler layer rather than inside the stream pipe, so it also
covers combo fallback time that Pro's per-attempt remainder does not.
