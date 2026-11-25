### Fixed
- Import from /models preview panel: the provider-switch reset effect read
  `showImportPreview` through `closeImportPreviewOnProviderSwitch()`, making the
  effect depend on the panel state and re-run the moment `openImportPreview` set
  it — the modal closed itself immediately after opening, so clicking the import
  button appeared to do nothing. The reset now writes the staging fields
  directly instead of reading the flag.
- The panel is a modal (backdrop click, X, and Escape all close it) rendered
  once per page instead of inline per section.

### Why
- Found by driving the dashboard in a real browser against an isolated instance:
  the fetch to `/api/providers/<id>/models` returned 200 with a full model list
  while no dialog appeared, no alert fired, and the console was clean.

### Verified
- `bun test src` 1025 pass; `bun run build`; svelte-check ratchet 0 errors;
  oxlint clean.
- Browser E2E on an isolated instance with a fake OpenAI-compatible upstream:
  compatible-node import opens the panel (11 chips), "Import free only" opens it
  with the empty state, search filters client-side ("gemini" → 2 of 11), chip
  add turns it green "Ada" and removes it back, X / Escape / backdrop click all
  close it, and the registered-provider path (cline, 460 models) opens, adds and
  removes with no leftover rows in `/api/models/custom`.
