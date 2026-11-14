### Combos: per-combo custom system prompt with global fallback

Add a `customPrompt` field to combos, letting operators set a combo-specific
system prompt that overrides the global custom prompt prefix/suffix when set.
When unset, requests routed through the combo fall back to the global custom
prompt settings unchanged.

Port of upstream decolua/9router PR #5433 / Issue #5376.

**Backend:**
- `Combo` model gains `CustomPrompt` (`json:"customPrompt,omitempty"`).
- `combos` table gains an additive `customPrompt TEXT DEFAULT ''` column.
- Combo execution injects the combo's custom prompt as the system prompt before
  any model attempt; when unset, the global prompt prefix/suffix applies.
- When set, the global prompt is not stacked on top — the combo prompt replaces
  it.

**Dashboard:**
- `CreateComboModal` gains an optional "Custom System Prompt" textarea for
  create and edit.
- `customPrompt` is threaded through `createCombo`/`updateCombo`.

**Verification:**
- `go test ./internal/handlers/chat/ -run "Combo" -count=1` — green
- `go vet ./internal/...` — clean
- `gofmt -l internal/models internal/db internal/handlers/chat` — clean

Closes #1109
