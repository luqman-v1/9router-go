### Added

- **`9router-go doctor`**: diagnoses a gateway that will not answer, instead of
  only reporting that it is not running. `status` says whether a daemon is
  alive; `doctor` says why it is not, checking the resolved port, the pid claim,
  whether the listening socket belongs to the gateway or a foreign process, the
  data directory's writability, the database file, and a failed-bind marker in
  the run log. Each failing line names the command that fixes it on that
  platform (`netstat`/`taskkill` on Windows, `lsof`/`kill` elsewhere), and the
  process exits non-zero so it works as a pre-flight gate. `--port` diagnoses an
  address other than the one `PORT` resolves.

### Fixed

- **`daemon.Healthy` probed the dashboard root, not `/health`**: a base URL
  reached `/`, which any static server answers 200 and the gateway answers 404.
  The health check now appends `HealthPath` itself, so every caller probes the
  address `waitHealthy` polls and a foreign listener is not mistaken for the
  gateway.
