### Added
- `Import from /models` stages the fetch in a **Preview Fetch Models** panel
  instead of writing every row into the model list. Clicking the button fetches
  the provider's catalogue and opens a modal between the Model ID bar and the
  list; nothing is saved until the operator acts on a chip. On a 460-model
  catalogue the old one-click import filled the list with 460 rows to undo by
  hand.
- Chips are the staged models: `+` adds that one model and flips the chip to the
  green "Added" state with a red trash beside it, the trash removes it again, and
  a flask probes that model (a real completion, account quota consumed) without
  adding it. Test state is drawn on the chip: spinner while probing, then the
  latency in ms or the error on hover.
- Panel and main list stay in sync in both directions: the panel derives
  "already added" from the parent's compatible/builtin id sets, so removing a
  chip removes the main-list row and vice versa.
- An action row applies the current filter in bulk: `Select all`, `Add all`,
  `Test all`, `Remove all`, their counts following the filter
  (`Select all (11)`), with `Add all` / `Remove all` hidden when the filter
  leaves nothing to add or remove. `Test all` probes every matched model before
  an import; the destructive action stays last.
- A state split (`All` / `Not added` / `Added`) with live counts, sorting (`A–Z`,
  `Z–A`, `Latency: fastest`, `Latency: slowest`), and a client-side search with
  a clear button over a synthetic 460-model catalogue.

### Changed
- The panel is a modal with a fixed header (title, count, close), a full-width
  search, the state split and sort, the action row, one scrolling body, and a
  footer holding the running totals (`shown / added / available` plus
  pass/fail once a probe has run). Before this the controls and the list
  scrolled together, so the search box left the screen the moment the operator
  scrolled.
- The header badge reports the catalogue size and, while a search is active, the
  match count: a query narrowing 460 models to 111 reads 111, not 460.
- Pagination replaces the growing list, 30 models per page with
  `Prev` / numbered pages / `Next` and a page-jump input; the window keeps the
  first and last page and elides the middle. Drawing 460 chips at once meant
  ~3,000 DOM nodes on a phone-sized first paint for a 1 vCPU box.
- The dialog is `max-w-5xl` at `96vh`, sized so a page of chips fits without a
  second scroll region inside the panel (measured 0px overflow at 1280x720).
- Chips are a rounded rectangle rather than a pill, so long ids read as text
  instead of being clipped by a circle; the selection ring is stronger.
- The empty state says "No models found" and offers a reset instead of a blank
  pane; on a narrow viewport the search takes its own row below `sm`.

### Fixed
- The provider-switch reset effect read `showImportPreview` through
  `closeImportPreviewOnProviderSwitch()`, making the effect depend on the panel
  state and re-run the moment `openImportPreview` set it: the modal closed
  itself immediately after opening, so clicking the import button appeared to do
  nothing. Found by driving the dashboard in a real browser against an isolated
  instance, where `/api/providers/<id>/models` returned 200 with a full list
  while no dialog appeared and the console stayed clean. The reset now writes the
  staging fields directly instead of reading the flag.
- The panel is rendered once per page as a modal (backdrop click, X, and Escape
  all close it) rather than inline per section.

### Performance
- `GET /api/providers/{id}/models` is served through a process-level catalogue
  cache (60s TTL, 200 entries, one body per connection) with `singleflight.Do`
  collapsing concurrent misses, so reopening the panel and opening the same
  connection in two tabs cost one upstream call instead of one per click.
  Responses carry `X-Models-Cache: hit|miss`. Editing or deleting a connection
  drops its entry, because the cached body belongs to the previous baseUrl and
  credential.

### Verified
- `go test ./internal/handlers/dashboard/` (cache TTL, error non-caching,
  concurrency collapse, eviction, invalidate-on-edit/delete, writer defaults).
- `cd web && bun test src scripts` 1062 pass; `bun run ratchet:svelte` 0
  unresolved identifiers, 0 errors; `bunx oxlint --deny-warnings` clean;
  `bun run build` + `go build` OK; `go test ./web/...` OK.
- `web/e2e/modelImportPreview.test.ts` drives the panel in Chromium against a
  fake 3-model upstream: the button opens the panel and `/api/models/custom`
  stays empty, the badge reads 3, search `dee` narrows it to 2, a chip's + lands
  exactly that one id and flips the chip to "Added", its trash empties the list
  again. Fails on `main` (`[role="dialog"]` never appears), passes here.
- `web/e2e/modelImportNameConflict.test.ts` still pins the loop walking past an
  id a bare-name alias owns; its apply step is now `Add all`.
- Full `bun run e2e`: 107 tests, 0 failures.
- Measured against an isolated instance and a fake upstream that sleeps 800ms:
  cold call 820ms (`X-Models-Cache: miss`), warm call 2-3ms (`hit`), 6 concurrent
  cold requests = 854ms total with exactly **1** upstream hit.
- Browser (playwright-cli, isolated instance, fake 460-model upstream): state
  segments report 460 / 460 / 0 and update to 460 / 459 / 1 after one add;
  search `model-42` reports 11 shown and `Select all (11)`; clear restores 460;
  page 1 shows 30 chips and reads `Prev 1 2 3 4 … 16 Next Page 1 /16`, page 4
  opens with `model-260-chat`, page 16 holds 10 chips with `Next` disabled;
  body overflow 0px at 1280x720; light and dark themes and a 390px viewport
  checked; console clean.

Closes #1482
