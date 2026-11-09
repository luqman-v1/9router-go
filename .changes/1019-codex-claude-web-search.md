### Codex: translate Claude Code web_search_20250305 to native web_search

Translate Claude Code's `web_search_20250305` function tool declaration and
forced tool choice into Codex-native `web_search`, using the existing Codex
account without an MCP replacement or separate search provider.

- Recognize `web_search_20250305` as Codex native `web_search` in the Responses
  request transform, for both Responses-shaped and Chat Completions-shaped bodies.
- Convert a forced tool_choice naming `web_search_20250305` into the
  Codex-native `web_search` choice.
- Render search results into citation text for Claude Code-readable search blocks.
- Report unsupported controls and failed/incomplete search responses instead of
  returning an unanswered function call or an empty success.

Adds comprehensive table-driven tests for tool declaration translation, streaming
output chunking, and error cases.

Port of upstream decolua/9router PR #5496 / Issue #5485. Fixes #1018.
