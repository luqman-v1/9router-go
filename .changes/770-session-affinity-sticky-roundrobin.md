### feat(chat): session affinity sticky round-robin for multi-account prompt caching

- Add `SessionAffinityStore` in `internal/handlers/chat` to bind multi-turn client conversation sessions to upstream accounts when rotation is enabled.
- Optimize multi-account setups (e.g. running multiple workspace or terminal sessions across Codex/OpenCode/Claude accounts) so multi-turn conversations stay pinned to the same account prefix cache (>85-95% cache hit), while new sessions balance across available accounts in rotation order.
- Support failover re-binding: when an assigned account encounters HTTP 429, 401, or enters cooldown, the session automatically falls over to the next eligible account and re-binds.
- Extract session identifiers from request headers (including `x-claude-code-session-id`, `x-session-id`, `x-opencode-session`, and `x-session-affinity`).
