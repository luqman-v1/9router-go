### Added
- Configurable `RequireClaudeToolType` on `providers.ProviderConfig` so strict Anthropic-compatible endpoints (such as MiniMax gateways) automatically inject `type: "custom"` on tool declarations without hardcoded provider name checks (fixes #682, upstream `decolua/9router#998bb3d9` parity).
