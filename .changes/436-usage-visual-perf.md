### Dashboard: the Usage visuals stop repainting every frame, and the Usage panels become arrangeable

- **Combo flow card.** The route dot was an SVG `<animate>` on `cx` plus a
  `drop-shadow` filter, running at 60fps for as long as the card was mounted —
  including after the combo run had already finished. It is now a single
  composited CSS hop that renders only while a member is being tried, and the
  blurred glows on the trying/connected rows and order discs are gone (the tint,
  border, icon and label still carry the status).
- **Provider topology map**, the measured hot spot on that page (~95% of its
  main-thread task time while requests are in flight): removed the
  `topo-electric` filter (`feTurbulence` with an animated `baseFrequency` over a
  10000x10000 user-unit region plus `feDisplacementMap`) and every inline
  `drop-shadow` on animated elements; one token per direction instead of three
  forward + four reverse dots, and the four electric sparks are gone — ~5 SMIL
  animations per active node instead of ~25; the router pulse and icon shake
  animate `transform` only, with their blurred shadow animation replaced by a
  static glow.
- **Details panels.** Activity Heatmap and Node Health now use the same header
  strip as Recent Request Details (`bg-surface-2`, `font-headline` title,
  caption, chevron) instead of the 10router `Card title icon padding="md"`
  header, which also left a dangling gap under the title while collapsed.
- **Usage dashboard preferences (Settings).** Every Usage panel — Overview,
  Details, Cache Analytics and Compression Analytics, 22 in all — can be hidden
  individually, and the leading groups reordered by drag or by the arrow
  buttons. A Default / Advanced / Simple / Super Lite dropdown fills the whole
  toggle set — Default is what an unarranged dashboard shows (no combo flow, no
  Details-only cards) and the control reads "Custom" once the toggles match no
  preset — and "Reset order" restores one group. The card has no Save of its own:
  an edit raises the same bottom-right "Unsaved changes" reminder the rest of the
  settings form uses, and that Save writes the arrangement (a `usagePanelLayout`
  localStorage preference) alongside the server settings. The panels whose
  position is fixed — the paged request list and its payload dialog — say so and
  cannot be dragged.
- **A folded Display card.** The card held 1.34 screens of controls — the rate
  grid alone is 203px of 13 inputs, the Usage card the same 1204px again — which
  buried Runtime Storage a full screen below the fold. Both are now behind one
  "Advanced settings" switch at the top of Display: off, the two cards render at
  506px each and Runtime Storage sits 184px down; on, everything is exactly as
  before. The fold is a disclosure preference (`advancedSettingsVisible` in
  localStorage), not a feature flag — the currency still formats every cost and
  the arrangement still drives the Usage page while folded — so the switch says
  as much, and nothing is left unreachable. The sidebar feature row keeps its
  own server-saved meaning inside the fold under a distinguishing label.
- **The Usage dashboard preferences survive a saved edit.** The card edits a
  draft; `localStorage` is written only by the global Save, after the settings
  it was edited alongside have been accepted, and the draft only becomes the
  stored value at that point — so a change that was saved is the one a reload
  reads back (verified in both directions: a panel hidden, saved and reloaded
  stays hidden; a panel unhidden, saved and reloaded stays shown, and the Usage
  page draws the same selection). The write itself happens before the layout is
  called stored, so a browser that refuses it — storage blocked, quota — leaves
  the edit dirty with the reminder still up rather than reporting a save that a
  reload would not read back. An edit that is never saved leaves the stored
  value untouched and keeps the reminder up.
- **The Display card reads like the rest of the form.** Its row titles were
  16px — twice the 12px semibold of Security & Password, whose "Require Password
  on Localhost" is the house setting title — so "Unit abbreviation", "Expand
  usage details", "Advanced settings", the sidebar row and the two currency
  field labels are now `text-xs font-semibold text-text-main`, sitting over the
  11px subtle captions they already matched. The `Currency` group heading loses
  its 10px uppercase tracked treatment for that same 12px semibold title, with
  "Every cost is measured…" demoted to its 11px subtle caption — before this it
  was the dimmest line of its own block, introducing a paragraph brighter than
  itself. Its two sub-headings — "Show USD next to the converted amount" and
  "Rates — one USD in each currency" — drop the semibold they had, so they read
  as labels under the group rather than as titles beside the one field label
  ("Display currency") that still carries its weight.
  The rates action is a bordered secondary button
  ("Update currency") instead of a ghosted "Refresh from provider" that read as
  a link to the page rather than the button that fetches the rates.
