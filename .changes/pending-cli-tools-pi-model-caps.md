### Configure Pi model input, reasoning and thinkingLevelMap

Port upstream commit 1ca0761 (and follow-ups 50dcaa0cc, 72cb589e1) to the Go
backend and Svelte 5 dashboard: Pi model entries now carry `input`, `reasoning`
and `thinkingLevelMap`, seeded from `/api/models` caps and thinking levels.
Backend merges previous entries by id, preserves hand-set names, clamps caps,
and sanitizes capability fields. Add `web/src/lib/piModels.ts` and
`PiModelsEditor.svelte` plus a live browser e2e test.
