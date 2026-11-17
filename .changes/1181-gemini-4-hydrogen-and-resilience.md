### Gemini 4 Hydrogen model, capabilities, xhigh thinking level, long decode, and Antigravity fake-quota 429 resilience

Adds `gemini-4-hydrogen` plus level variants (`-xhigh`, `-high`, `-medium`, `-low`)
to the registry for `ag`, `antigravity`, `gc`, `gemini`, `gemini-cli`, `gemini-web`,
`gh`, `github`, and `vertex`. Mirrors Gemini 4 Argon wiring (gemini-level thinking,
1M context window, $4/$20 intro pricing) but adds an `xhigh` thinking tier above
`high`, long-decode continuation hops, and Antigravity fake-quota 429 resilience.
Models and behavior are aligned with Antigravity's live `fetchAvailableModels` catalog.

#### Models, Registry & Capabilities

- `ProviderModels` registry lists hydrogen variants (`-xhigh`, `-high`, `-medium`, `-low`)
  on all upstream-facing providers (`ag`, `antigravity`, `gc`, `gemini`, `gemini-cli`,
  `gemini-web`, `gh`, `github`, `vertex`).
- `patternCapabilities` gains `*gemini*hydrogen*` row: vision/PDF/audio/video,
  reasoning/search/tools, `gemini-level` thinking format, thinking cannot be
  disabled.
- `patternThinking` gains `*gemini*hydrogen*` row advertising
  `low|medium|high|xhigh` (ordered before `*gemini-4*` so Hydrogen claims its own
  row); `xhigh` clamps to itself, `minimal`/`none` clamp to `low`.
- Pricing: `gemini-4-hydrogen` and level variants at $4 input / $20 output per 1M,
  cached 0.2, reasoning 20, cache creation 0.375.
- `internal/pricing/tables.go` pricing rows for `gemini-4-hydrogen-xhigh`.
- Dashboard `web/src/lib/models.ts` blocks updated for `ag`, `gc`, `gemini`,
  `gemini-web`, `gh`, `vertex` so routed models appear in the dashboard catalog,
  with `-xhigh` mapping to `(xhigh)` and `-high` mapping to `(high)`.

#### Antigravity Fake-Quota 429 Resilience

- `internal/translator/antigravity.go`: `rewriteBrandingText` gains the
  `openclaw` → `antigravity` rewrite row for system instructions, matching the
  unbounded case-insensitive rule (`openclaw`, `OpenClaw`, `OPENCLAW`, `openclaw-go`
  all rewritten). The conversation is untouched.
- `internal/proxy/gemini.go`: when the Antigravity backend answers a bare
  429 `RESOURCE_EXHAUSTED` (without `QUOTA_EXHAUSTED`, `RATE_LIMIT_EXCEEDED`, or
  `"Individual quota reached"` marker), `ForwardGemini` retries once with the
  `systemInstruction` folded into `contents[0]` as a user-role turn. The retry
  is gated on `cfg.Format != "gemini-cli"` — `gemini-cli` connections skip it
  because the retry is Antigravity-specific (`WrapForAntigravity`). Explicit
  quota errors are never retried. If the retry also fails, the original 429 is
  returned so the combo loop can rotate the connection.
- `internal/translator/antigravity.go`: `MoveSystemInstructionToContents` implements
  the prompt relocation.

#### Gemini Long-Decode Continuation Hops

- `internal/handlers/chat/gemini_handler.go`: `followLongDecodeContinuation` chains
  requests with `continuationToken` when the model is Hydrogen and the response
  carries `finishReason: CONTINUATION`. Up to 16 hops (`maxLongDecodeHops`), the token
  goes at the top level for Gemini native and at `request.continuationToken` for
  the Antigravity envelope (`buildContinuationRequest`).
- `internal/translator/antigravity.go`: `isHydrogenModel` detects Hydrogen models;
  `hardenAntigravityRequest` lifts the output-token cap from 64k to 256k for Hydrogen
  (`maxHydrogenOutputTokens`) so long decode responses are not truncated.

Tests: `go test ./internal/providers/... ./internal/pricing/... ./internal/translator/... ./internal/proxy/... ./internal/handlers/chat/...` pass.
