### feat(keys): search, filter and a metrics ribbon on the API key list

The key list answered one question — which keys exist — and left the rest to
counting rows. Four numbers an operator actually needs were only available by
reading the table and adding up: how many keys are live, how much traffic they
carry, what they have spent, and whether anything is sitting idle.

**A metrics ribbon of four pills.** Each is a count that is also a control.
`Active` filters to live keys, `Requests` and `Tokens` order by spend (most
first, then least), and `Unused` filters to keys that have never served a
request. Every pill cycles three phases — off, forward, reverse — so one tap
target asks a comparison in both directions. The number itself stays visible in
every phase because it is a total over the whole list, never over the current
filter: narrowing to "paused" must not make `Active` read 0.

`Tokens` and `Unused` replace the `Expiring 7d` and `Expired` pills this work
originally carried. On a personal deployment expiry is a fact about one or two
rows, and the row already says so — `policyBadges` renders `expired` in danger
tone. A pill that reports what the row already reports spends a slot for
nothing. Spend and idleness are the questions with no answer anywhere else on
the card: a deployment with four keys can have one idle, one at a hundred
thousand tokens and one at two billion, and a credential that has never been
used is either a spare or a key left behind.

`Limited` and `Unrestricted` were considered and dropped: on the deployment this
was built against both read 0 and 4 on every load, and a pill that never changes
tells you nothing.

**Search and sort.** A text field matches on name or masked key, and a sort
select orders by newest, name or requests. All three controls run client-side
over the list the component was already handed — a key list is tens of rows, not
thousands, so a round trip per keystroke would cost more than the filtering does.
The filtered list is one derived value that both layouts render, so the table and
the phone cards can never disagree about which keys are showing.

The status and scope selects that used to sit beside the search field are gone:
the pills express both, and on a phone the selects had no room next to the field
while needing two taps into a menu to reach a state a pill already covers. The
search bar keeps only what a pill cannot do — match text, order the result, and
start a new key. `Create Key` moved to the right end of that row rather than the
card header: it is the action taken after reading the list, so it belongs where
the reading ends, and on a phone the header is the first thing to scroll away.

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
