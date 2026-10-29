---
"9router-go": patch
---

fix(codex): add session_id header, prompt_cache_key injection, reasoning continuity, and responses allowlist

- Pin prompt cache routing by forwarding `session_id` header to `chatgpt.com/backend-api/codex/responses` (falls back to connection ID or "default")
- Inject `prompt_cache_key` into request body for session continuity when absent, matching upstream `open-sse/executors/codex.js`
- Include `reasoning.encrypted_content` when reasoning effort is active for multi-turn reasoning continuity with `store=false`
- Strip server-generated item references and IDs (`rs_`, `fc_`, `resp_`, `msg_`, `item_reference`) from Responses input
- Strip unsupported parameters and enforce `RESPONSES_API_ALLOWLIST` for Codex upstream calls
- Support `workspaceId` and `accountId` as fallbacks for `ChatGPT-Account-ID` header
- Preserve `prompt_cache_key` across Chat Completions to Responses API translation
