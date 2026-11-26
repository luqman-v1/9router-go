### Changed
- The import preview's actions are one row: `Select all`, `Add all`, `Test all`,
  `Remove all`. The select-all checkbox moved here from the toolbar and is drawn
  as a button with the same shape and weight as the others, its glyph switching
  between `check_box_outline_blank` / `indeterminate_check_box` / `check_box`,
  so it reads as another operation on the list instead of a form field.
- `Test all` is new and sits before `Remove all`: it probes every model matching
  the current filter, which is what "test them before importing" needs. The
  destructive action stays last.
- The two mutually exclusive bars (bulk actions when nothing was selected,
  selected-count bar otherwise) collapse into one, so the buttons no longer jump
  row and change label when a chip is selected.
- Counts follow the filter on every button (`Select all (11)`, `Add all (11)`,
  `Test all (11)`) and `Add all`/`Remove all` hide when the filter leaves nothing
  to add or remove.

### Verified
- `bun test src` 1039 pass; `bun run ratchet:svelte` 0 errors;
  `bunx oxlint --deny-warnings` clean; `bun run build` + `go build` OK.
- Browser (playwright-cli, isolated instance, fake 460-model upstream): the row
  reads `Select all (460) · Add all (459) · Test all (460) · Remove all (1)`;
  clicking select-all switches it to `Deselect all 460/460` with `aria-pressed`
  true and every chip selected; deselect returns it to the outline state;
  filtering to `model-42` reports `(11)` on all three buttons; `Test all` on one
  filtered model updates the footer to 0 passed / 1 failed; checked at 1280px
  and 390px; console clean.
