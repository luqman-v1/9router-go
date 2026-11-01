### feat(keys): bulk-create API keys, and hand back the batch as a file

Standing up N clients meant clicking the create modal N times and copying N
secrets one at a time. The create dialog is now tabbed Single / Bulk — the
segmented control the Proxy Pools dialog uses — and Bulk takes one key per
line, `name | machineId` with both halves optional.

**Bulk create, and the secrets as a file.** The create dialog is tabbed
Single / Bulk — the segmented control the Proxy Pools dialog uses, so the two
"one by hand vs many at once" dialogs match. Bulk takes one key per line,
`name | machineId` with both halves optional, and plans the paste on every
keystroke so the preview counts what will happen before anything is sent: a
line with too many separators is refused rather than truncated, because the
dropped text is a machineId the operator meant to send, and a name that
collides with an existing key or repeats inside the batch is named as the
problem it is rather than silently renumbered.

Each planned line is one `POST /api/keys` — the same endpoint the single tab
calls — so the batch path cannot mint a credential the dashboard never saw and
needs no new contract. Requests go one at a time rather than with
`Promise.all`: a burst of concurrent writes is the pattern behind the N+1 and
race findings this repo keeps fixing, and a dozen keys is not worth a parallel
write path. A partial batch is reported as what it is — "created 2 of 3" — with
the secrets that did succeed, because those keys exist and their secrets were
shown once.

The result dialog lists every secret in one scrollable list with a per-row
copy, and offers **Download .txt** as `name<TAB>machineId<TAB>secret`. Tab
separation rather than CSV because a machineId is free text and a name may
contain a comma. A secret appears exactly twice — in the dialog and in the file
— which is the point of the file: an operator creating five keys should not
have to copy five secrets one at a time before the dialog closes.

**Verified:** `bun run build`, `bun run ratchet:svelte` (0 unresolved
identifiers, 75 errors against a baseline of 76), `bun test src scripts`
(695 pass), `go test ./internal/handlers/dashboard/ ./internal/keikey/
./internal/db/`, and the Playwright suites `e2e/apiKeys.test.ts` (6 pass) and
`e2e/mobileApiKeys.test.ts` (7 pass) driving a real binary and a real Chromium —
the row menu's confirmation guards, the copy-while-hidden path and the phone card
layout all still hold against the new ribbon and search bar.
