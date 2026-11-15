### mcode-lease: forward authType through chat dispatch and open usage gate

A `minimax-code` / `minimax-code-global` connection created with `authType:
"mcode-lease"` was being rejected by the usage eligibility gate
(`isUsageEligibleConnection`) because only `oauth` and apikey-providers passed,
and lease detection in `ForwardMinimaxCode` never matched because `authType` is
a separate `connections` column, not a field in the `data` JSON blob, so
`mcodelease.IsLeaseConnection(req.ConnData)` always returned `false`.

Changes:

* Add `AuthType string` to `ConnectionData` (`internal/handlers/shared/types.go`)
  and populate `connData.AuthType = conn.AuthType` from the DB row in
  `getBestConnection` (`internal/handlers/chat/connections.go`).
* Add `AuthType string` to `executor.Request` and forward it on `execReq` and
  `retryReq` in `tryForwardWithConnection` (`internal/handlers/chat/fallback.go`).
* `ForwardMinimaxCode` now detects lease mode via `req.AuthType == "mcode-lease"`
  (new `mcodelease.IsLeaseAuthType` / `LeaseAuthType` helper), keeping
  `IsLeaseConnection(req.ConnData)` as a fallback for callers that populate the
  data blob.
* `isUsageEligibleConnection` now allows `c.AuthType == "mcode-lease"` for
  `minimax-code` / `minimax-code-global`, which are already in
  `usageSupportedProviders` (`internal/handlers/dashboard/connections.go`).
* Add `mcode-lease` and `apikey` cases for both minimax-code lanes to
  `TestIsUsageEligibleConnection`.

Upstream parity: port of the `mcode-lease` lease connection behaviour.
