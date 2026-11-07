### feat(kimi): normalize reasoning_effort so Kimi's backend doesn't 400

A client-supplied `reasoning_effort` is normalized onto the values Kimi's
backend accepts before the request is forwarded (upstream decolua/9router#2427):

- `auto` → `high`
- `minimal` → `low`
- `xhigh` → `max`
- `low`/`medium`/`high`/`max` pass through
- `none`/`off` are dropped

`thinking.ToKimiReasoningEffort` in `internal/thinking/levels.go` maps the
intent onto Kimi's enum, and `internal/proxy/executor/kimi.go` applies it in
`ForwardKimi` and `forwardKimiResponses` when the incoming body carries
`reasoning_effort`. Unit tests cover the mapping table and the executor
normalization.
