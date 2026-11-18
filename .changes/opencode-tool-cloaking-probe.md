### test(opencode): a runnable probe for the tool-cloaking strategy, and why no strategy changed

`9router-go` shapes free-tier requests by replacing caller tools with the CLI's
built-in declarations and instructing the model to call them through
`execute({"code": "CALL_TOOL <name> <json>"})`, which the response side rewrites
back. The reference implementation instead declares caller tools natively under
an opaque `mcp_<12-hex-sha256>` name. The two projects measured opposite
results, and **the difference was not resolved here — this PR adds the
instrument, not a strategy change.**

What was measured on 2026-11-18, trying to settle it:

- `/inference/anthropic/v1/messages?beta=true` and `/inference/v1/messages` both
  **route** — each answers `429`, while bogus paths under the same origin answer
  `404`/`405`, so the refusals are route-specific rather than catch-alls.
- **Body validation runs after the quota check.** A missing `model` returns
  `400 "model must be a non-empty string"` first, but a foreign tool name
  returns the *same* `429 FreeUsageLimitError` as a tool-less body. So from an
  egress whose free allowance is spent, every tool strategy is
  indistinguishable from every other.
- The probe egress's allowance was spent (`429` on `glm-5.3-flash-free`), and
  `deepseek-v4-flash-free` answered `400 "Model is unavailable"` for all three
  bodies including the tool-less control.

**`TestLiveOpenCode_ToolCloakingProbe`** (`internal/integration/`, `integration`
tag, opt-in via the existing `9ROUTER_LIVE_OPENCODE=1`) runs the matrix —
`no_tools` control, `raw` declaration, `mcp_12hex` — against two models and
judges each model against its own control. It reports rather than asserts,
because a closed tier is a fact about upstream. Its verdict vocabulary is
deliberately narrow: `accepted` requires a real `200` with completion frames, and
`quota-wall` is called out as *saying nothing* about the body. The first version
of this probe collapsed "non-200" into "accepted" and produced a confident,
wrong conclusion; that is the failure the stricter classification exists to
prevent, and it is why the reporter is per-model and control-relative.

**No cloaking strategy changed**, and `README`-level behaviour is untouched. The
audit's §2.4 question stays open with a runnable way to answer it, which is the
honest state: switching on evidence we could not gather would have replaced a
working mechanism with a guess. Switching requires a clean egress, and the run
order is spelled out in the test's doc comment.

**A related gap is recorded, not silently fixed.** `NewOpenCodeExecuteBridgeWriter`
is a Chat-chunk writer: it decodes `choices[].delta.tool_calls[]` and emits
`chat.completion.chunk`. It is therefore not wired on `ForwardOpencode`'s native
Responses path or on `forwardZenMessages`, and simply adding it there would
parse nothing — those lanes emit `response.output_item.added` /
`input_json_delta` events, not `choices[]`. A Responses and a Claude variant
would each be needed, or one rewrite placed after translation (the layer the
non-streaming `bridgeOpenCodeExecuteToolCallsJSON` already proves works). That is
a meaningful piece of work whose shape depends on the probe's answer, so it is
deferred with the reason rather than half-built here.

Verified: `go vet -tags=integration ./internal/integration/` clean;
`go test -tags=integration ./internal/integration/` green (the probe skips
without the env var, so CI is unaffected); the probe run against the live gate
produced the measurements quoted above rather than an unsupported verdict.
