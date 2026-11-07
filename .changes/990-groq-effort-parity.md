### 🐛 Groq gpt-oss effort clamp and upstream strict-message parity

- Groq answered `400 "reasoning_effort must be one of low, medium, or high"` for `openai/gpt-oss-120b` (probed live: `minimal`, `xhigh`, `auto`, `none`, `max` all rejected; only `low|medium|high` return 200). `normalizeGroqGPTOSSRequest` now clamps `reasoning_effort` on every gpt-oss request to the nearest valid level by rank.
- Upstream parity: `FilterToOpenAIFormat` now drops echoed `reasoning_content`/`reasoning`/`reasoning_details` on assistant history for groq/mistral/cerebras (upstream paramSupport `dropMessageFields`), wired via `providers.StrictReasoningRemoving()`.
- Thinking levels for groq/gpt-oss narrowed to `[low,medium,high]` to match Groq's enum; registered as an intentional divergence in the fixture test (upstream still advertises the openai default).
