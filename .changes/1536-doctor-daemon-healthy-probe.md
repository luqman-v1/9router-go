### Fixed

- **CLI doctor probes and flags**: `checkDaemon` now uses the `healthyAt` probe seam consistently, `doctor --port 0` fails config validation instead of falling back to `cfg.Port`, and `checkDatabase` rejects directory paths instead of opening them as SQLite files.
