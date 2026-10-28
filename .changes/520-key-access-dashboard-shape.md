### feat(dashboard): read and write a key's model access in the Node shape

The chat gate already enforced a key's model allow-list, but the dashboard had
no way to see or change it: the listing and the single-key update spoke only
the flat `models` list, while the Node dashboard speaks
`access: {restricted, allow}`. A client that understood only the Node shape
saw an unrestricted key and could not restrict one — the policy the request
path enforced was invisible to the operator who set it.

Both endpoints now carry the `access` object as well, in both directions. The
mapping is not a second store: every write still goes through
`SetAllowedModels` and every read through `GetAllowedModels`, so the chat gate
keeps seeing one source of truth.

**The two shapes are not the same language, and the bridge says so.** Go's
store predates this bridge and is a pattern language (#365): `*` globs, and
`!term`/`-term` deny. A set with no positive entry is deny-only, which the gate
reads as "allow everything except these". Upstream's shape is a set of exact
resolved identities and states in its own header that it has no wildcards and
no deny mode. So the same rows mean different policies on each side:

- **Writing through `access` accepts exact identities only.** `["*"]` and
  `["-gpt-4o"]` were being stored as they came in, which meant a client asking
  to restrict a key got one the dashboard called restricted and the gate treated
  as open — the flag and the policy disagreed in the widening direction. Both
  now return 400, and the key keeps the policy it had. The flat `models`
  endpoint keeps its pattern semantics: this narrows the new door, it does not
  take an existing one away.
- **Reading reports `restricted` for any non-empty policy, and an empty `allow`
  as soon as one stored row is outside the Node shape.** A pattern-only
  allowlist is published as restricted with an empty `allow` (the same shape
  upstream uses for "restricted, allows nothing") rather than as unrestricted.
  The plain entries beside a pattern are not published on their own either: for
  `["gpt-4o", "*"]` — reachable by putting the glob on the key's plan, whose
  endpoint keeps pattern semantics — listing `gpt-4o` alone would say the key
  may call one model while the gate allows everything. The flat `models` field
  on the same response still carries the stored patterns verbatim.
- **A read failure is not "no restriction".** The listing used to fall back to
  an empty allowlist when the access table could not be read, and the mapping
  rendered that as `restricted: false` — while the request path fails *closed*
  on exactly that error. Every key is now reported restricted-with-nothing-allowed
  when the read fails, which is the direction upstream's own comment promises
  ("it can never turn a restricted key into an unrestricted one").
- **`access` reports the effective policy, not the key's own rows.** The gate
  unions a key's rows with the rows its plan grants, and the union can only
  widen. A plan-bound key therefore showed `restricted: true, allow: [gpt-4o]`
  while the gate still allowed everything the plan named, and a write through
  this endpoint cannot narrow a plan-bound key at all. Both surfaces now report
  the union. The flat `models` field deliberately stays the key's OWN rows: it
  is the field that endpoint writes, so folding the plan's rows into it would
  let a read-modify-write copy them onto the key.

An omitted `access` leaves the stored allowlist alone; a present one is
validated before the first write, so a refused body leaves the allowlist
exactly as it was. A non-string `allow` entry is a type error naming the field
rather than a body-wide "invalid JSON", so the client can tell what was wrong.

`.changes/516-key-access-dashboard-shape.md` was numbered for a different open
pull request (#516), where it would have competed for the same slot; the
fragment is `520-key-access-dashboard-shape.md`.

Evidence: `internal/handlers/dashboard/apikeys_access_test.go` and
`internal/integration/keys_access_test.go`. Each guard was removed in turn and
the corresponding test failed: the pattern rejection (six spellings refused, the
seed surviving each refusal), the fail-closed read (the table is dropped, and the
key must still read as restricted), the plan union (both surfaces report the
union while `models` keeps the own rows), and the subset rule above (with it
reverted to "empty only when nothing is expressible", a key whose plan carries a
glob reports `allow: [gpt-4o]` on both surfaces). The integration test drives the
real gate with the key under test rather than the suite's default credential, so
a 403 there is proof the refused writes did not land.
