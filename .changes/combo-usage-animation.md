### Usage dashboard: show the route a combo request took

- The usage dashboard showed only the member that served a request, so a combo
  run was indistinguishable from asking for that member by name: which combo the
  client asked for, and which member of it did the work, was invisible.
- The tracker now carries both names next to the member — `requestedModel` and
  `comboName` on active and recent requests — paired with the same counter key
  the counters use, and cleared when that counter reaches zero. The counters
  cannot separate two requests for the same member, so an in-flight row takes the
  names of the latest start; completed requests are unaffected, each carrying its
  own names. `comboName` is set only when a combo served the request, so the
  dashboard acts on a statement from the backend instead of guessing which model
  names are combos.
- `TrackPending` keeps its signature and now delegates, so other callers (media)
  are unaffected.
- Recent requests also carry `latencyMs`, which `logUsage` already had: a member
  that answered shows how long it took. A member that refused shows no latency,
  because a rejection has a time-to-failure, not a latency.
- New card above the provider map: `9router-go → combo → members`, with the
  members as a numbered list in the combo's own order — one row each with its
  provider, model, status and latency. A failed row names the member it handed
  off to, the row that answered says the rest were not used, and later rows are
  dimmed. A single column also gives the text the full card width on a phone,
  where a multi-column layout left the member column too narrow to read.
- `web/embed.go`: the entry document and client-side routes are served with
  `no-cache`, and content-hashed assets with `immutable`. Embedded files carry no
  ModTime, so a browser had nothing to revalidate against and could keep serving
  a previous bundle after an upgrade.
- Verified: `go build ./...`, `go vet`, `go test ./internal/usagetracker/
  ./internal/handlers/chat/ ./web/` (new `TestTracker_RequestedModelPairing` and
  `TestTracker_RecentRequestCarriesLatency`), the dashboard bundle builds in CI,
  and on a live instance the usage stream carries `comboName`/`requestedModel`
  while a combo request runs (`comboName=xcode-cepat`, member
  `opencode/cat-jam-free`, `latencyMs=1351`) while a plain request carries the
  name it asked for and no combo name.
