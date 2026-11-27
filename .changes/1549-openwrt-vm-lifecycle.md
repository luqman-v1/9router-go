### OpenWrt: the VM suite walks the package's lifecycle, not just its install

`scripts/openwrt-vm-test.py` installed the package from a local `.ipk` and stopped there. Four paths shipped without ever running on a real image:

- **The documented update path.** `opkg install /tmp/gw.ipk` bypasses the feed entirely: the `Packages` index, the `<base>/<arch>` URL that `9router-go-feed add` builds, and opkg's resolution were never exercised. The suite now serves the feed root, registers it with `9router-go-feed add`, runs `opkg update`, and reinstalls the package **through the feed**.
- **Upgrade in place.** A reinstall is where the cron entry can double, the runtime copy can be left stale and the generated JWT secret can be re-minted (which invalidates every dashboard session on the device). All three are asserted after the feed reinstall.
- **`open_lan`.** The LAN firewall rule was covered by the shell harness and never on a live firewall. The suite flips the option on and off across restarts and asserts the rule appears once and is gone again.
- **Removal.** `opkg remove` now asserts no listener, no probe cron entry, no LAN rule, and that `/opt/9router/db/data.sqlite` is still there. `luci-app-9router-go` is removed first: it `Depends` on the gateway, so removing the gateway directly is refused with a dependency error.

Verified by CI on the stacked branch: the `OpenWrt package test` job passed on both 18.06.9 and 23.05.5, running the new feed, reinstall, `open_lan` and removal assertions against a booted image.

Stacked on #1548, so the VM now boots a package that carries the backup, health-probe, cron-owner and feed-preflight fixes.
