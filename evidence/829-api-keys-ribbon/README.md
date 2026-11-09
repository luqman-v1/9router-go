# Evidence — PR #829 (API Keys: search, filter and a metrics ribbon)

Recorded from real `9router-go` binaries on ports 20601–20613 with temporary
`DATA_DIR`s, not from a unit test or a component harness. Date: 2026-11-01.

The **before** shots come from a worktree at `luqman/main` (`315bb2d9`), the
commit this PR branches from. The **after** shots come from the PR head
(`7de2d76c`). Both gateways were booted fresh with `INITIAL_PASSWORD=shotpass`
and seeded with three keys through `POST /api/keys` — `production`, `staging`,
`hermes` — so the ribbon and the card list have rows to describe and neither
database holds operator data.

Desktop at 1440×960, phone at 390×844.

## Before

| File | What it shows |
|---|---|
| `before-1-desktop.png` | The key list is a five-column table. `Create Key` sits in the card header as a full-width button. No totals of any kind: whether a key is live, how much traffic it carries and whether anything is idle are all questions the operator answers by reading rows and adding them up. |
| `before-2-mobile.png` | Below `md` the same list renders as `<li>` cards. The header still carries `Create Key`, so it survives the scroll. |

## After

| File | What it shows |
|---|---|
| `after-1-desktop.png` | A four-pill ribbon — **Active**, **Requests**, **Tokens**, **Unused** — sits above the table, each pill a count that is also a control. A search row follows: a text field matching on name or masked key, a sort select, and `Create Key` moved to the right end of that row. The table itself is unchanged. |
| `after-2-mobile.png` | The ribbon stacks to two columns (`grid-cols-2 sm:grid-cols-4`) so each pill still reads on a 390px card. The search row keeps the field, the sort and `Create Key` together. The cards below are now disclosures: the header row is the toggle and the secret, policy, usage, `Regenerate` and the overflow menu render only while a card is expanded. |

## What the shots are evidence of

- **The four pills render and read.** `Active 3`, `Unused 3` on this seed — three keys, none of them used. `Requests 0` and `Tokens 0` are totals over the whole list, not over a filter, which is why they do not move when the list is narrowed.
- **`Create Key` is reachable at both widths.** Desktop shows it at the right end of the search row; mobile shows it in the same row, with a fixed `aria-label` so `getByRole('button', { name: 'Create Key' })` matches below `sm` too.
- **The empty state still offers the action.** Not captured here because these runs seed three keys; verified separately by `e2e/apiKeys.test.ts` and `e2e/mobileApiKeys.test.ts`, which both boot an empty database and click `Create Key` before anything else.
- **The mobile cards are disclosures.** The expanded state is what carries the overflow menu, which is why the mobile suite opens a card before reaching for `Actions for …`.

## Reproducing

```bash
# before: a worktree at luqman/main, web/dist built, then
DATA_DIR=$(mktemp -d) PORT=20601 INITIAL_PASSWORD=shotpass ./9router-before.exe
# after: the PR head, web/dist built, then
DATA_DIR=$(mktemp -d) PORT=20602 INITIAL_PASSWORD=shotpass ./9router-go.exe
```

Seed three keys through `POST /api/keys` with the session cookie from
`POST /api/auth/login`, then open `/dashboard/endpoint` at 1440×960 and 390×844.
