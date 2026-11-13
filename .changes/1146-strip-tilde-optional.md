### Bug Fixes

- Strip `~optional` from tool parameters schemas during Gemini and Antigravity translation, preventing HTTP 400 `INVALID_ARGUMENT: Unknown name "~optional"` from Google Protobuf validation (upstream `decolua/9router#5573` parity).
