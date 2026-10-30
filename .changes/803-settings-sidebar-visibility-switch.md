### fix(settings): the Sidebar card's visibility switches answer again
The persona PR (#601) rewrote the sidebar row's `<Toggle>` to `disabled={true}`,
so every switch in the Sidebar card rendered greyed out and swallowed the click.
The preset dropdown still worked, which is what made the panel look healthy and
the switches alone inert. The switch now carries the guard the entry list has
always used — only `settings`, the page the panel lives behind, stays locked —
and the handler goes through `markLayoutEdit`, so the edit raises the save
reminder like every other layout edit in that card.
The same PR dropped a `</div>` in the Display card, leaving the "Advanced
settings" row as the parent of the provider-toggle row, the animation dropdown
and the whole Currency block, so they rendered as children of one flex row
instead of siblings. The compiler had been reporting it as
`element_implicitly_closed` on every build.
Verified: `bun test src scripts` (664 pass), `bun run e2e` (61 pass), `make
vet-svelte` (0 unresolved identifiers, baseline 77 → 76), `bun run build`, `go
test ./internal/handlers/...`. The new `web/e2e/sidebarVisibility.test.ts`
drives a real browser against a real gateway and fails on the disabled switch.
