### Fix DeepSeek thinking-mode forced tool_choice 400

DeepSeek V4 models think by default, and the Chat Completions API rejects
required/named `tool_choice` while thinking is on with a 400 "Thinking mode
does not support this tool_choice" — the same failure the official client works
around by sending `tool_choice="auto"` on V4 requests. The gateway surfaced the
upstream 400 as-is.

The fallback pipeline now normalizes the DeepSeek API-key lane: a `tool_choice`
other than `auto`/`none` is downgraded to `auto` before dispatch. A request that
explicitly disabled thinking (`thinking.type:"disabled"` or
`reasoning_effort:"none"`) keeps its forced choice, and so does one that
declares no tools at all.

Measured against `api.deepseek.com` with a live key: `tool_choice:"required"`
and a named-object choice both answer 400 as reported; the same requests with
`"auto"` answer 200. `thinking.type:"disabled"` plus `"required"` answers 200
with a real tool call, which is why that path is preserved. The Anthropic lane
(`/anthropic/v1/messages`) accepts `tool_choice:{"type":"any"}` and is
unaffected.

Verified: `TestNormalizeDeepSeekThinkingToolChoice` (unit) and
`TestDeepSeekThinkingRelaxesForcedToolChoice` (integration, real router + fake
upstream) cover forced-required, named-object, `none`, `auto`, disabled-thinking,
no-tools and malformed-JSON cases. End to end through a running gateway with the
reported request body: 200.
