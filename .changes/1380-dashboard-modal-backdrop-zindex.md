### Fixed

- Dashboard: eight hand-written modals (Create/Bulk/API Key Created, Tool Config Detail, Policy, Create/Manage Combo, Request Details, Extras confirm) now close when the backdrop is clicked, while clicks inside the panel still leave them open.
- Dashboard: the Create API Key and API Key Created backdrops in `EndpointView.svelte` used the escaped class `z-\[100\]`, which never matched a Tailwind utility and computed to `z-index:auto` below the topbar; both are now `z-[100]`.
- Dashboard (Persona): the "Per-key bindings" and "Resolver check" sections now carry a heading and subtitle like the other sections on the page.
- Added `web/e2e/modalBackdropClose.test.ts`, which drives a real gateway in Chromium to pin backdrop close, the computed `z-index:100`, and both Persona headings. Verified failing on `origin/main` and passing with the fix.
