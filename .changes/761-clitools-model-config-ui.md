---
"9router-go": minor
---

feat(clitools): add visual model and client configuration editor to dashboard

- Support reading and writing model, tier overrides (Sonnet, Opus, Haiku, Fable), and autoCompactWindow in clientconfig adapters
- Add CurrentConfigReader interface and implement current configuration reader for Claude Code and OpenAI Codex
- Support AutoCompactWindow parameter in POST /api/cli-tools/{tool}-settings
- Return currentConfig in GET /api/cli-tools/{tool}-settings
- Add CliToolConfigForm component with model autocomplete, API key selection, base URL, Claude tier overrides, and auto-compact threshold controls
- Support direct Save/Apply and Reset/Disconnect from dashboard UI
