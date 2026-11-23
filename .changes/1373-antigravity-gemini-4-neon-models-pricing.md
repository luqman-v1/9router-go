### Added
- **Antigravity Gemini 4 Neon & Claude 5.5 Max tiers** — Registers Gemini 4 Neon (xhigh/high/medium/low), Claude 5.5 Max tiers, and aligns default Gemini 4 tiers (Carbon, Argon, Hydrogen) to high and Claude 5.5 (Sonnet/Opus) to medium, matching upstream decolua/9router@767d7aeb0.

### Changed
- **Default tier mappings** — `gemini-4-carbon`/`gemini-4-argon`/`gemini-4-hydrogen` now resolve to their high tier; `claude-sonnet-5-5`/`claude-opus-5-5` resolve to medium.
- **Quota lookups** — `claude-*-5-5` quota checks now consult the medium tier (falling back to high).
