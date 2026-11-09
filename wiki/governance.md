---
title: Governance
order: 16
section: Using the Gateway
since: 1.9.14
---

# Governance

Controls that decide *who may spend what*. Rate limits, budgets, plans, and
per-key model access all read one resolver, so there is a single source of truth
rather than limit logic scattered across the codebase.

## The resolver

`internal/policy/policy.go` resolves an **effective policy** for a key:

1. Start from zero.
2. Overlay the key's plan, if it has one and the plan is found.
3. Overlay the key's own columns where they are non-zero.

Everything downstream — rate limits, budgets, the model gate — reads that result.
A key with no plan and no overrides behaves exactly as it did before plans
existed.

## Rate limits

Per-key limits with a FIFO queue:

| Limit | Unit |
|---|---|
| RPM | requests per minute |
| TPM | tokens per minute |
| Concurrency | parallel requests |
| Queue timeout | how long a request may wait for a slot |

Prompt tokens are charged up front as an estimate, then reconciled when the real
completion tokens arrive — so one very long response correctly reduces the
running minute's budget after it finishes.

**Per-provider limits are separate.** A key limit answers "how much may this
caller spend"; a provider limit answers "how much can this endpoint take". Key
limits run first, in middleware; provider limits run after resolution. A caller
who exceeded their own limit is rejected *without* consuming provider quota —
one abusive caller must not spend a healthy node's budget.

## Budgets

USD and token budgets with a period and a hard cutoff:

| Field | Meaning |
|---|---|
| `limitMicros` | USD cap, stored as integer micro-dollars — never a float |
| `limitTokens` | Token cap |
| `period` | `daily`, `weekly`, `monthly`, or `total` |
| `alertPct` | Percentage that raises a warning header |
| `hardCutoff` | `1` = reject with `402` once the limit is reached; `0` = warn only |

Money is stored as integer micros; the float→micros conversion happens in **one**
helper so it is never mixed into a guard. In-flight requests are reserved, so N
parallel requests cannot all slip under the limit.

## Plans

A plan is a reusable template: rate limits, budget, `allowedModels`, and the
period. Assign it to many keys instead of configuring each one.

```text
GET    /api/plans
POST   /api/plans
GET    /api/plans/{id}
PUT    /api/plans/{id}
DELETE /api/plans/{id}      → 409 while any key still inherits it
GET    /api/plans/{id}/keys
```

Validation: `period` must be one of the four, `alertPct` 0–100, every limit ≥ 0,
and `allowedModels` may use a trailing `*`.

**A plan still attached to a key cannot be deleted.** The gateway answers `409`
rather than removing the plan and leaving those keys with no limits at all — a
key with no plan and no overrides is unlimited, so deleting a plan in use would
silently lift the ceiling on every key that had one. Detach the keys first.

## Which limits a key actually gets

Three layers, applied in order — this is the resolver above, stated as rules:

1. Start from zero — no limits.
2. Apply the key's plan, if it has one.
3. Apply the key's own settings, where they are non-zero.

So a key's own value **overrides** its plan. A key with neither behaves exactly as
it did before plans existed.

**Model access is the exception.** A key's allowlist and its plan's are
**unioned** — a key may add models, never remove them. A plan sets the floor; a
key can only widen it.

## Per-key model access

An allowlist per key. Empty means "all models", which is backward-compatible.

- Wildcards: `claude-*`, `gpt-4*`
- The gate runs before dispatch, so a denied model never reaches a provider.
- How a key's list and its plan's list combine — and why a key can add but never
  remove — is covered under [Plans](#which-limits-a-key-actually-gets) above.

## API key lifecycle

```text
POST /api/keys/{id}/rotate       Rotate: the old key stops working
PUT  /api/keys/{id}/toggle       Enable / disable
POST /api/keys/{id}/reset-quota  Lift a spent quota without deleting history
PUT  /api/keys/{id}              Metadata, expiry, limits
```

Keys are stored as Argon2id verifiers with a SHA-256 lookup index, and the
plaintext is shown exactly once at creation. See [Security](security.html) for the
hashing and vault details.

## Where the numbers come from

The dashboard's rate-limit and quota numbers are read from the **same limiter
instance the data plane enforces with**, not a private view. That is why a window
shown in the dashboard matches what a request actually hits.

## Further reading

- Rate limiter: `internal/middleware/ratelimit.go`
- Policy resolver: `internal/policy/policy.go`
- Budget enforcement: `internal/middleware/budget.go`
- Model gate: `internal/handlers/chat/access.go`
- Plans storage: `internal/db/plans.go`
