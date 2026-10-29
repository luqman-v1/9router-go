### fix(thinking): route deepseek-v4* /responses with nested reasoning via the declared format contract

upstream decolua/9router d07e5bc3 parity (#631, upstream #3014). A Responses-format
client asking opencode-go for a deepseek-v4* model was converted to Chat Completions
and bridged back instead of relayed, because the lane decision read the model's
*target* format rather than whether the model serves the Responses wire at all —
and deepseek-v4.1-flash was not even declared, falling to the chat-only default.
Upstream relays such requests natively and nests reasoning:{effort,summary}; the
Chat-shaped fields written into a Responses body are what made the upstream answer
503 "Endpoint is unavailable" and silently fall a combo through to the next model.

- `openCodeGoModelFormats` declares `deepseek-v4.1-flash` as
  `{openai, [openai, claude, openai-responses]}`, mirroring the upstream curated
  entry (the family fallback only matches `deepseek-v4-(pro|flash)`).
- `UpstreamSpeaksResponses` for the opencode lanes asks `SupportsFormat(responses)`
  instead of `TargetFormat == responses`, so a `/v1/responses` client plus
  deepseek-v4* relays untouched while chat-only models (`glm-5.2`, …) are still
  translated in.
- `ForwardOpencodeGo` routes on a catalog-driven `goTargetFormat` — the paid
  sibling of `zenTargetFormat`/`museTargetFormat` — replacing the hand predicate
  `isOpencodeGoResponsesModel`, whose stale list routed `grok-4.5`, `grok-4.7` and
  `gpt-6-luna` (responses-only upstream) to `/chat/completions`. A Chat client
  asking deepseek-v4* keeps the deepseek thinking shape on `/chat/completions`,
  exactly the upstream behaviour the fix preserved.
- `normalizeMuseSparkResponsesBody` omits `reasoning` for effort "none" (nested and
  top-level) instead of sending `effort:"none"` — a different, billable request —
  and preserves a client-set `reasoning.summary` it used to clobber; same semantics
  as `applyZenResponsesReasoning` and the upstream `openai-responses` branch of
  `applyFormat`. The `max→xhigh` clamp stays (upstream `normalizeOpenAILevel`).
- The optional declarative `thinkingFormat` transport field is not ported as a
  separate concept: the catalog's per-model `SupportedFormats` already is that
  declaration, and the lane choice now reads it.

Verified: `go test ./...` green (the two failures outside the change reproduce on
pristine main / need a built `web/dist` in a fresh worktree), the integration suite
(`go test -tags=integration ./internal/integration/...`) green, and new tests mirror
the upstream `thinking-unified.test.js` cases: nested reasoning with Chat-shaped
fields dropped, "none" omitted from both shapes, a client-set summary preserved,
muse unchanged, plus `TestGoTargetFormat` pinning the lane matrix (responses client
→ `/responses` for deepseek-v4*, chat client → chat; grok-4.5/4.7 and gpt-6-luna →
responses for every client).
