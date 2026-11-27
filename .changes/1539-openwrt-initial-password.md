### OpenWrt: set the dashboard password at install time, and correct the size figures

The package shipped no way to set the dashboard password, so a fresh install
refused a LAN login until the password was changed from the router itself. That
guard is right — a router sold with a known password is a router anyone on the
LAN can reconfigure — but it left an operator with only the web UI stuck until
they had a shell.

`initial_password` (new UCI option, LuCI field with password masking) is exported
as `INITIAL_PASSWORD`, which is the switch the gateway already reads. Empty
exports nothing, so the existing behaviour is unchanged for anyone who does not
set it.

The value is plain text in `/etc/config/9router-go`, mode `0644`, as every UCI
file is. That file already carries `jwt_secret`, which is enough to forge a
session, so the option does not widen the exposure — but the documentation says
so and tells the operator to change the password from the dashboard (which
stores a bcrypt hash) and clear the option afterwards.

**Size figures were stale.** Measured against the 1.9.17 release binary, not
estimated: the binary is 49.8 MB and `Installed-Size` is 51046 KiB; the `.ipk` is
20.7 MB compressed, not the "~18 MB" the page claimed. The 4.6 MB dashboard
figure is unchanged and still holds. The measurements come from the built package
(`go run ./tools/ipkbuild` against the release binary), not from a rebuild of the
bundle.

Two new shell-harness cases: a set password becomes `env INITIAL_PASSWORD=…`, an
empty one exports nothing. Reverting `openwrt/9router-go.init` fails the first
(9/10), so it is not passing vacuously. The harness block defines its own
`procd_set_param` recorder and shares no line with the resource-limit cases, so
the two changes merge without a conflict.

Reviewer follow-up (PR #1539):

- **The harness did not cover special characters in the value.** Its own two
  cases passed `s3cret-pass` and an empty string, so a password carrying a
  space, a single quote or a `$` was never asserted. The block now routes the
  injected option through a `shquote` helper and covers `s3cret-pass`,
  `has space`, `has'single`, `has$dollar` and `has"quote`, each asserted to
  reach `INITIAL_PASSWORD` byte-exact. An empty value still exports nothing
  and does not abort the start. Reverting `openwrt/9router-go.init` gives
  **10/15**, failing exactly the five set-password cases.
- **LuCI before/after captures attached** (see
  `screenshots/openwrt-luci-password-{before,after}.png` and the PR body). Both sides
  were rendered from the real LuCI Lua CBI renderer on an OpenWrt 21.02.7
  x86-64 rootfs with `luci-compat` and `uhttpd-mod-lua` installed and the app
  files copied from the tree under test; the before side is the pre-PR tree
  (`33afbf4c`), the after side is the PR head, same UCI config and viewport.
  `Dashboard password` renders between `Self-update` and `Health check` with
  masking and the `change on first login` placeholder; `o.password = true` is
  confirmed by the rendered input's own `type="password"`.
