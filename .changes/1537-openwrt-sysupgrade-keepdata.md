### OpenWrt: keep the database across a sysupgrade

The package now ships `/lib/upgrade/keep.d/9router-go`, listing
`/etc/9router-go`, `/opt/9router/db` and `/opt/9router/uploads`.

OpenWrt's `sysupgrade` preserves only what `/etc/sysupgrade.conf` and
`/lib/upgrade/keep.d/*` name, plus changed conffiles. Its default keep set is
`/lib/upgrade/keep.d/base-files-essential` — a handful of `/etc` files — and a
`/etc/sysupgrade.conf` that ships commented out. Nothing this package owns was
in either, so a flash that kept the operator's settings still discarded the
database: every provider connection, combo and API key. The documentation and
`option data_dir`'s comment claimed the opposite, which is how the gap survived
review.

`/opt/9router/bin` is deliberately **not** listed. It holds the ~52 MB runtime
copy plus the downloaded cloudflared, and `sysupgrade` stages its backup in
`/tmp`, which is RAM on a router. Preserving a file `postinst` rewrites on the
next boot would turn a firmware upgrade into an out-of-memory event.

A non-default `data_dir` cannot be covered by a static file, so `postinst` now
appends the configured database and uploads paths to `/etc/sysupgrade.conf` —
once, guarded by `grep -qxF` — so moving the database to a USB mount does not
silently lose it on the next flash.

Verified by `TestSysupgradeKeepsDatabase`, which parses a built `.ipk`, asserts
the three paths are present, asserts no `/bin` path is, and asserts `postinst`
consults `/etc/sysupgrade.conf`; it fails against a package without the drop-in.
The same test now pins the `conffiles` stanza (`/etc/config/9router-go`), the
mechanism that stops `opkg upgrade` from overwriting the operator's port,
`data_dir` and password. Nothing else read that file, so dropping the stanza
would have reset the operator's settings with no error anywhere; removing the
entry makes the test fail.
