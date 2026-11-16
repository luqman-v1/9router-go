### Antigravity fake-quota 429 prompt-rewrites, 1-hop fallback retry, and Gemini long-decode continuation hops

Ports two upstream fixes from `decolua/9router#5703` into the Antigravity and
Gemini-native execution paths.

#### Fake-quota 429 prompt-rewrites

- `internal/translator/antigravity.go`: `rewriteBrandingText` gains the
  `openclaw` → `antigravity` rewrite row for the system instruction, matching
  upstream's unbounded case-insensitive rule (`openclaw`, `OpenClaw`,
  `OPENCLAW`, `openclaw-go` all rewritten). The conversation is untouched.
- `internal/translator/antigravity_test.go`:
  `TestStripCompetitivePrompts_RewritesOpenClawBrandInSystemInstruction` pins
  the rewrite table.

#### 1-hop fallback retry for bare 429 RESOURCE_EXHAUSTED

- `internal/proxy/gemini.go`: when the Antigravity backend answers a **bare**
  429 `RESOURCE_EXHAUSTED` (no `QUOTA_EXHAUSTED`, `RATE_LIMIT_EXCEEDED`, or
  `"Individual quota reached"` marker), `ForwardGemini` retries once with the
  `systemInstruction` folded into `contents[0]` as a user-role turn. The gate
  is the `isBareResourceExhausted` helper; explicit quota errors are never
  retried. If the retry also fails, the original 429 is returned so the combo
  loop can rotate the connection.
- `internal/translator/antigravity.go`:
  `MoveSystemInstructionToContents` (exported) and
  `moveSystemInstructionToContents` (unexported) implement the rewrite.
- `internal/proxy/gemini_transient_test.go`: three table-driven tests cover
  the bare-429 retry, the explicit-quota no-retry gate, and the
  retry-fails-returns-original-429 fallback.

#### Gemini long-decode continuation hops

- `internal/handlers/chat/gemini_handler.go`:
  `followLongDecodeContinuation` chains requests with `continuationToken` when
  the model is Hydrogen and the response carries `finishReason: CONTINUATION`.
  Up to 16 hops (`maxLongDecodeHops`), the token goes at the top level for
  Gemini native and at `request.continuationToken` for the Antigravity
  envelope (`buildContinuationRequest`).
- `internal/translator/antigravity.go`:
  `isHydrogenModel` detects Hydrogen models; `hardenAntigravityRequest` lifts
  the output-token cap from 64k to 256k for Hydrogen (`maxHydrogenOutputTokens`)
  so long decode responses are not truncated.

Tests: `go test ./internal/translator/ ./internal/proxy/ ./internal/handlers/chat/` pass.