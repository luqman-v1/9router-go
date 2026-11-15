### fix(translator): extract in-body system messages to top-level system for Claude Messages

- Extracts and removes `role: "system"` messages from `messages[]` and promotes their text to the top-level `system` parameter for Claude Messages API requests, matching merged upstream `open-sse/translator/request/openai-to-claude.js:36-46`.
- Includes local superset extension promoting `role: "developer"` turns to the top-level system prompt for OpenAI developer-turn compatibility.
