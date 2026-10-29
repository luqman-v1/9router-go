### Combos: model response timeout and grace period

- **Per-combo Model response timeout** (`comboStrategies[name].firstTokenTimeoutMs`,
  disabled by default; edited in seconds in the combo Edit dialog or on the
  combo card). Each candidate except the last is bounded from connection
  selection through response headers to its first usable content, on
  `/v1/chat/completions` and Messages/Responses combo routing, streaming and
  non-streaming. A timed-out candidate is cancelled without cooling its provider
  account, and the next candidate is tried. A stream that has started is never
  cut off. Fusion panel timing is unchanged. Resolves #744.
- **The last candidate keeps the gateway's normal deadline**, as specified in
  #744 and the upstream proposal (decolua/9router#5214): the setting only
  advances a combo, it never makes one fail sooner than it would without it.
- **Replaces the #745 first-token timer.** #745 armed a second, streaming-only
  timer on the same `firstTokenTimeoutMs` key; it is removed so one timeout
  governs every endpoint and mode. The undocumented `firstTokenTimeout` alias
  #745 also read is no longer parsed; the dashboard never wrote it.
- **Grace period after a timeout** (`firstTokenGraceMs`, default 5 minutes, `0`
  re-includes the model on the next request): a model that timed out is left out
  of that combo's chain until the period ends. Held in memory, reset on restart;
  if every member is benched the full chain is used.
- **Committed in-band errors still fail over and lock the connection.** An
  earlier revision returned as soon as the response was committed, which skipped
  the lock and let the next request hit the same sick upstream.
  `TestComboSkipsInbandErrorOnRetry` now runs with the timeout off and on.
- Fixed detection of translated Messages text when an empty
  `content_block_start` and a text delta arrive in one write.
- Verified: `go test ./internal/db ./internal/proxy ./internal/handlers/chat`,
  `go test -tags=integration ./internal/integration/...`, combo type tests,
  `make vet-svelte`, and a live gateway smoke run against stalled and fast mock
  upstreams.
