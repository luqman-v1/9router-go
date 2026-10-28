### Combos: the response timeout no longer cuts off the last candidate actually tried

- **`firstTokenTimeoutMs` now spares the last member a combo pass will really
  send a request for, not the last member stored.** The guard was index-based
  (`i == len(models)-1`), but the loop `continue`s past an entry that does not
  resolve and past a provider whose model is locked, and `withoutBenchedModels`
  can shorten the list before the loop starts. When the final entry was skipped
  for either reason, the timer stayed armed on the last member that could still
  answer: a three-member combo whose third member was model-locked returned
  `504 model response timeout` while the same combo with the timeout disabled
  answered `200` from the second member. Both the chat loop and the Messages
  loop now compute the last index that resolves and is available before the
  pass, and the zero-content probe and stream peeker use it too, so they share
  the same blind spot fix.
- The setting still only *advances* a combo, never makes one fail sooner
  (#744, upstream `decolua/9router#5214`): a candidate that will be tried last
  keeps the gateway's normal deadline.
- Verified: `TestComboTimeoutSparesLastTriedCandidate` (three members, third
  model-locked, second slow but healthy) fails on the pre-fix tree with
  `status = 504, want 200` and passes after; mutating `lastTriedComboIndex` back
  to `len(models)-1` makes it fail again, so the guard is not decoration.
  `go test ./internal/handlers/chat`, `go test -race -run TestCombo`,
  `go test -tags=integration ./internal/integration/...` and the diff-scoped
  `golangci-lint` all pass.
