### Fixed

- **Thinking config preservation on tool-result turns** (upstream #5305):
  - Treat `role: "tool"` (OpenAI) and `role: "function"` (Gemini) as user-equivalent turns so `NormalizeThinkingConfig` does not drop `thinking` on follow-up turns carrying tool execution results.
  - Ensures reasoning models (Claude, DeepSeek, Kimi) continue thinking when synthesizing responses after tool calls.
