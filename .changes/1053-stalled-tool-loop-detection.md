### feat(resilience): detect stalled agentic tool loops and prevent runaway retries
- Autonomous coding agents (Claude Code, Cline, Roo, OpenClaw) can enter repetitive "stalled tool loops" where the agent issues the identical tool call (same tool name and identical arguments hash) repeatedly without making state progress or changing context. This wastes upstream tokens, depletes quotas, and risks automated abuse bans.
- Added `StalledToolLoopDetector` and conversation history inspector `InspectHistoryTurns` in `internal/resilience/` to track consecutive repeated tool calls.
- Integrated detection into chat handlers (`HandleChatCompletions` and `HandleMessages`) to intercept requests before dispatching upstream when consecutive tool turns match the loop threshold (default 3).
- Added structured, client-compatible interventions for OpenAI and Claude clients (both streaming SSE chunks and non-streaming JSON) prompting agents to alter their strategy.
- Verified with unit tests covering threshold triggering, non-identical arguments/tools resetting, user message reset, and SSE/JSON framing.
