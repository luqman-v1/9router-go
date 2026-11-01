### fix(translator): auto-heal Monitor, Skill, and Grep tool argument hallucinations

When non-Claude models (such as OpenAI Codex) execute tool calls in Claude Code, they frequently hallucinate conflicting or aliased parameters:
- `Monitor`: model provides both `command` and a dummy `ws` object (`{"protocols":[], "url":"wss://localhost"}`), triggering Claude Code's schema error `"exactly one of command or ws"`.
- `Skill`: model emits `name` instead of `skill`.
- `Grep`: model hallucinates a boolean parameter `n` (borrowed from the shell command flag `grep -n`).

Fixes applied:
- Add `sanitizeMonitorArgs`: strips dummy `ws` when `command` is set, preserving single-source monitor requirements.
- Add `sanitizeSkillArgs`: normalizes `name`, `skill_name`, and `skillName` to `skill`.
- Update `sanitizePatternArgs`: strips hallucinated parameter `n`.
- Add test coverage in `internal/translator/sanitize_test.go`.
