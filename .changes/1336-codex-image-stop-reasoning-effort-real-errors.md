### Fixed
- **Stop sending unsupported reasoning effort on Codex image tools** — Removes reasoning.effort from Codex image tool payloads to prevent 400 complexity rejection, and surfaces actual stream error messages instead of generic entitlement failures (upstream decolua/9router@a6d3cdbf7 parity).
