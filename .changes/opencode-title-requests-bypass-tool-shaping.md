### fix(opencode): stop shaping title-generation requests like agent turns

OpenCode 1.x/2.x fires tool-less title-generation calls — one system
instruction asking for a title plus the conversation text. `9router-go` shaped
them exactly like an agent turn: the 12 built-in tool declarations were
installed, the caller's tools (none) were described, and the `execute` /
`CALL_TOOL` note was appended. The free-tier gate accepts a tool-less title
call and refuses that same call once agent tool declarations are attached, so
the shaping itself was causing the refusal — and the router then spent an entire
proxy-pool walk retrying a request that should never have carried tools.

- **`IsOpenCodeTitleRequest`** (`internal/translator/opencode_title.go`) detects
  the shape: at most three turns, no tool declaration in any spelling
  (`tools` / `functions` / `tool_choice` / `tool_config`, with an empty array
  counting as tool-less), no assistant or tool turn, **and** a known title
  opener. The opener requirement is what makes this safe: a short tool-less turn
  is a perfectly ordinary request, so its failure mode has to be "behaves
  exactly as today" rather than "a real turn silently loses its tools". Case is
  folded, and both the Chat/Claude `messages` carrier and the Responses `input`
  carrier are read.
- **`ShapeOpenCodeV2TitleRequest`** keeps the harness preamble — the gate
  fingerprints it, and a request without it is refused on every lane — while
  installing no tools and appending a title-specific note instead of the Code
  Mode instructions. Without that note the preamble tells the model to call
  tools "listed below" when there are none.
- **`shapeOpenCodeFreeBody`** (`internal/proxy/executor/opencode_gate_shape.go`)
  is the single decision point, and all six free-lane call sites now go through
  it — both `ForwardOpencode` lanes, `forwardOpencodeGoFree`, and the three zen
  lanes via `zenShapedBody`. Routing them through one helper is deliberate: a
  site left on `ConcealOpenCodeV2FingerprintTools` would keep passing the
  translator tests while breaking the behaviour they exist for, so the wiring
  has its own test.
- **Dead `zenFreePrefix` removed.** It was unreferenced and a second source of
  truth for the inference prefix. Its claim now lives where the prefix is
  actually decided.

**The Messages beta route is measured but not adopted.** The audit asks for
`/inference/anthropic/v1/messages?beta=true` plus
`anthropic-beta: interleaved-thinking-2025-05-14`. A probe of the live gate on
2026-11-18 established that the route **exists**: it answers `429
FreeUsageLimitError`, while a bogus path under the same origin answers
`404`/`405`, so the refusal is route-specific rather than a catch-all. What the
probe could **not** establish is a `200` — the probe egress's free allowance was
spent, and body validation does not run before the quota check (a missing
`model` returns `400` first, but a foreign tool name returns the same `429` as a
tool-less body). Switching a working lane from `/inference/v1/messages` — which
also routes — onto an unverified path would trade a known-good route for a
guess, so the current prefix stands and the measurement is recorded beside it in
`opencode_zen.go` for the next clean-egress run.

Verified: `go test ./internal/proxy/... ./internal/translator/...` green (the
three `internal/proxy/executor/mcodelease` failures are pre-existing on `main`),
`go build ./...` clean. Tests cover 17 detection cases including all four
false-positive guards, the shaping contract (preamble kept, tools not installed,
caller tools untouched), the two shapers being non-interchangeable, and the
six-site wiring.
