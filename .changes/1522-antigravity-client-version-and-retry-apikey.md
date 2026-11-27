### Fixed
- **Antigravity: one client identity per request, not two.** The image, STT and
  dashboard-usage lanes sent `User-Agent: antigravity/cli/1.5.9 (aidev_client; …)`
  beside `X-Client-Version: 2.33.0`, mixing the CLI fingerprint with the IDE
  version. No real client emits that pair, and `antigravity_quota.go` already
  sent the CLI version, so the same account presented two different
  `X-Client-Version` values depending on which surface ran. All four call sites
  now read `providers.AntigravityCLIVersion`.
- **Antigravity: the bare-429 retry keeps its API-key scope.**
  `ForwardGemini` retries once with the `systemInstruction` folded into
  `contents[0]` when the backend answers a bare `RESOURCE_EXHAUSTED`. That retry
  re-translated the OpenAI body without the `apiKeyID` the first translation
  receives, so a bare-key request (no session header) looked up unscoped
  thought-signature keys, missed the signature it stored on the previous turn,
  and fell back to `DefaultThinkingSignature` — which is the corrupted-history
  path the retry chain exists to avoid. The retry now threads `apiKeyID`
  through.

Verification: `go test ./internal/proxy/ ./internal/handlers/media/
./internal/handlers/dashboard/` passes. New tests:
`TestForwardGemini_Bare429RetryCarriesAPIKeyID` (fails with the argument
dropped: the retry body carries no `thoughtSignature`),
`TestHandleImage_Antigravity` and `TestHandleSTT_Antigravity` (assert the
`User-Agent` / `X-Client-Version` pair against the `providers` constants), and
`TestAntigravityDashboardHeaders_ClientVersionMatchesUA`.

Scope: local hardening, not upstream parity — nothing in `decolua/9router`
carries either defect (`open-sse/executors/antigravity.js` has no
`X-Client-Version` split, and its 429 retry keeps the request's own key scope).
The `clientProfile` setting stays as-is: it is read at exactly one place, and
the chat/default lanes deliberately pin the CLI identity because the legacy
`ide` flavor answers 404 on the Gemini 4 Argon tiers.
