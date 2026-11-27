### OpenWrt: stop the health probe punishing a deliberate stop and a bad port

The once-a-minute probe restarted the gateway whenever `/health` did not answer, without asking whether the service was supposed to be running.

- **A stopped service is no longer a failed probe.** `9router-go-backup` stops the gateway to copy the database; a probe landing in that window counted a failure, and a long copy (large database, `tar` of the uploads) could reach the threshold and restart the service **mid-copy**, which is the torn snapshot the stop existed to avoid. A service that crashed and exhausted procd's respawn budget was restarted every `healthcheck_threshold` minutes from cron, bypassing the backoff procd applies to a binary that cannot start. The probe now exits 0 unless `/etc/init.d/9router-go status` reports `running` — procd owns the stopped case, this script only covers the process procd cannot see (alive, listener wedged).
- **A non-numeric `port` falls back to `20130`.** `healthcheck_threshold` and `healthcheck_timeout` were already guarded against a non-numeric UCI value; `port` was not, so a bad value built an unfetchable URL, every probe "failed", and the gateway was restarted every threshold minutes forever.

Verified with two new `scripts/openwrt-shell-harness.py` cases that drive the real packaged script: reverting `openwrt/9router-go-healthcheck` gives **8/10**, the branch gives **10/10**. The harness gained an `/etc/init.d/9router-go` stub (`status` follows a flag file, `restart` appends to a log), a `wget` stub that records the URL it was asked for, and a `UCI_PORT` override so the probe can be run against a value uci would never store.

Stacked on #1545 (the harness case list lives in one file).
