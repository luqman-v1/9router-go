### Dashboard: Escape goes through the dialog stack in every hand-rolled dialog

- `AddConnectionModal`, `EditConnectionModal`, `ChangelogModal` and `UpdateModal`
  render their own overlay and registered a raw `window` keydown listener for
  Escape. One Escape therefore reached every open dialog: with a picker over a
  form, both closed — and when the top layer had no handler of its own, the
  dialog underneath closed while the top one stayed. They now register with
  `pushEscapeHandler` from `web/src/lib/ui/escapeStack.ts`, the stack
  `Modal.svelte` and the model picker already use.
- `UpdateModal` keeps its scroll lock and shutdown-timer cleanup by releasing
  the stack entry from the same effect teardown.
- Verified: an e2e case boots the real gateway and asserts Escape closes Add
  Connection while the page underneath stays; `escapeStack.test.ts` covers the
  stack's push/pop ordering; the svelte-check ratchet resolves the new imports.
