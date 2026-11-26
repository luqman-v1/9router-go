### Changed
- The "Preview Fetch Models" panel is rebuilt as a real modal: a fixed header
  (title, catalogue count, search, close), a toolbar (state split with live
  counts, sort, select-all), a single scrolling body, and a footer that keeps
  the running totals visible (`shown / added / available` plus pass/fail when a
  probe has run) next to a `Done` button. Before this the controls and the list
  scrolled together, so on a 460-model catalogue the search box left the screen
  the moment the operator scrolled.
- New state filter: `All` / `Not added` / `Added` segments, each with its count.
  `Added` turning up empty is the common case right after an import, so it says
  so and offers a reset instead of a blank pane.
- Chips are a rounded rectangle rather than a pill, so long ids read as text
  instead of being clipped by a circle, and the selection ring is stronger.
- Search gains a clear button; the empty state gains a "Reset search and filter"
  action; bulk actions announce what they will act on ("Add all (459)").

### Fixed
- On a narrow viewport the search input was squeezed to an icon-sized box beside
  the title. It now takes its own row below `sm`.

### Verified
- `bun test src` 1039 pass (3 new cases for `filterImportPreviewByState`).
- `bun run ratchet:svelte` 0 errors; `bunx oxlint --deny-warnings` clean;
  `bun run build` + `go build` OK.
- Browser (playwright-cli, isolated instance, fake 460-model upstream):
  state segments report 460 / 460 / 0 and update to 460 / 459 / 1 after one add;
  `Added` filter shows the single chip with its trash button; search `model-42`
  reports `11 shown` and `Select all (11)`; clear restores 460; a chip selection
  shows the batch bar with `Test selected (1)` and `Remove selected (1)`;
  probe updates the footer tally to 0 passed / 1 failed; `Done` and `Escape`
  both close; body is the only scroll region (1010px content in 410px);
  light and dark themes both checked; 390px viewport checked.
