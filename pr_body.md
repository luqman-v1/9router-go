## Summary
Port upstream decolua/9router PR #5433 / Issue #5376: support a custom system prompt per combo with default fallback to the global custom prompt.

## Changes
- **`internal/models/types.go`**: Added `CustomPrompt string` field to the `Combo` struct (`json:"customPrompt,omitempty"`).
- **`internal/db/schema.go`**: Added additive `customPrompt TEXT DEFAULT ''` column to the `combos` table.
- **`internal/db/combos.go`**: Added `customPrompt` to `comboColumns` and `scanCombo` so reads return it.
- **`internal/db/dashboard.go`**: `CreateCombo` and `UpdateCombo` now accept and write `customPrompt`.
- **`internal/handlers/chat/combo.go`**: In `handleComboFallback` and `handleMessagesComboFallback`, when a combo has `customPrompt` set, inject it as the system prompt via `tokensaver.InjectPrefixSystemPrompt` before any model attempt. When unset, the global system prompt prefix/suffix applies unchanged.
- **`internal/handlers/chat/fallback.go`**: Added `comboCustomPromptKey` context marker so `applyTokenSavers` skips the global prompt injection when a combo custom prompt was already injected.
- **`web/src/components/combos/CreateComboModal.svelte`**: Added an optional "Custom System Prompt" textarea for create/edit.
- **`web/src/components/combos/CombosView.svelte`**: Thread `customPrompt` through `handleSaveCombo` to `api.createCombo`/`api.updateCombo`.
- **`web/src/api/client.ts`**: Added `customPrompt?: string` to `Combo` interface and `createCombo` payload.

## Tests
- `internal/handlers/chat/combo_custom_prompt_test.go`: table-driven tests — combo with custom prompt injects that prompt; combo without it falls back to global prefix/suffix.
- All existing `Combo` tests pass (`go test ./internal/handlers/chat/ -run "Combo" -count=1`).
- `go vet ./internal/...` clean.
- DB migration is additive-only (`customPrompt TEXT DEFAULT ''`).

Closes #1109
