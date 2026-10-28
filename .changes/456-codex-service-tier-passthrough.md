### fix(codex): carry service_tier through the Chat→Responses conversion

Codex Fast Mode never reached the upstream on the path most clients take.
`buildResponsesBody` rebuilds the Responses request field by field, and
`service_tier` was not among the fields it named — so a client asking for
`service_tier: "fast"` had the flag set on the Chat Completions body and
silently dropped before dispatch.

- `internal/proxy/executor/transform.go`: `service_tier` is carried through as
  `jsontext.Value`, so presence and the exact value both survive — an absent
  field stays absent (`KindInvalid`), an explicit `null` stays `null`
  (`KindNull`). A `string` field would conflate the two.
- `internal/proxy/executor/codex_token_params.go`:
  `normalizeCodexServiceTier` applies the Codex backend's two-line rule from
  `open-sse/executors/codex.js` transformRequest — `"fast"` → `"priority"`,
  then delete a truthy value that is not `"priority"`. The guard is
  truthiness, so `null`, `false`, `0` and `""` survive, exactly as upstream
  leaves them. `speed` is dropped here because the upstream's final
  `RESPONSES_API_ALLOWLIST` filter is what removes it there, and this port does
  not carry that filter.
- The rule lives in the Codex path, not in `buildResponsesBody`: that builder
  is shared with grok-cli and the other Responses providers, whose backends are
  not behind this allowlist. A test pins that — a `flex` sent to grok-cli still
  arrives as `flex`.
- Absence stays absence. Nothing here invents a tier the caller did not ask
  for: there is no account-level default, and a `speed` hint is not a
  `service_tier`. The parity reference (`origin/master` v0.5.104) has neither
  in the request path.
- Evidence: `go test ./internal/proxy/executor/ -run
  "NormalizeCodexServiceTier"` and `go test -tags integration -run
  "CodexServiceTier|SiblingResponses" ./internal/integration/` pass offline
  (`GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off`). Both halves were neutered and
  re-run red: dropping the passthrough fails four cases, dropping the normalize
  rule fails `flex`/`standard`.
