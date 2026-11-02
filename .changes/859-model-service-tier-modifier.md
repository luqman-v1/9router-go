### feat(models): select OpenAI and Codex service tiers via model modifiers

- Allow clients to select service tiers (Fast/Priority vs Standard/Default) via model name suffixes (`(fast)`, `(standard)`, `(priority)`, `(default)`), including combined dual modifiers such as `(xhigh,fast)` or `(high,standard)` (porting upstream `decolua/9router` PR #5356).
- Model tier suffix overrides request body `service_tier` and strips cleanly from the canonical model name.
- Added comprehensive unit tests in `internal/thinking/intent_test.go` and `internal/handlers/chat/codex_fast_test.go`.
