### Zed provider parity: tool calling, xAI, live catalog routing, status frames, usage

- `internal/proxy/executor/zed.go`: the `open_ai` lane now reuses the canonical
  `buildResponsesBody` transform instead of a hand-rolled builder. This gives
  `gpt-*` tool turns (e.g. `gpt-5.6-luna`) the strict Responses shape upstream
  requires: normalized object schemas with `properties`, clamped call ids,
  system/developer messages folded into `instructions`, and `store: false`.
  Previously a tool call reached cloud.zed.dev with fields the Responses
  backend rejects, so tools appeared unusable on those models.
- `internal/providers/zed.go`, `internal/proxy/executor/zed.go`: added the `x_ai`
  wire provider with OpenAI-shaped passthrough, and made model routing consult
  the account's live `/models` catalogue first (upstream `resolveModel`), with
  a bounded/TTL cache and id-prefix fallback. A model whose id does not match
  its backend family is now sent with the correct provider dialect.
- `internal/proxy/executor/zed.go`: forward the client's `prompt_id` in the
  completion envelope, matching upstream's `prompt_id` field.
- `internal/proxy/executor/zed.go`, `internal/proxy/executor/zed_stream.go`:
  send upstream's identity/protocol headers (`x-zed-version`, `9router/zed`
  user agent, status/stream-ended/xAI support flags, broad Accept); recognize
  `{"event":...}` / `{"status":...}` frames, translating `failed` statuses to
  an OpenAI error chunk and `stream_ended` to `[DONE]`; refresh the LLM token
  when upstream returns `x-zed-expired-token` / `x-zed-outdated-token`.
- `internal/handlers/dashboard/usage_providers.go`: wire Zed into
  `GET /api/usage/{connectionId}` with the upstream plan/usage shape —
  Edit Predictions and Hosted Model Requests quotas, plan labels, token-billing
  note for a zero request limit, trial/overdue-invoice messages, and the
  `{"user_id} {access_token}"` layer-1 auth.
- Added table-driven regression tests covering tool-body shaping, xAI envelope,
  catalog-driven provider resolution, status frames, and Zed usage parsing.
- Verified with `go test ./...`, `go vet` on the touched packages, and targeted
  executor/providers/dashboard test runs.
