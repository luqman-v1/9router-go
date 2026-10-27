### Fix CLI Tools status reporting "Not configured" for Claude Code

- Fixed `GET /api/cli-tools/all-statuses` omits `has9Router` detection in `cliStatus`, causing installed CLI tools (including Claude Code) to always display "Not configured" in the dashboard even when already configured with 9router.
- Added `has9Router` probe in `internal/handlers/media/clitools.go` detecting Claude Code configuration from environment variables (`ANTHROPIC_BASE_URL`, `NINEROUTER_URL`) and `~/.claude/settings.json` (or `~/.claude.json`), as well as other CLI tools (Codex, OpenCode, Droid, Crush, OMP, Hermes, Pi).
- Added `has9Router` to `GET /api/cli-tools/{tool}-settings` response in `cli_tool_settings.go`.
- Avoid duplicated tool name in `CliToolsView.svelte` accessibility and selection by setting `alt=""` on the decorative tool icon.
