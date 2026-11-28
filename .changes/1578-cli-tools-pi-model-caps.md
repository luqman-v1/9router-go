### Configure Pi model input, reasoning and thinkingLevelMap

Port upstream commit 1ca0761 (and follow-ups 50dcaa0cc, 72cb589e1) to the Go
backend and Svelte 5 dashboard: Pi model entries now carry `input`, `reasoning`
and `thinkingLevelMap`, seeded from `/api/models` caps and thinking levels.
Backend merges previous entries by id, preserves hand-set names, clamps caps,
and sanitizes capability fields. Add `web/src/lib/piModels.ts` and
`PiModelsEditor.svelte` plus a live browser e2e test.

Review blockers fixed: `CliToolConfigForm.handleApply` sends `model` on every
apply again (it was dropped for every non-Claude tool, which made
`claude.go` delete the stored `model` on re-apply); the Pi models e2e is
discovered by `bun run e2e` (`pi_models.e2e.ts` → `pi_models.test.ts`) and
asserts the catalog-seeded `reasoning` / `image input` / `Thinking levels`
state; dead `piModelEntry` removed from `internal/handlers/media`.
