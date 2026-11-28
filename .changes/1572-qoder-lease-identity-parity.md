### qoder: lease release + prompt/status parity (10605)

- Release a held queue lease after a queued run via `qoderQueueFinish`
  (`POST <origin>/algo/api/v2/service/ask/finish?Encode=1`, COSY-signed),
  best-effort with a 3s timeout; never blocks or fails the inference result.
- Tighten the readiness gate: `isQueued == false && serviceAvailable != false`,
  keeping the "still queued" wording in timeout errors.
- Port prompt transforms `qoderAsAgentSDK` and `qoderHarnessComma` into the
  system text before sending.
- Applies to both `qoder` and `qoder-cn` without cross-aliasing hosts or
  credentials (§3).

Source: magpie-community/plugins main commits de308730f (0.2.72),
1e4844b34 (0.2.73), 4c9f36bd1 (0.2.74), cbd075ff2 (0.2.75).
