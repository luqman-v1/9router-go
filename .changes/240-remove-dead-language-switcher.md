### fix(dashboard): drop the dead language switcher — Closes #240

The top-bar menu carried a **Language** item whose `onSelect` was an empty
function, so clicking it closed the menu and nothing else happened. The
dashboard ships English only — there is no i18n layer, and every label in the
SPA is hardcoded — so the item advertised a capability the build does not have.

Removed:

- the `Language` menu item from the top-bar overflow menu (`TopBar.svelte`);
- the "Language & Display" card from the profile settings page, along with its
  `selectedLanguage` state, its read-back from `GET /api/settings`, and the
  `language` key it wrote on save (`ProfileSettingsView.svelte`);
- the now-unreferenced `language` field from the `Settings` TypeScript type
  (`api/client.ts`).

Nothing on the Go side changes: the settings row is a passthrough JSON map and
no handler ever read the `language` key, so a stored value from an older build
is inert and simply stops being rewritten.

Verified with `bun run build`, `bun test` (358 pass), and `bun run
ratchet:svelte` (0 unresolved identifiers, 83 errors — unchanged from the
pinned baseline). The end-to-end top-bar assertion now pins the item's absence
rather than its presence.