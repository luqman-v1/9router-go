### fix(dashboard): keep the request-payload dialog above the sidebar

Clicking **View** in Usage › Details opened the payload dialog with its left
half hidden behind the navigation — the report's screenshot shows the dialog
cut off at the sidebar's edge (issue #729).

The dialog is `position: fixed` inside the content column, and the sidebar
wrapper is a *sibling* of that column. The wrapper carried `z-50` at every
breakpoint, which is right for the mobile drawer (it has to sit above its own
backdrop at `z-40`) but wrong on desktop, where `lg:static` returns it to
normal flow: an index on a static sibling still makes it a stacking context,
and that context painted above the entire column. The dialog's `z-50` could not
compete — it was nested *inside* the column, so its index only ordered it
against its own siblings. Hit-testing confirmed it: the topmost element at a
point inside the dialog's left edge was a sidebar nav label, not the dialog.

`lg:z-auto` drops the index exactly where the drawer becomes the static column.
The mobile drawer is unchanged (`z-50` against a `z-40` backdrop, verified at
390px), and every other dialog opened inside `<main>` is fixed by the same
change — Add Custom Provider was overlapping the sidebar the same way.

Verified in Chromium against the built binary: the dialog now wins the hit test
across its whole box at 1440px and at 390px, and its box sits fully inside the
viewport. The new `web/e2e/requestDetailModal.test.ts` pins it — it fails on the
previous build with the sidebar label as the hit target, and passes with the
fix. Full e2e suite (51 tests) green.
