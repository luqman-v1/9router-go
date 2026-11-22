### Fixed

- **Preserve prompt cache breakpoints for OpenRouter Anthropic models** — Retains `cache_control` markers on content blocks when forwarding to OpenRouter models under the `anthropic/` prefix, enabling prompt caching for Claude Code callers (upstream OmniRoute #19755 parity).