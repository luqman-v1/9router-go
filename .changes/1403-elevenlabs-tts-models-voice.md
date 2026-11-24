### feat(elevenlabs): refresh TTS/STT catalog and honor OpenAI voice parameter

- Refreshed ElevenLabs catalog to GA models (eleven_v4, eleven_v4_turbo, eleven_flash_v2_5, eleven_flash_v2, eleven_multilingual_v2, eleven_v3, scribe_v3).
- Honor OpenAI-style `voice` field for bare `eleven_*` models while preserving voice inside model string.
- Map `response_format` to ElevenLabs `output_format`.

Upstream parity: decolua/9router#72ad720c8. Closes #1396.
