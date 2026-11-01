# Evidence — PR #811 (dashboard sidebar overhaul)

Recorded from real `9router-go` binaries on ports 20198 and 20199 with temporary
`DATA_DIR`s, not from a unit test or a component harness. Date: 2026-10-30.

The **before** shots come from a worktree at `luqman/main` (`35d2c688`), the
commit this PR branches from. The **after** shots come from the PR head. Both
gateways were booted fresh with `INITIAL_PASSWORD=shotpass`, so the databases
hold no operator data and the sidebar draws its default hide list.

Chromium at 1440×960.

## Before — the two bugs this PR fixes

| File | What it shows |
|---|---|
| `before-1-sidebar.png` | No `Advanced Features` section; Security sits inside System; no Search button in the top bar. |
| `before-4-sidebar-card.png` | **The disabled toggles.** Every visibility switch in Settings › Sidebar renders as a muted grey-red pill and swallows the click. The preset dropdown above still works, which is what made the panel look healthy. |

## After — the same surfaces

| File | What it shows |
|---|---|
| `after-1-sidebar.png` | `Advanced Features` above System holding Security; `System` is now a collapsible heading; Search with a `Ctrl K` hint in the top bar. |
| `after-4-sidebar-card.png` | The same card with every switch enabled (bright orange), and the Settings row still disabled — the always-visible guard. |

## After — the new surfaces

| File | What it shows |
|---|---|
| `after-5-palette-staged.png` | `claude sonnet` is typed and **no results are shown**: the query is staged, and the hint under the input says Enter or the Search button runs it. |
| `after-6-palette-results.png` | The same query after Enter — 54 results, first 40 drawn, models ranked with their provider alias and display name. |
| `after-7-pinned.png` | The `Pinned` section above the groups, holding Quota Tracker, pinned from a search result. |
| `after-8-badge.png` | A count beside `Endpoint & Key` — one API key issued, with the sentence in its tooltip. |
| `after-9-group-folded.png` | `Advanced Features` folded: its chevron points right and Security is gone, while `System` stays expanded. |
| `after-10-icon-rail.png` | The 56px icon rail, every label replaced by its icon, and the Display card showing `Pinned entries` and `Collapse to icons` with the unsaved-changes reminder raised. |

`after-2-settings.png` and `after-3-settings-advanced.png` are the Settings page
before and after the advanced disclosure, for context.
