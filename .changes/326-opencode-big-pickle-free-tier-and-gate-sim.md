### opencode-zen: big-pickle is free, so route it through the inference gate

`big-pickle` is billed at zero (`pricing.KnownFreeModels` already listed it) but
carries no `-free` suffix, and the routing catalog decided its tier separately.
It was left out of `openCodeFreeTierModels` on the reasoning that it had "no
live 403 evidence" while the lane table called it paid chat. Absence of
evidence is not evidence of a paid tier, and a free id without the usual suffix
is precisely the case that suffix matching cannot recognise — so this was the
one free model routed down the paid lane, where the gate refuses free ids.

- `big-pickle` is now in `openCodeFreeTierModels`, so a keyed `opencode-zen`
  (or `opencode-go`) request for it is forwarded to the keyless
  `/inference/v1` gate with `Bearer public`, exactly like every other free id.
- The pricing layer and the routing layer now agree. They already disagreed:
  `pricing.KnownFreeModels` billed it at zero while routing charged it as paid
  traffic.
- `TestForwardOpencodeZen_KeyedLaneStaysOnZenV1` no longer lists it as paid;
  `TestForwardOpencodeZen_FreeModelTakesInferenceGate` covers it, so the
  suffix-independent classification is pinned by an explicit case rather than
  left to follow from the `-free` ids.

### test(opencode): simulate the free-tier gate answering HTTP 200 for big-pickle

`internal/integration/opencode_zen_gate_simulation_test.go` adds a fake
`/inference/v1` upstream implementing the gate's contract — the 2.x User-Agent
shape, the four mirrored session headers, the rejected `x-opencode-request`, the
keyless `Bearer public` credential, mandatory streaming, and the verbatim CLI
2.x `read` definition with its `path` schema and `strict:false`. Anything less
answers 403 FreeTierError, or 500 for a duplicated tool name.

Driving it through the production router exercises lane choice, credential swap,
header fingerprinting and body shaping end to end:

- `TestBigPickle_IsFreeTier` — pins the classification the rest depends on,
  including that the providers which do not offer the id do not claim it.
- `TestBigPickle_GateAnswers200` — a keyed zen request for `big-pickle` reaches
  `/inference/v1/chat/completions` with the public credential and both exact
  fingerprint tools, and comes back 200.
- `TestBigPickle_ToolLessAnswers200` — a tool-less request is shaped and
  accepted.
- `TestBigPickle_RawBodyIsRefused` — the negative control: the unshaped client
  body is refused both on headers (v1 UA) and on the tool contract, proving the
  200s are earned rather than granted by a lenient fake.

The gate owns the body read instead of using the shared `NewUpstream` helper,
which drains `r.Body` into its own record first and would leave nothing to
validate. It also checks a tool definition in the shape actually received: the
Chat lane nests description and parameters under `function`, the flat
Responses/Messages lane keeps them at the top level.

Verified with `go build ./...`, `go test -count=1 ./internal/proxy/...
./internal/providers/ ./internal/pricing/ ./internal/handlers/chat/
./internal/translator/`, and
`go test -tags=integration -count=1 ./internal/integration/` (69s).

### Note on `7de81f7`

`7de81f7` pinned `big-pickle` as paid on every provider and warned that the
`-free` suffix rule must not sweep a bare paid id onto the inference gate. The
warning still holds — the suffix rule only reaches ids ending in `-free` — but
the classification it asserted does not: live keyless calls to
`/inference/v1/chat/completions` for `big-pickle` return 200 with a completion,
and that gate serves free-tier ids only. It is now classified by the curated map
rather than by suffix, and the provider-scoped assertions in
`model_formats_test.go` and `opencode_test.go` were updated to match the
observation.
