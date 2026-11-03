### Added

- **Configurable stream stall timeout** (upstream #1703, #5276, #5296):
  - Support `STREAM_STALL_TIMEOUT_MS` and `NINEROUTER_STREAM_STALL_TIMEOUT_MS` environment variables to configure the global SSE stream stall watchdog.
  - Support optional per-provider `StallTimeoutMs` override in `ProviderConfig`.
  - Wire resolved stall timeout across all streaming executors and forwarders.