- **A squeezed switch stays a switch.** A row whose description wrapped — the
  Advanced settings row, at 57px tall — shrank its `Toggle` to 19px of its 28px
  track, which pushed the 12px knob past the end of the pill in the on state.
  The switch is `shrink-0` now, so a long label shortens the text column
  instead.
- **The sidebar has its own card, with presets.** It used to be one row inside
  Display — Plans, the only entry the panel could toggle — and then a list in
  Display's advanced fold; it is now a card of its own, which is where an
  operator looks for it. The card lists the whole sidebar, grouped the way the
  sidebar groups it, with a switch per entry and up/down arrows plus a drag
  handle for the order; `Settings` cannot be hidden (it is the page the panel
  lives on) but it can be moved. A Default / Advanced / Simple / Super Lite
  dropdown fills the switches — 20 / 21 / 16 / 6 entries, with the control
  reading "Custom" once they match no preset — and leaves the arrangement where
  the operator put it. Visibility and arrangement ride in the one server-side
  `navVisibility` value (`{hidden, order}`), so a single Save publishes both and
  every client draws the same sidebar; an empty `order` means "the order the
  build declares", which is what Reset order writes back. Rows move only inside
  their container — a Media Providers child stays with its accordion, and the
  accordion moves as the one row it is — and a drag that starts in one list
  cannot land in the other.
- **The two arrangement editors share one implementation.** The list primitives
  (`normalizeListAgainst`, `moveInList`, `moveBeforeInList`) moved to
  `web/src/lib/orderList.ts`; the Usage panel arrangement and the sidebar
  arrangement both call them, and the panel plan module keeps only the panel
  pairing and the block folding it owns. On the server the same normalizer now
  accepts `order` beside `hidden`: lower-cased, de-duplicated, pattern-checked,
  capped at 64, and `settings` refused from the hide list alone.
- **"Expand usage details" moves to the card it describes.** It set the default
  state of the Usage page's Details panels, but sat in Display among the currency
  preferences; it is now the first row of the Usage dashboard card, above the
  preset.
- **The three preference cards move to the end of the form.** Display, Sidebar and
  Usage dashboard were the first cards on the page, above Runtime Storage, so a
  settings page opened on a currency selector while the sections an operator
  actually saves — storage, password, SSO, routing, observability, the system
  prompt — sat below the fold. They now follow Global System Prompt &
  Anti-Poisoning, in the same order, and the Advanced settings switch still
  folds Sidebar and Usage dashboard away (nine cards open, seven folded).
- **Combo flow and Usage trend are collapsible, and stop querying when hidden.**
  Extends the pattern #429 started: both optional Overview panels are Cards with a
  chevron in the header, closed by default, and the two sit last in the declared
  order so a reader reaches the numbers before a disclosure row. Their headers are
  the Details panels' own — `font-headline text-sm font-bold` title over
  `font-body text-xs` label, measured as Inter 14px/700 and Inter 12px/400, the
  same as Activity Heatmap and Node Health — with the panel's icon in front of the
  title. Hiding the Combo
  flow panel in Settings now also skips `GET /api/combos` outright — the panel
  draws nothing then, so the definitions would only cost a request and memory —
  and turning it back on refetches without a reload, because the fetch reads the
  arrangement reactively.
- **The Usage Details summary cards stay, and the one that clashes with the
  Overview is renamed.** #429
  deleted the five Details summary cards (Total Requests, Lifetime Tokens, Peak
  Day Tokens, Cache Hit Rate, Most Used Model) as redundant with the Overview.
  They stay here: on this branch each of those is an arrangement panel an
  operator can keep or hide, so the delete would remove a control rather than
  duplicate a number. The Overview KPI of the same name counts the selected
  window only, so the Details card now reads "Lifetime Requests" — the all-time
  scope it already had, and the scope of "Lifetime Tokens" and "Peak Day Tokens"
  beside it. Verified in the browser with every Details panel enabled: all five
  render (1,941 / 11.8M / 435.9K / — / nemotron-70b against a harness database
  holding 1,941 `usageHistory` rows), the first card reads "Lifetime Requests",
  and the Overview KPI of the same name is unchanged. The row keeps its place
  beside the Activity Heatmap and Node Health.
