### Dashboard: Escape closes only the topmost dialog

- **Escape in the model picker no longer closes the combo dialog under it.**
  With **Add Models Manually** open over Create/Edit Combo, Escape closed the
  combo dialog (losing its unsaved edits) and left the picker floating over the
  page: the picker had no Escape handler and `Modal` listened on the document.
  A `Modal` stacked on a `Modal` (a confirm over a form) closed both for the same
  reason. Dialogs now register on one escape stack (`lib/ui/escapeStack.ts`)
  and only the most recently opened one answers the key.
- Verified: `escapeStack.test.ts`, `make vet-svelte` (77, at the baseline),
  oxlint, build, and in a browser against a live gateway (picker closes alone;
  a second Escape closes Edit Combo).
