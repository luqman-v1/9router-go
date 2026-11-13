### Features & Improvements

- **Antigravity**: Add Gemini 4 Carbon models (`gemini-4-carbon`, `gemini-4-carbon-high`, `gemini-4-carbon-medium`, `gemini-4-carbon-low`) with reasoning effort tier resolution and dashboard quota tracking (upstream `decolua/9router#8aef7480` parity).
- **Antigravity**: Bump IDE / Hub version to 2.24.0 across client identity and MITM handlers (upstream `decolua/9router#f172c3c4` parity).
- **Antigravity**: Support Gemini 4 Argon quota fallback to shared Gemini weekly/session pools for Ultra plans without dedicated Argon quota summary buckets (upstream `decolua/9router#8a833631` parity).
- **Antigravity**: Resolve Claude 5.5 and Gemini 4 Carbon models in quota checks to inspect tiered variants (`claude-sonnet-5-5-high`, `gemini-4-carbon-high`).
- **Antigravity**: Harmonize Argon weekly and session quota keys (`gemini_argon_weekly` and `gemini_argon_session`) with legacy alias backward compatibility across backend and UI.
