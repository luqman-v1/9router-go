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
| Fallback | Try candidates in order until one succeeds |
| Round-robin | Rotate across candidates |
| Sticky | Hold the same candidate while it succeeds |
| Capacity | Reorder capability-aware — a model that cannot serve the request is skipped rather than tried |
| Fusion | Combine several models into one panel |

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
