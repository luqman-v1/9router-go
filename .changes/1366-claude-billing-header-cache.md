### Fixed

- Strip client `x-anthropic-billing-header:` lines from every system text block before the OpenAI-to-Claude request conversion joins them into the prompt. The header changes per Claude Code turn (cron/subagent flags, client version), so leaving it in the joined text split the cached system prefix; Anthropic only ignores it as the separate `system[0]` block that OAuth cloaking already provides. Port of upstream decolua/9router@971ae638 (upstream #5739).
- Verified with `go vet ./...`, `go test ./...` (0 FAIL) and a successful `go build ./cmd/9router-go/`; the new table-driven test covers plain text, CRLF/mixed case, array blocks, billing-only blocks, header mentions inside instructions, and untouched user text.
