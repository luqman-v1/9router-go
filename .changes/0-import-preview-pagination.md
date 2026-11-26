### Changed
- The preview panel's layout follows the order the work happens in: title row,
  then a full-width search, then the `All / Not added / Added` split with the
  sort pushed to the right edge of that row, then the action row.
- The chip hint moves out of the action row and into the top of the list, where
  it describes the thing it is about. Previously it sat at the right end of a
  row of buttons and read as a caption for `Remove all`.
- Pagination replaces the growing list and the footer's `Done` button: 30 models
  per page, `Prev` / numbered pages / `Next`, plus a `Page [n] / total` input to
  jump straight to a page. The window keeps the first and last page and elides
  the middle, so page 16 is still one click away.
- The dialog is `max-w-5xl` at `96vh`, sized so a page of chips fits without a
  second scroll region inside the panel (measured: 0px overflow at 1280x720).
- `Done` is gone: the header's X and Escape already close the panel.

### Verified
- `bun test src` 1046 pass (7 new cases for `importPreviewPageCount` and
  `importPreviewPageItems`); `bun run ratchet:svelte` 0 errors;
  `bunx oxlint --deny-warnings` clean; `bun run build` + `go build` OK.
- Browser (playwright-cli, isolated instance, fake 460-model upstream):
  page 1 shows 30 chips and the row reads `Prev 1 2 3 4 … 16 Next Page 1 /16`;
  clicking 4 moves the window to `1 2 3 4 5 … 16` and the first chip becomes
  `model-260-chat` (page 4 = 30-per-page offset); typing 7 in the jump input
  shows `model-422-chat`; page 16 is the last page with 10 chips and `Next`
  disabled; `Prev` steps back; filtering to 11 results drops the pagination and
  shows `30 per page · 1 page`; body overflow is 0px at 1280x720; 390px viewport
  wraps the control without horizontal page scroll; console clean.
