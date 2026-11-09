### feat(parity): port upstream v0.5.116 features and fixes

- **OpenCode Go endpoint update**: Migrated `opencode-go` default and transport endpoints from retired `/zen/go/v1` to `https://opencode.ai/inference/go/openai/v1` and `https://opencode.ai/inference/go/anthropic/v1/messages`.
- **Codex Image (GPT Image 2.6)**: Added `gpt-image-2.6-flare` and `gpt-image-2.6-sunburst` to Codex provider registry, image tools list, and frontend model catalogue.
- **Claude Code Tool Deduplication**: Added `CleanStrippedToolReferences` to remove dangling `tool_reference` blocks in `tool_result` history for tools that were stripped or deduplicated, preventing upstream 400 errors (z.ai code 1210).
- **DeepSeek Thinking Budget for Compatible Nodes**: Preserved explicit `thinking.budget_tokens` for dynamic `openai-compatible-*` providers (vLLM, SGLang) while retaining legacy bare markers for registered DeepSeek hosts.
- **Kiro Hostname Regionalization**: Restricted AWS hostname regionalization to covered regions (`q`: `us-east-1`, `eu-central-1`; `codewhisperer`: `us-east-1`), preventing unresolvable hostnames and token redirection.
- **Web Fetch Output Format**: Honored requested format by forwarding `x-respond-with` to Jina Reader and `format` in payload to Tavily, while validating supported format sets.
