### Features
- **audio translations**: New route `POST /v1/audio/translations` (and `/audio/translations`) that forwards speech-to-text translation requests to upstream providers, mirroring the `transcriptions` lane (default model `whisper-1`, combo fallback, Antigravity / ElevenLabs STT lanes, Gemini Live transport detection, STT URL override).
- **docs**: Added `POST /v1/audio/translations` to the API surface list in `README.md`.
