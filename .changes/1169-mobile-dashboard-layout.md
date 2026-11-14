### Mobile dashboard layout: thumb-sized targets, readable values, tighter shell

Dashboard on a phone kept the desktop register: 16px toggle switches, 28px
buttons, and every long value (API keys, skill URLs, model ids) truncated
mid-glyph. The changes below make the narrow viewport a designed state, not a
squeezed desktop.

**Shell**
- `TopBar`: the breadcrumb crumb ("Providers ›") and the page icon only render
  from `sm:` up; on a 390px screen the title now gets the whole row instead of
  wrapping into the truncated "Providers & En…" the before screenshots show.
- `Sidebar`: the three fake macOS traffic-light dots are hidden below `lg:` —
  a drawer on a phone has no window around it, so the dots read as broken UI.
- `App.svelte`: canvas padding steps `px-4 py-4 sm:p-6 lg:p-10`, and the
  scroll canvas carries `pb-safe`; `index.html` gets `viewport-fit=cover` so
  the iOS home indicator never covers the last control.
- Group accordions in the sidebar are full-height tap rows on mobile.

**Tap targets (44px thumb minimum, antislop-layoutmobile R-03)**
- `ui/Toggle.svelte`: every switch in the app (settings, providers, quota)
  keeps its 28×16 visual but gains an 8px invisible halo via a new
  `tap-target` utility (`index.css`), hit-tested in the browser.
- `ui/Button.svelte`: `size="sm"` is 40px tall below `sm:`, 28px from `sm:` up.
  This lifts the Wrap/Copy/Export console toolbar, Reconcile rules, and every
  modal footer button at once.
- `QuotaTrackerView`: the 13 `h-8` filter/stepper controls are `h-11 sm:h-8`;
  the per-connection ARN copy pill gets `py-2 sm:py-0.5`.
- `ApiKeysTable`: reveal/copy icon buttons and the enable/disable/expire pills
  reach 44px on a phone.
- `ComboCard`: the Test/Copy/Edit/Delete icon grid rows are 44px, the strategy
  select is taller, and the two checkboxes carry the `tap-target` halo.
- `ConsoleLogStream`: level-filter chips are `h-11 sm:h-6`.
- `Sidebar` drawer nav rows: `py-2.5 sm:py-1.5`.

**Readable values (truncation where the value is the point)**
- Skills prompt instruction and skill URLs wrap (`break-all`) on mobile.
- API key tokens (`ApiKeysTable`), bulk-created keys, and the tunnel URL in
  `EndpointView` wrap on mobile, truncate from `sm:` up.
- Model ids and proxy URLs in `ProviderDetailView` likewise.

**KPI grids**
- Usage summary and `SummaryKpiCards` drop from a 1-column phone stack (6 cards
  ≈ 700px of scrolling) to 2 columns; Cache/Compression analytics keep their
  existing 2-col base with a tightened gap.

**Token Saver**
- The "Optimal Setup" header stacks on mobile (icon row, then buttons), so the
  previously cut-off "Apply Recommended" button and the squeezed title render
  in full; the guide-toggle chevron no longer overflows the viewport.

**Verification**
- Before/after screenshots at 390×844 across all 16 dashboard routes
  (endpoint, providers, combos, usage, quota, token-saver, cli-tools,
  proxy-pools, plans, persona, security, settings, skills, console, media);
  DOM audit before: 155+ sub-34px controls, one off-viewport icon; after:
  zero horizontal overflow on every route and every remaining small control
  carries the tap-target halo (hit-test verified).
- `bun run build`, `bun run ratchet:svelte -- --tsconfig tsconfig.app.json`
  (74 = baseline, 0 unresolved), `bun test src scripts` (835 pass),
  `go test ./web/...`, `bunx oxlint --deny-warnings`, CGO-disabled builds for
  all `CROSS_TARGETS` pairs.
