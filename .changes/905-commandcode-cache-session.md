### fix(commandcode): conversation-stable session identity so the upstream prompt cache actually hits

- Resolved the CommandCode `threadId`/`x-session-id` pair from a conversation-stable key instead of falling back to a per-request random UUID when the client sends no session header (`commandcodeSessionKey`): session headers → body session fields (Claude Code's `metadata.user_id`, OpenAI's `prompt_cache_key`/`session_id`/`conversation_id`) → conversation prefix (system + first user turn) → connection id.
- Kept the pair byte-identical to the official CLI's derivation (`toCommandCodeSessionUuid`), matching upstream `decolua/9router` #5006, so one conversation keeps one warm prompt-cache key even for clients that carry their session only in the body.
- Added unit tests for body-field session extraction, per-conversation stability, and an end-to-end check that session-less requests no longer mint a fresh `threadId` per request.
