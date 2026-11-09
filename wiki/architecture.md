---
title: Architecture
order: 10
section: Under the Hood
since: 1.9.14
---

# Architecture

```text
┌─────────────┐
│  CLI tool   │  (Claude Code, Codex, OpenClaw, Cursor, Cline...)
└──────┬──────┘
       │ http://localhost:20130/v1
       ↓
┌─────────────────────────────────────────────┐
│         9router-go (Smart Router)           │
│  • RTK Token Saver (cut tool_result tokens) │
│  • Format translation (OpenAI ↔ Claude)     │
│  • Quota tracking                           │
│  • Auto token refresh                       │
└──────┬──────────────────────────────────────┘
       │
       ├─→ [Tier 1: SUBSCRIPTION] Claude Code, Codex, GitHub Copilot
       │   ↓ quota exhausted
       ├─→ [Tier 2: CHEAP] GLM ($0.6/1M), MiniMax ($0.2/1M)
       │   ↓ budget limit
       └─→ [Tier 3: FREE] Kiro, OpenCode Free, Vertex ($300 credits)
```

## RTK Token Saver

RTK compresses `tool_result` content before it reaches the model, saving 20-40%
of tokens. The guards it keeps:

- A failed command's output is **never** compressed — the agent comes back to
  read it again.
- Tool names are matched case-insensitively (`read` = `Read`), the same rule
  `tokensaver.protectedTools` uses.
- A JSON document is left intact.
- The filter is chosen by the **command** that produced the output, not by its
  content.
- Truncation follows upstream's caps (`RAW_CAP = 10MB`, smart truncation only
  engages once output spans at least 250 lines).

## Combo strategies

Each combo carries its own strategy:

| Strategy | Behaviour |
|---|---|
| Fallback | Try candidates in order until one succeeds |
| Round-robin | Rotate across candidates |
| Sticky | Hold the same candidate while it succeeds |
| Capacity | Reorder capability-aware |
| Fusion | Combine several models into one panel |

## Design decisions that matter

- **Concurrency slots are per attempt.** In a combo/fallback, one request can try
  several providers. A slot must be released when that attempt ends (success,
  failure, or failover), not once at the end of the request — otherwise slots
  leak permanently and the node locks itself out.
- **A congestion violation writes no cooldown.** One burst over a limit must not
  globally park a healthy node. Only a debug trace is written.
- **One source of truth for policy.** Rate limits, budgets, and the model gate
  all read the same resolver — no limit logic scattered around.

## Further reading

Full internals: [`ARCHITECTURE.md`](https://github.com/luqman-v1/9router-go/blob/main/ARCHITECTURE.md).
