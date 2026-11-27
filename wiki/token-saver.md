---
title: Token Saver
order: 9
section: Using the Gateway
since: 1.9.14
---

# Token Saver

RTK is the feature that pays for the gateway: it compresses what your agent reads
before the model sees it, cutting 20-40% of input tokens. Three further savers
change how the model *writes*. All four are configurable per run and from the
dashboard.

## RTK — request-side compression

RTK rewrites oversized `tool_result` content into a shorter equivalent before it
reaches the model. It is on by default (`RTK_ENABLED=true`).

### What it protects

These are invariants, not tuning knobs — each one exists because compressing the
wrong thing breaks the agent:

| Guard | Rule |
|---|---|
| Error results | A failed command's output is **never** compressed — the agent comes back to read it again |
| Protected tools | Tool names matched case-insensitively (`read` = `Read`), the same list `tokensaver.protectedTools` uses |
| JSON documents | A JSON body is left intact rather than rewritten |
| Filter selection | The filter is chosen by the **command** that produced the output, not by its content |
| Truncation caps | `RAW_CAP = 10MB`; smart truncation only engages once output spans ≥ 250 lines |
| Line-numbered dumps | A `read`/`grep` numbered dump is truncated the way upstream truncates it |

### Levels

| Level | What it does |
|---|---|
| `off` | Nothing is compressed |
| RTK only | Tool-result compression |
| Stacked | RTK + one of the style savers below |

The dashboard's **Compression Analytics** page
(`/api/analytics/compression`) shows tokens saved, savings ratio, average
duration, and the mode breakdown per provider, so you can see what RTK actually
returned rather than guessing.

## Caveman — terse output style

Caveman adds an output directive that makes the model answer in short, blunt
sentences. Three levels (`light`, `medium`, `full`) carry progressively stricter
doctrine, matching upstream `open-sse/rtk/cavemanPrompts.js`. Enable with
`CAVEMAN_ENABLED=true` or `--caveman=true`.

## Ponytail — structured output style

Ponytail pushes the model toward a tighter, more uniform response shape. It also
has levels and is mutually exclusive with Caveman — you pick one style, not both.

## ADHD — scannable output style

The ADHD saver asks for neatly structured, bulleted, easy-to-scan answers. It is
the third style option, also exclusive with the other two, and it has its own
levels.

## REA allow — the reverse-engineering clause

`REA` here is the reverse-engineering *workflow* the clause points at
(https://github.com/morluto/rea), not a compressor. When `reaAllowEnabled` is on,
the gateway rewrites the request's system prompt on its way through: it removes
paragraphs matching a fixed signature set — the stock safety clauses some CLI
clients ship ("Refuse requests for destructive techniques", "Dual-use security
tools") — and appends one paragraph scoping the turn to authorized reverse
engineering and security research on software the user owns or is authorized to
examine, preferring structured REA workflows and citing evidence.

What it is not:

- **Not a provider setting.** It edits the prompt the gateway sends. The
  provider's own policy and its own refusals are untouched; a model may still
  decline. It is the mirror image of [guardrails](security.html), which inspect
  what leaves the gateway — this one expands what leaves it, so keep the guardrail
  audit trail on if the traffic matters.
- **Scoped to the system prompt.** Every system-prompt shape the translator
  knows is handled (Claude's `system`, Gemini/Vertex's `systemInstruction`,
  OpenAI's `system`/`developer` role, Kiro's own envelope), and an already-applied
  clause is detected so a second pass does not stack it. Conversation messages
  and tool schemas are never rewritten.
- **Exact-match stripping.** Only those signature paragraphs are removed, and
  only in whole paragraphs. A client that rewords them leaves its text in place —
  the clause is appended, nothing is mangled.
- **Off by default** (`reaAllowEnabled: false`); turning it on rewrites only
  requests that have a system prompt to rewrite.

## Testing a saver before enabling it

Two benches post a sample payload through the real compressor and show the
result, so you can see the saving and the risk before turning anything on:

```bash
curl -X POST http://localhost:20130/api/tokensaver/rtk/test \
  -H 'Authorization: Bearer sk-your-api-key' -d '{...}'

curl -X POST http://localhost:20130/api/tokensaver/caveman/test \
  -H 'Authorization: Bearer sk-your-api-key' -d '{...}'
```

## Runtime flags

```bash
./9router-go --rtk=false            # disable RTK for this run
./9router-go --caveman=true         # enable Caveman
./9router-go --ponytail=true        # enable Ponytail
./9router-go --adhd=true            # enable the ADHD style
./9router-go --no-injection-guard   # disable the injection guard
```

Dashboard settings win unless a flag is passed explicitly, so a flag is the way
to override a stored setting for one run.

## Where it lives

- `internal/tokensaver/rtk.go` — the compressor
- `internal/tokensaver/caveman.go` — the style savers
- `internal/tokensaver/prompts_custom.go` — your own prompt presets
- `internal/headroom/` — the token-compression proxy lifecycle
