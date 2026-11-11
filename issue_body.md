## Problem
Currently, users can only set a single global custom system prompt in Settings. Different combos serve radically different use cases (e.g., a "Code-Review" combo needs strict instructions about diff inspections, while a "General-Writing" combo needs a creative persona). Operators cannot customize instructions per combo.

## Background & Upstream Reference
Port upstream decolua/9router PR #5433 / Issue #5376 to 9router-go:
"feat(combos): support custom system prompt per combo with default fallback"

Upstream PR #5433 adds a `customPrompt` field on combos with automatic fallback to the global custom prompt when unset.

## Scope
- Add `CustomPrompt string` field to the `Combo` model and to the `combos` DB schema (additive column `customPrompt TEXT DEFAULT ''`).
- Dashboard: add an optional "Custom System Prompt" textarea to `CreateComboModal` so operators can set it when creating/editing a combo.
- Backend: when preparing messages for combo execution, inject the combo's `customPrompt` as the system prompt. When unset or empty, fall back to the global custom prompt settings (`systemPromptEnabled` / `systemPromptPrefix` / `systemPromptSuffix`). When set, the global prompt is not stacked on top — the combo prompt replaces it.
- Table-driven unit tests covering both paths: (1) combo with custom prompt injects that prompt; (2) combo without custom prompt uses global fallback.

## Acceptance Criteria
- `gofmt -l internal/models internal/db internal/handlers/chat` — clean
- `go vet ./internal/...` — clean
- `go test ./internal/handlers/chat/ -run "Combo" -count=1` — all green
- `make vet-svelte` (or `bun run ratchet:svelte`) — clean for the touched component

## Verification
- New unit tests: `TestComboCustomPrompt_InjectedWhenSet` and `TestComboCustomPrompt_FallsBackToGlobal` pass.
- Full `Combo` test package passes (`go test ./internal/handlers/chat/ -run "Combo" -count=1`).
- `go vet ./internal/...` passes.
- DB schema bootstrap is additive-only (`customPrompt TEXT DEFAULT ''`), matching the repo's no-destructive-migration contract.
