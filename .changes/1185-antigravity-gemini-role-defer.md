### Antigravity: defer `role: assistant` until real stream content

`TranslateGeminiChunkToOpenAI` (the Antigravity / Gemini path) no longer emits an
unconditional `role: "assistant"` chunk on the first frame. Empty prefill frames
now carry `delta: {}` so the client is not handed a fabricated assistant turn
before any content, reasoning, or tool call arrives.

The deferred `role: "assistant"` is merged into the first real content,
`reasoning_content`, or `tool_calls` delta, guarded by `GeminiStreamState.RoleSent`
so it appears exactly once. When a stream ends without any content (premature
EOF / empty prefill), the terminal finish chunk synthesizes `role: "assistant"`
to prevent clients from hanging on a turn that never opened an assistant message.

The Claude translator is untouched.
