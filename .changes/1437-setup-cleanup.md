### Fixed

- Setup wizard follow-up (#1422): three statements that stopped matching the
  code once the 403 gate was dropped.
  - The `advancedMode` sidebar state in Settings is gone. It was loaded, saved
    and moved by the preset dropdown and the per-entry toggles, but nothing on
    the server read the stored key. `navVisibility` — the preset and the
    switches — is the whole mechanism and is unchanged.
  - The wizard's Personal option no longer claims to turn surfaces off
    server-side: it hides entries from the sidebar and removes nothing.
  - The `/api/setup` route comment no longer attributes the design upstream.
