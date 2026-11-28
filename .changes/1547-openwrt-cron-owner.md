### OpenWrt: one owner for the health-check cron entry

The probe's `/etc/crontabs/root` line was written by three separate implementations: `cron_sync()` in the init script, an inline block in `postinst`, and another inline block in `prerm`. They disagreed on the part that matters on a router — `cron_sync` compared the result with the current table and skipped the write when nothing changed, while `postinst`/`prerm` rewrote the file and restarted cron unconditionally, on every install, upgrade and removal.

- `sync_cron <0|1>` is now a declared `EXTRA_COMMANDS` entry point on the init script, and `postinst`/`prerm` call it instead of carrying their own copy. The idempotency, the "keep the operator's other cron jobs" rewrite and the flash-write avoidance now exist once.
- `sync_cron` takes its argument from `$1`: rc.common sets `initscript=$1; action=$2` then does `shift 2` before calling `$action "$@"`, so the command name is shifted away and the requested state lands in `$1`.

Verified with four new `scripts/openwrt-shell-harness.py` cases driving the real init script against a temp `/etc/crontabs`: install writes exactly one line, a second install changes nothing, an operator's unrelated cron job survives, removal leaves no probe line, and `sync_cron` is present in `EXTRA_COMMANDS` (without that declaration rc.common drops the action to `help` and the install would silently stop maintaining the entry). Reverting `openwrt/9router-go.init` gives **9/13**; the branch gives **13/13**.

Stacked on #1546 (shared harness file).
