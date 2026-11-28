### OpenWrt: the VM suite walks the package's lifecycle, not just its install

`scripts/openwrt-vm-test.py` installed the package from a local `.ipk` and stopped there. Four paths shipped without ever running on a real image:

- **The documented update path.** `opkg install /tmp/gw.ipk` bypasses the feed entirely: the `Packages` index, the `<base>/<arch>` URL that `9router-go-feed add` builds, and opkg's resolution were never exercised. The suite now serves the feed root, registers it with `9router-go-feed add`, runs `opkg update`, and reinstalls the package **through the feed**.
- **Upgrade in place.** A reinstall is where the cron entry can double, the runtime copy can be left stale and the generated JWT secret can be re-minted (which invalidates every dashboard session on the device). All three are asserted after the feed reinstall.
- **`open_lan`.** The LAN firewall rule was covered by the shell harness and never on a live firewall. The suite flips the option on and off across restarts and asserts the rule appears once and is gone again.
- **Removal.** `opkg remove` now asserts no listener, no probe cron entry, no LAN rule, and that `/opt/9router/db/data.sqlite` is still there. `luci-app-9router-go` is removed first: it `Depends` on the gateway, so removing the gateway directly is refused with a dependency error.

These assertions are **added but not yet executed**. As of `5b5e1292` no image has reached a login prompt in CI: the OpenWrt VM job now fails honestly at `boot to login prompt` on both 18.06.9 and 23.05.5 (`SERIAL_TIMEOUT` of 240 s elapses on each). The boot failure is pre-existing and environmental — `main`'s own run `46098683205` at `2b0fabc6` shows the same two `[FAIL] boot to login prompt` lines and only echoed `PASS` because the old early-return exited 0 — not introduced by this change. A fail-closed boot check is also part of this change, so the red is now honest rather than a false green. Once a runner can boot the images, the suite runs these assertions for real; until then they should be treated as unexecuted.

Stacked on #1548, so the VM now boots a package that carries the backup, health-probe, cron-owner and feed-preflight fixes.
