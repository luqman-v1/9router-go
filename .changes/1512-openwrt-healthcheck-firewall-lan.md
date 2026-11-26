### feat(openwrt): healthcheck probe, LAN firewall rule, and settings UI

- Wire the once-a-minute `/health` probe into UCI (`healthcheck`,
  `healthcheck_threshold`, `healthcheck_timeout`), procd and the cron table. The
  probe restarts the gateway after a run of consecutive failures, covering the
  wedged-listener case procd respawn does not.
- Add `open_lan` plus an idempotent `uci-firewall` rule. The rule is created on
  start, its port is kept in sync, and every matching section is deleted when
  `open_lan` is turned off or the package is removed.
- Expose `healthcheck`, `healthcheck_threshold` and `healthcheck_timeout` in the
  LuCI form, and show the runtime version in the overview panel.

Verified: `sh -n` on every changed shell script, `go test ./tools/ipkbuild/...`.