- **The lifetime cache-hit rate costs no extra query.** Completes the work #426
  began: `GetCacheUsageRows` scanned the whole ledger with a
  `LIKE '%cached%'` prefilter
  purely to build that one percentage. It is gone; `windowCacheHitRate` computes
  the same number from the trailing-7-day rows the Top Model card already reads,
  with the cache-inclusive/cache-exclusive shapes resolved by
  `max(prompt, cached+creation)` and cache-only imports excluded. `cacheTokens`
  and `cacheRequests` went with the scan — no card read them.
- **Observability & Storage Retention no longer empties itself.** Its database
  size, row counts, reclaimable figure and the three prune/vacuum buttons all sat
  behind `{#if dbStats}`, and `loadDbStats()` swallowed the error — so on an
  install whose dashboard runs without an admin session (requireLogin off), where
  the always-protected `/api/settings/database/stats` answers 401 while every
  other call succeeds, the whole block and its buttons disappeared with no
  explanation. The rows now fall back to "—", the reason is printed with a Retry
  button, the actions stay reachable, and a partial payload can no longer crash
  the render (`undefined.toLocaleString()`). That 401 also no longer trips the
  global unauthorized handler, which was clearing the stored key and bouncing the
  operator to the login screen over a read-only probe.
- **Currency moves into Display.** The Currency card is gone; its content — the
  display-currency selector, the "Show USD next to the converted amount" switch,
  the rate grid and "Refresh from provider" — now sits in the Display card under
  a `CURRENCY` divider, because it is one of those preferences rather than a
  section of its own. The switch was the second column of a two-column grid, so
  from the `sm` breakpoint up it sat beside the selector as if it scoped that one
  control; it is the row under it now. The rate grid fits the narrower column
  (`sm:grid-cols-3` instead of `lg:grid-cols-4`).
- **Details period filter.** The Request Details table now takes the same
  `?period` window as the Overview instead of ignoring the picker:
  `GetRequestDetailsPaged` gained a `since` predicate, the handler derives it
  from `period` (`detailsSince`), and the table, its total and its pagination
  follow the window. The heatmap, lifetime totals and node health keep their own
  windows and still label them.

Verified with `bun run build`, `bunx oxlint --deny-warnings`, the svelte-check
ratchet (0 unresolved identifiers, 81 errors = baseline) and
`go test ./internal/db/ ./internal/handlers/`, then rendered in a real browser
against a seeded local gateway: the combo flow card holds no `<animate>`, no
`filter` and no blurred shadow (1→0, 1→0, 6→0), the topology map still draws
every node, edge and token, a dragged panel reorders the list and the page,
storage stays untouched until the reminder's Save is pressed (and then matches
the arranged order exactly, surviving a reload in both directions), a fresh
profile folds the rate grid, the panel list and the sidebar row behind the
Advanced settings switch (1204px → 506px a card) and unfolds them again, and
`period=7d` narrows the request table and its total.

Upstream parity: `decolua/9router#381` is the combo flow card; the header change
follows the house strip in `RequestDetailsTab.svelte`. No API, schema or `/v1`
behaviour is touched; the only backend change is the additive `since` filter on
the request-detail query.
- **The trend's metric switch moved into the panel header.** Costs / Tokens sat
  inside the chart card, so the panel header named a chart it no longer described.
  The switch is beside the chevron now, and the header label follows it and the
  window: `Tokens per hour, stacked by model family.` / `Estimated cost per day,
  stacked by model family.` `viewMode` and the hourly fact are bindable props on
  `UsageChart`, so the label and the chart cannot disagree. Verified in the
  browser: Costs → cost label + active button, Tokens → token label, Last 7 days
  → "per day", Last 24 hours → "per hour".
