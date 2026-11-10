### OpenCode free tier: stop amplifying IP penalties, widen small output budgets, report usage

Four defects on the keyless `opencode` free lane, each measured against the live
gate and cross-checked with the findings in
[`azrinaldi/opencode-free-bridge`](https://github.com/azrinaldi/opencode-free-bridge).
That project is a sibling shim, not this repo's upstream — the port is of the
*findings*, not the code.

**An egress flagged for abuse is no longer retried.** The gate answers one
`403 FreeTierError` body for at least five unrelated causes, and only the
egress-level abuse flag is made worse by a replay: no header, session or body
change clears it. `isOpenCodeTerminalRefusal` now matches the three abuse
phrasings (case-insensitively, without allocating — the body is capped at 1 MiB
and this runs on a 1 vCPU box) and short-circuits before the retry sleep and the
direct replay. A failed replay also arms a 30s cooldown that suppresses the
*replay* only; the first attempt of every request still runs, and session
rotation on a 429 is unaffected.

**A spent daily allowance now parks its egress for the window upstream names.**
`Retry-After` was dropped in three places: the refusal lift discarded
`resp.Header`, both free-lane error sites built the error without it, and
`opencodeClosureError` then overwrote whatever survived with a hardcoded 60s (or
30s). The header now travels end to end and the classifier prefers it over its own
estimate, clamped by the existing `maxResetCooldown` ceiling. `/v1/responses` also
forwards it now, which it previously did not.

**A small `max_tokens` no longer returns an empty answer.** Every free OpenCode id
reasons, and a reasoning model spends the same output ceiling on its
chain-of-thought as on the visible text — so `max_tokens: 50` came back as `200`
with empty content and `finish_reason: "length"`. `output_budget.go` adds a floor
driven by `GetCapabilitiesForModel`: 5120 (1024 answer + 4096 thinking) for a
reasoning model, 512 otherwise, capped at the model's declared `MaxOutput`. It is
a floor, not an addition, so a caller's deliberate 128k budget is left exactly as
written. Applied to the free chat lane, the go-free lane, and — replacing the
hardcoded 512 constant — the muse-spark responses lane, so one upstream no longer
has two different floors.

**Token counts reach the dashboard on non-streaming calls.** The free lanes force
`stream: true` and re-aggregate for non-streaming callers, but never asked for
`stream_options.include_usage`, so the body carried no `usage` at all and the
ledger billed the turn at zero. Setting the flag alone would have fixed nothing:
`sseToOpenAIJSON` read `usage` *inside* its `len(choices) == 0 { continue }`
guard, and the usage-bearing chunk upstream appends has an empty `choices` array.
Both halves are fixed; a caller that set `stream_options` itself keeps its own
setting.

Verified with 14 new table-driven tests across
`opencode_retry_test.go`, `output_budget_test.go` and `opencode_closure_test.go`,
the existing opencode gate-simulation and free-tier closure integration suites,
and `go test ./...` (four unrelated macOS-environmental packages fail
identically on a clean tree: `browserlogin`, `daemon`, `proc`, `mcodelease`).

Known gap, deliberately out of scope: upstream's real free-tier reset is ~21.5h,
which the 2h `maxResetCooldown` still clamps. Raising that ceiling is
cross-provider policy, not an OpenCode fix.
