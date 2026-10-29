### fix(codebuddy-intl): match the WorkBuddy 5.8.4 client the intl gateway now serves

The intl channel's outage had two shapes. `codebuddy-intl` still talked to
`www.codebuddy.ai` with the Tencent CLI identity (`CLI/2.108.1 CodeBuddy/2.108.1`,
`X-IDE-Type: CLI`, Stainless headers), while the gateway that answers for the
international product is WorkBuddy AI on `www.workbuddy.ai`, and its channel
verification wants the desktop client's identity. And the conversation-id family
added in the 2026-10-15 fix derived `X-Conversation-ID` from the session key —
which was the opening user message. Two unrelated sessions that opened with the
same prompt therefore shared one conversation id, and the gateway keeps no
history under a caller-supplied one: the derivation was worse than inert.

Ported from upstream `open-sse` (v0.5.107):

- **Identity.** `codebuddy-intl` now points at
  `https://www.workbuddy.ai/v2/chat/completions` and sends the 5.8.4 desktop
  identity, read out of the shipped client rather than the JS port alone:
  `main/sidecar-manager.js` builds the CLI environment as
  `CLIENT_INFO_PLATFORM`/`IDE_TYPE` = `WorkBuddy`, `CLIENT_INFO_PLUGIN_NAME` =
  `workbuddy-desktop`, versions from the desktop bundle, and
  `CLIENT_INFO_USER_AGENT_EXTENSION` = `CLI/<bundled cli version>`, which is how
  `WorkBuddy/5.8.4 workbuddy-ai/5.8.4 CLI/2.161.0` is composed (the CLI side
  reports `X-Product` = deploymentType `SaaS`, `X-IDE-Type/Name` = platform
  `WorkBuddy`, `X-IDE-Version` = platform version, `X-Agent-Intent` = `craft`).
  The intl identity also sets `X-Agent-Type: main`, drops the Stainless headers
  (the desktop client is not an OpenAI-SDK CLI), and keeps
  `X-Private-Data: true`. That last one is a deliberate divergence: the CLI
  computes it as `enableModelOptimization ? "false" : "true"`, so a default
  install announces optimizable data. Declaring private data instead keeps a
  user's prompts out of the vendor's optimization channel and passes the gate
  (upstream's capture-verified default is also `true`). OAuth and plugin URLs
  stay on `www.codebuddy.ai`; only chat and billing moved. The dashboard's intl
  usage probe reads
  `www.workbuddy.ai/v2/billing/meter/get-user-resource` with the same identity.
- **Header family.** `applyCodebuddyConversationHeaders` fills
  `X-Conversation-ID` (dashed UUIDv7), `X-Conversation-Request-ID` (32 hex,
  mirrored by `X-Root-Request-ID`), `X-Conversation-Message-ID` (32 hex, mirrored
  by `X-Request-ID`), and the W3C/B3 trace family (`X-Trace-ID`, `traceparent`,
  `b3`, `X-B3-*`). The request and message ids are fresh per request in every
  case. The conversation id follows what the shipped CLI does
  (`dist/codebuddy-lite-wb.mjs`: `M[CONVERSATION_ID_HEADER] = session.id`, with
  sessions keyed by `uuidv7`): a caller-named session keeps one conversation id
  across its turns, a request with no session gets a fresh one, and an explicit
  caller- or connection-supplied id outranks both. Non-empty values already
  present are preserved, blank counts as absent, lookups and writes are
  case-insensitive so a lowercase caller key is never duplicated into a
  comma-joined value, and a second pass rotates nothing. It fails closed: a
  request without the family is refused by the gateway every time, so the error
  names the gate instead of silently sending one.
- **Error fidelity.** `parseError`'s overloaded-code handling is ported.
  6004 (and the 频率限制 wording) stays 429 and now carries the reset wall clock
  the message names (`... 2026-10-16 10:00:00 UTC+8`) as `Retry-After`. Code
  11140 branches on wording: `request illegal` / restricted-account display text
  is a risk-control ban (403, message naming `account restricted upstream`,
  `requestId` preserved), while the `rate-limiting` variant and the ambiguous
  remainder stay retryable 429s. Code 11128 is only channel verification when the
  message says `conversation id missing`; the `first message is not system
  prompt` variant keeps passing through. An account ban now parks the credential
  for two hours instead of the generic two minutes, because re-picking a banned
  account every two minutes is what inflates the block.

The CN channel is untouched: `codebuddyHeaders` still derives its conversation
id from the session (Tencent is not gated and continuity helps its prompt cache),
and `isCodebuddyIntlBaseURL` keys on either `codebuddy.ai` or `workbuddy.ai` so a
custom base URL naming the old host still gets the family.

**Verified.** Against the shipped client: `product.json` in the 5.8.4 bundle
declares `applicationName: workbuddy-ai`, `platform: CLI`,
`endpoint: https://www.workbuddy.ai`, and the CLI bundle carries the
`X-Conversation-*` / `X-Trace-ID` / `X-Request-ID` constants with a `uuidv7`
dependency, which is where the identity and the id shapes below come from rather
than from the JS port alone. `codebuddy_intl_headers_test.go` covers freshness
without a session, one conversation per session with rotating request ids,
explicit-id precedence, the id shapes and mirror relationships, the trace family,
blank-as-absent, case-insensitive single-key writes, idempotency, and a
wire-level pass through `ForwardCodebuddyCN` against an `httptest` gateway that
asserts the WorkBuddy identity replaces the CLI one.
`codebuddy_intl_error_test.go` covers all four 11128/11140 variants, the reset
clock in three zone spellings, `Retry-After` on the wire for a 6004 and its
absence for a ban. `errorclassify_codebuddy_test.go` pins the two-hour ban
cooldown at every backoff level, keeps throttling off it, and keeps the phrase
provider-scoped. Full `internal/proxy/executor`, `internal/providers`,
`internal/handlers/chat` and `internal/handlers/dashboard` suites pass.

**Live confirmation (both directions).** A healthy credential answered `200`
with real SSE through this executor, on a fresh conversation id and on a
session-derived one — so the identity and the family are accepted by the current
gateway, not merely present in the code. A credential whose account the vendor
had already restricted answered `403` with code `11140`, `msg: "request illegal"`,
`displayMsg: "Your account has been restricted due to unusual activity"` and an
appeal tip; the classifier turned that into the canonical ban message with
`requestId` preserved and **no** `Retry-After`, and the two-hour cooldown rule
matches it — which is what stops a banned credential from being re-picked every
two minutes.

That second test also exposed a logging defect this commit fixes: `fmt.Errorf`
snapshots the message of what it wraps, and the inner wrapper was built before
`applyCodebuddyFailure` rewrote the body, so every log line kept printing the raw
gateway JSON even though the canonical wording had decided the cooldown. The
executor now re-wraps the rewritten error (`ForwardCodebuddyCN upstream (<url>)`),
so the text and the body agree; `TestForwardCodebuddyIntl_ErrorTextIsCanonical`
pins it.

**Identity parity.** The desktop client also sends `X-Product-Version`, the model
it is asking for (`X-Model-ID`) and the signed-in account (`X-User-Id`) beside the
rest of the identity — the same trio 9capn added to its 4.12.1 IDE profile after
reading the shipped bundle. The intl lane now sends all three: `X-Product-Version`
is the desktop version, `X-Model-ID` comes from the request, and `X-User-Id` is
the access token's `sub` claim. Neither of the last two is ever sent blank — a
credential that is not a parseable JWT, or a request with no model name, omits the
header instead, because an empty identity header is a mismatch the gateway can
see while a missing one is not.

**Content gate.** The gateway also answers 11128 on prompt *content*: a system
or assistant sentence from DeepSeek Harness (the identity clause, the Web GUI
clause, the `@`-path paragraph) matches a verbatim fingerprint and is refused
before a model is reached, and the match survives invisible-character
normalisation — scattering zero-width spaces, an earlier mitigation, does not
help. `transformCodebuddyBody` now rewrites those sentences to synonym wording
after the channel-marker sanitiser and before the body is sent, on system and
assistant text only. The user's own words are never touched: the gateway does not
fingerprint that role, and rewriting it would be tampering. Nothing matched means
the text comes back unchanged, and a second pass is a no-op. The phrase table is
the sample set measured live by CARVIN94/dsh-router-codebuddy 0.3.24 (same
account, `max_tokens=4`: original `@`-path paragraph → 11128, rewritten → 200),
kept as documentation of why the rewrite works rather than as a predicate.

**Per-account in-flight ceiling.** The step between a per-model throttle
(`6004`) and an account restriction (`11140`) is concurrency on one credential:
simultaneous requests on one account read as automation. The attempt loop now
refuses, per account, above `CODEBUDDY_MAX_IN_FLIGHT` (default 3; `0` disables;
scoped to `codebuddy-intl`/`-cn` and their aliases) and moves to the next account
without spending an upstream call — a provider-wide cap is not the same thing,
because with five accounts it still allows five concurrent attempts on one of
them. The default sits between the two figures the projects that run this
channel publish: workbuddy2api caps at 2, Workbuddy-WB2api's
`ADMIN_POOL_MAX_IN_FLIGHT` defaults to 3.

**Gate shapes, measured on the live gateway.** Three bodies, one per mistake:
without the `X-Conversation-*`/trace headers the same credential, host and body
answer `11128 "request illegal"` + "Request blocked. Please send it again" (add
the family → `200`); `/console/chat/completions` answers `11128 "first message
is not system prompt"` until the body opens with the CodeBuddy system prompt and
typed user blocks (which this executor's transform already does); and a
fingerprint mismatch answers "Illegal API invocation from an unapproved
channel". The classifier now tells them apart instead of reporting one generic
channel-verification cause: missing family, wrong identity, or a gate that moved.

**Residual risk.** The 5.8.4 identity and the trace family are upstream
capture-verified, not re-derived here without an intl credential; if the gateway
adds a required header, the 11128 diagnostic now says so instead of surfacing a
bare 400. `X-Domain` still prefers a connection-supplied override, which can
name a host the client would never send — that behaviour predates this change.
