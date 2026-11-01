### Compatible endpoints refuse a prefix that already has an owner

Creating a provider node accepted any prefix. A prefix is the alias every model
the node serves is published under, and model resolution walks the registry
aliases first (`resolveProviderAlias`), so a collision is not cosmetic:

- A node claiming `gh` shadows GitHub Copilot entirely — every `gh/*` request
  answers from the wrong upstream, with no error anywhere.
- A node claiming `oc` shadows OpenCode, `cbai` shadows CodeBuddy INTL, `cl`
  shadows Cline. The registry has ~140 such aliases.
- A node claiming a canonical id that has no alias (`openrouter`, `nvidia`) is
  an owner too, because those are published under themselves.
- Two nodes sharing a prefix make routing non-deterministic, and which one wins
  depends on map iteration order — so the same request can behave differently
  across restarts.

`providerNodePrefixConflict` checks all three owner tables and the create and
update handlers answer `409 PREFIX_CONFLICT` naming the owner, so an operator
sees which node to edit rather than a bare prefix. The node being edited is
excluded, so keeping its own prefix across an unrelated change (a base-URL edit)
is not reported as a conflict. Matching is case-insensitive, because a
different case is the same prefix on the wire.

A lookup failure does not block the write: the create path has already
validated everything else, and refusing on a transient read error would strand
an operator mid-edit. The worst case is the collision this check exists to
prevent, which is the previous behaviour anyway.

Four existing tests were using prefixes that turned out to be real registry
owners (`bai`, `voyage`, `oc`) — the guard correctly refused them, which is
evidence it works rather than a regression. Their fixtures now use free
prefixes; the cases still pin what they were written to pin (id collision,
rename, apiType validation, suffix whitespace trimming).

Verified: `go build ./...` clean; `go test ./internal/handlers/dashboard/`
passes. New tests cover refusal against a registry alias, a registry id,
another node, case-insensitivity, the free-prefix path, the own-prefix edit
path, and the owner table itself.
