### fix(chat): fail closed when the model-access policy cannot be read

A broken allowlist read used to degrade to "allow all". `effectiveAllowedModels`
and `allowedModelPatterns` both logged a warning and returned an empty pattern
list, which every caller reads as unrestricted — so the moment policy storage
was unavailable, every key became an unrestricted key. That is the opposite of
what the gate exists for: a policy that cannot be evaluated is not a policy that
permits.

Storage and repo failures now produce `ErrPolicyUnavailable`, which
`writeModelAccessError` maps to `503 policy_unavailable`. The pattern-list path
returns the `db.SentinelDenyAll` (`!*`) pattern instead, so even a caller that
reads only the list sees a denial rather than an empty allowlist.

The 403 wire shape also becomes structured: `Model not allowed for this API
key` with `code: model_not_allowed`, matching the envelope every endpoint
sharing the gate emits, instead of the typed error's own sentence. That is a
client-visible change for anyone matching on the old text; the status code is
unchanged.

Verified with `TestModelAccess_FailClosedSemantics` (closed database → 503,
nil repo → 503, sentinel on the pattern path) and by the full
`go test -tags=integration ./internal/integration/` suite, 79 s, which exercises
the denial on every media endpoint. The pre-fix state does not compile against
the new test — the test refers to `ErrPolicyUnavailable` — so the honest
statement is that the old behaviour is pinned by the closed-database case rather
than demonstrated by a red run.
