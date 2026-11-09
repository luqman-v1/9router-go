# Evidence — PR #805 (API key quotas, context ceiling, spend per client key)

Recorded from real `9router-go` binaries on ports 20198 and 20199 with temporary
`DATA_DIR`s, not from a unit test or a component harness. Date: 2026-11-02.

The **before** shots come from a worktree at `luqman/main` (`7f155d7`), the
commit this PR is rebased onto. The **after** shots come from the PR head. Both
gateways were booted fresh with `INITIAL_PASSWORD=shotpass`, so the databases
hold no operator data. Each holds two API keys ("Reseller A", "Trial key") and
the same five `usageHistory` rows, so the two dashboards are comparable.

Chromium at 1440×960, and at 390×844 for the two narrow shots.

## Before — what the dashboard could not do

| File | What it shows |
|---|---|
| `before-1-apikeys.png` | The keys list. `Reseller A` carries a 10M lifetime quota and shows `0 / 10.0M tok`; there is no second badge, because a per-request context ceiling does not exist yet. |
| `before-2-policy-dialog.png` | The policy dialog: the quota is written as the raw `10000000`, there is **no** "Context limit per request" field, and the help text says a spent quota is *"refused with 403"*. Model access is a free-text pattern field (`e2e/allowed-*`) with an **Add** button. |
| `before-3-create-key.png` | The create-key modal: a **Key Name** field and nothing else. A key cannot be issued with a budget or a ceiling. |
| `before-4-usage-overview.png` | The Usage Overview over 7 days. There is no panel that groups the spend by the key the callers presented — the page ends at "Usage by Model". |

## After — the same surfaces

| File | What it shows |
|---|---|
| `after-1-apikeys.png` | The same row now carries two badges: `0 / 10.0M tok` and **`200.0k ctx`**. |
| `after-2-policy-dialog.png` | The quota reads `10M` (scale), and the new **Context limit per request** field sits under it at `200K`. The help text now says a spent quota is *"refused with 429"*. |
| `after-3-policy-model-access.png` | The whole dialog: model access is now a **"Choose from the models this gateway publishes"** picker, with "Add a pattern instead" kept for the glob and deny forms a picker cannot express. |
| `after-4-create-key.png` | The create-key modal now carries **Token budget (optional)** and **Context limit (optional)**, so a key can be issued already bounded. |
| `after-5-usage-per-api-key.png` | The new **"Usage per API key — Spend by the key your callers present"** panel: requests, in/out tokens, cached tokens, cost, share and last used per client key, plus the window's totals. The third row, **"No client key"** (Untracked), is the media path, which presents no client key. |
| `after-6-usage-per-api-key-mobile.png` | The same panel at 390×844: it reflows into cards, one per key. |
| `after-7-apikeys-mobile.png` | The keys list at 390×844. |
