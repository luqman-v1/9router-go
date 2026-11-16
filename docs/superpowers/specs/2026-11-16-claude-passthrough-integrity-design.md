# Claude passthrough integrity — design

Date: 2026-11-16
Status: draft for approval

## Why

Every Claude Code CLI release breaks the `/v1/messages` lane again, and each breakage
is invisible until a user reports it. On 2026-11-16, 103 of 1853 requests failed:
69 because the CLI started sending `tool_addition` blocks behind a beta the gateway
never forwarded, 18 caused by two half-measures written while diagnosing, and the
rest from my own probes.

The pattern is the same every time: **the gateway decides what the API accepts by
keeping allowlists, and everything outside a list is dropped or refused in silence.**
A block type the CLI added last week is "not content", so the turn is deleted. A
message key nobody knew about is stripped, so a directive never arrives. A beta the
gateway does not list is never sent, so the API refuses the whole turn — and because
the turn stays in the history, every later turn fails too.

## Goals

1. An unknown block type, message key, or beta flag must not make the gateway drop
   or corrupt a request it could otherwise have passed through.
2. When the gateway *does* have to remove something, it says so in the log, with the
   name, so the next CLI change is visible without a user report.
3. A regression corpus built from real CLI payloads catches a future CLI change in
   tests, before it reaches a user.
4. The OAuth cloak never rewrites the tool names of a client that is already Claude
   Code.

## Non-goals

- Implementing any Claude Code feature in the gateway. It forwards; it does not adopt.
- Changing cloaking for non-Claude-Code clients.
- Touching non-Claude lanes (OpenAI chat, Responses, Gemini, commandcode).
- Making the gateway validate request bodies against the Anthropic schema.

## Anti-goals (this work has failed if)

- A request fails for a block the gateway could have forwarded unchanged.
- A request succeeds because the gateway silently removed a block the model needed.
- Every CLI release still needs a hand-edited list in `providers.go`.
- New noise: a log line for every request.

## Decisions

These are the choices taken during the design conversation. Each is a rule, not an
implementation detail.

**D1 — Passthrough by default, deny what is known bad.**
Content-block and message-key handling invert from allowlist to denylist. A block or
key the gateway does not recognise is forwarded; only names known to be wrong are
removed. The user chose this explicitly: a loud failure from the API is better than a
silent deletion, because the log then tells us what to add.

**D2 — "Empty" means provably empty.**
A message is contentless only when it carries no blocks at all, or when every block
it carries is one of the known-empty shapes (blank text, unsigned thinking, an empty
tool_result). An unknown block type is content by definition — the gateway cannot
know it is worthless.

**D3 — The client's beta header is authoritative.**
The registry list is a floor that keeps older clients working; the caller's own
`anthropic-beta` is merged on top, on every lane that reaches a Messages endpoint
(`/v1/messages` carries it today; the Chat Completions lane already did). New betas
then need no gateway release.

**D4 — The gateway never invents or repairs a tool call.**
A tool call with no arguments stays a tool call with no arguments. The gateway does
not fabricate `{}`, does not retry upstream, and does not rewrite history to work
around a weak model — it forwards, and the client decides.

**D5 — Cloaking is for clients that are not Claude Code.**
A client declaring three or more of Claude Code's own tool names is Claude Code;
its tools keep their names and no decoys are injected. A decoy that collides with a
declared tool is skipped in any case.

**D7 — The stalled-tool-loop guard never replaces a turn.**
Today `CheckStalledToolLoopOpenAI` / `CheckStalledToolLoopClaude` answer the client
with a canned intervention instead of forwarding to the model, so a turn dies. It
killed a design-review subagent that had read one file three times, and it was the
reason the earlier "intervene on a bare tool call" change (PR #1204, closed) could not
help. The guard becomes observability only: the detector still runs and logs, and the
turn is forwarded untouched. The user chose this over keeping the block or raising the
threshold.

**D6 — Sanitizers only run where the wire format requires them.**
Order on the `/v1/messages` lane: cloak decision → sanitize (empty turns, foreign
keys, tool results for dropped ids) → beta merge → upstream → decloak. The converted
(OpenAI) lane applies the same content and key rules through one shared function, so
the two lanes cannot drift.

**D8 — The drop line is debug-level.**
The sanitizer's line stays at `log.Debug` because every shape it now removes is routine
(`caller` on Claude Code traffic, blank text, empty turns); raising it would log on most
requests and break the no-new-noise anti-goal. A shape the gateway does not recognise is
forwarded and shows up as a provider 400 in `requestDetails`, which is the loud signal;
the debug line is for post-incident diagnosis with `LOG_LEVEL=debug`.

## Constraints

- Go 1.27, `encoding/json/v2` with deterministic marshal, existing package boundaries
  (`internal/translator`, `internal/providers`, `internal/handlers/chat`,
  `internal/proxy/executor`).
- A body with nothing to fix is forwarded **byte for byte**; every sanitizer already
  reports a `changed` flag and the callers depend on it.
- Registry beta flags are shared map state; request-scoped edits must copy.
- No new dependency.

## The regression corpus

Recorded from Claude Code 2.1.325 against a real gateway, redacted (tool results and
user text replaced, structure kept):

- one `/v1/messages` turn carrying `tool_addition` and `tool_removal` blocks;
- one carrying a system turn mid-history;
- one with `output_config` on a message;
- one with an unknown block type (`future_block`) and an unknown message key;
- one Claude Code tool set (Task/TaskUpdate/Bash/Read/Edit/Write) for the cloak rule.

The corpus lives as table-driven Go fixtures next to each sanitizer, not as files, so
the existing test commands cover it. Each case asserts the surviving structure, not
byte identity.

## What is left to the builder

- The exact denylist contents for blocks and keys, drawn from what the lanes actually
  produce today (`reasoning_content`, `output_config`, `tool_calls`, `function_call`,
  `reasoning`).
- Where the single shared function lives and how both lanes call it.
- Log level and wording for the "removed something unknown" line (one line, rate
  limited by occurrence, not per request).
- Whether the beta floor shrinks once client forwarding is proven on both lanes.
- What the guard logs (tool name, call count, session) and where that log lives.

## Acceptance

- The corpus passes, and passes with the gateway *forwarding* every unknown name.
- `go test ./internal/translator/ ./internal/providers/ ./internal/handlers/chat/`
  green, including the existing suites that pin byte-for-byte passthrough.
- A live smoke on the local gateway: a `/v1/messages` turn with a `tool_addition`
  block reaches the API, and a mid-history system turn survives as system text.
- No new log line for a clean request.
- A stalled-tool-loop detection produces one log line and the turn still reaches the
  model: the subagent that read one file three times completes instead of dying.
