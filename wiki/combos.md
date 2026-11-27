---
title: Combos
order: 7
section: Using the Gateway
since: 1.9.14
---

# Combos

A combo is a named chain of models the router walks when a request arrives. It is
the mechanism behind "never stop coding": the first candidate is your
subscription, the next is cheap, the last is free.

## Strategies

| Strategy | Behaviour |
|---|---|
| Inherit global | Use the gateway-wide **Combo Routing Mode** (Settings → *Combo Routing Mode*: *First Model* or *Round Robin*); a combo with no value at all falls back to *Fallback* |
| Fallback | Try candidates in order until one succeeds |
| Round-robin | Rotate across candidates |
| Sticky | Hold the same candidate while it succeeds |
| Capacity | Reorder capability-aware — a model that cannot serve the request is skipped rather than tried |
| Smart Router | Order by task suitability (System One evaluation) with session stickiness |
| Fusion | Combine several models into one panel |
| Tokenmaxxing | Order by remaining quota and reset time, so the bucket that frees up soonest is spent first |
| Cascade | Cheapest unit price first |

Stored order is a strategy of its own: the order you arrange in the editor is
walked as-is, and the three dynamic strategies (`smart`, `tokenmaxxing`,
`cascade`) are exempt from it because honouring a fixed order would defeat the
ranking they exist to compute.

The connection-level strategies — failover, round-robin, sticky round-robin and
**least busy** — live in [Settings](settings.html) under *Default Routing
Strategy* and decide **which connection of one provider** serves a request; they
are not combo strategies. Least busy prefers the connection carrying the fewest
turns still in flight, so a long streaming reply keeps its key out of the next
pick.

## Creating a combo

Dashboard → Combo & Vision Adapter → Create.

Two shortcuts build a chain for you:

- **Auto-free** (`POST /api/combos/auto-free`) — assembles a chain from the free
  tiers you have connected.
- **Auto-family** (`POST /api/combos/auto-family`) — builds one chain per model
  family, so `claude`, `gpt`, and `gemini` each get their own fallback.

**Presets** (`GET /api/combos/presets`) offer the Cursor and Claude default
lists. Every preset is *derived*, never hard-coded: the Claude items are whatever
the `cc` registry row currently holds plus the Claude Code aliases, so adding a
model to the provider registry adds a preset with no second edit.

## Smart combos

Smart combos keep a combo's membership in step with the models the gateway
actually knows about. Instead of listing model ids by hand, you write **rules**
and the router maintains the list.

### The two ideas a rule separates

| Notion | Meaning |
|---|---|
| **Addable** | A model a rule may put *into* the combo. Gated by reachability (a provider with a connection, a node that still exists) and by kind. |
| **Live** | A model that still *exists*. A member that is no longer live is pruned whatever the pattern says. |

A provider that loses its connection keeps its members — they are live, just
unreachable — but stops contributing new ones. Absence from a catalogue is weak
evidence of death, so a member is never pruned for being unreachable.

### Rule shape

| Field | Meaning |
|---|---|
| `pattern` | Glob, keyword, or regex (`re:` prefix required for regex) |
| `mode` | `glob` (default), `keyword`, `regex` |
| `effect` | `include` or `exclude` — an exclude rule always wins |
| `maxMembers` | How many models one rule may contribute. Default 20. `0` = unlimited, an explicit choice. |
| `allKinds` | Match non-LLM kinds too. Off by default: embedding, image, TTS and STT models cannot serve a chat turn. |
| `enabled` | Rule on/off |

Rules match against both spellings of a model — `alias/model` and bare `model` —
the same way the per-key allowlist does. A second qualified spelling (`AltID`)
means a member is never pruned for being spelled the other way round, and the
membership it adds still lands under one id, so a rule cannot create two slots
for one model.

### What survives a reconcile

- **Excluded ids** — a model you removed by hand is recorded in the rule's
  `excluded` list, so a later reconcile never re-adds it. A manual removal is
  permanent.
- **Managed members** — the picker never offers a model that is already a member,
  and the rules editor labels which members a rule owns.

**Known limitation:** a model id you pinned by hand *and* a rule claims is owned
by the rule, so editing the pattern removes it. The dashboard prevents the common
path, but the API cannot tell a pin from a claim when both spell the same id.

### Working with rules

```text
POST /api/combos/reconcile               Apply every combo's rules
POST /api/combos/{id}/smart/preview      Dry-run one combo — nothing is written
POST /api/combos/{id}/smart/load         Apply one combo's rules
```

A preview reports what would change before anything is committed:

```json
{
  "models":  ["ag/gemini-3-flash", "oc/free-model"],
  "added":   ["oc/free-model"],
  "removed": [],
  "rules":   [{ "id": "r1", "matched": 41, "applied": 20, "capped": true }]
}
```

`matched` is what the pattern selected; `applied` is what actually landed after
the cap; `capped` tells you the cap bit.

## Disabled members and pruning

Disabling a model or a provider connection preserves the combo's saved config
and skips it at runtime — re-enabling resumes routing without editing the
combo. The check runs once per dispatch (one snapshot of disabled models,
provider active status, and node prefixes), not per model in the chain. If
every member is disabled or inactive, fallback combos are still evaluated
before a `503` comes back.

Deleting a custom model or a provider's models prunes every combo that
referenced them, in one transaction — plain-string and weighted
(`{"model": ..., "weight": ...}`) entries both. Deleting a provider node
prunes atomically with the node itself. A combo never keeps a dead model
around to fail on it.

## Member attempt cap
A combo member owns every connection its provider has, and the fallback loops
used to walk all of them — up to ten — before moving on. When a lane's whole
pool is down, that is ten dead upstream calls the client waits through before
the chain advances.
**Combo Member Attempt Cap** in Settings bounds it: how many failed accounts one
member may spend before the combo moves to its next model. `0` is the default and
means **off**, so an upgrade never changes fallback behaviour on its own; `3` is
the value the dashboard suggests once you turn it on. Anything above `10` is
clamped, because ten is the most the member loop will ever try.
The setting resolves like the rest of the per-provider policy: a provider entry
that sets its own `comboMemberAttemptCap` wins — **including an explicit `0`,
which switches the cap off for that provider even when a global cap exists** —
then the global value, then off. A settings read that fails resolves to *off*: a
member never loses a working fallback because bookkeeping broke.

## Timeout and grace period

Set **Model response timeout** per combo in the Edit dialog (seconds, decimals
allowed; empty or `0` disables). The timer covers each candidate except the last.
When it fires, the candidate is cancelled and the next is tried; the timed-out
model is then left out of that combo for a grace period — default 5 minutes,
`0` re-includes it immediately — before rejoining the chain. If every model is
inside its grace period, the full chain is used. The pause is in-memory and
resets on restart.

## Where it lives

- `internal/handlers/chat/combo.go` — dispatch, failover, and the two combo loops
- `internal/combos/smart/smart.go` — the rule resolver (pure, no I/O)
- `internal/combopresets/presets.go` — the derived Cursor/Claude presets
- `web/src/components/combos/` — the dashboard UI
