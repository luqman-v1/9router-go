### OpenWrt packages: `9router-go` and `luci-app-9router-go`

The gateway now ships as an opkg package for **OpenWrt 18.06 and later**, built
by `make ipk` from `tools/ipkbuild` (a Go ar/tar writer, so the build needs no
`ar` binary on the maintainer's machine).

- **18.06 is the baseline, not an afterthought.** The procd script uses the
  two-argument `respawn` (the third `retry` argument is 19.07+), writes no
  `procd_set_param pidfile` (absent on 18.06, and an unknown parameter makes
  procd.sh warn on every boot), and depends on no busybox applet a default
  18.06 image lacks: the pid file replaces `pgrep`, the JWT secret tries
  `sha256sum`/`md5sum`/`base64` in turn, and `chmod` is not called at all.
- **The LuCI page is the Lua tree** (`luasrc/`, `luci-base` only), because that
  is what 18.06 and 19.07 run natively. A JS-only tree would render nothing
  there. On 21.02+ the postinst prints `opkg install luci-compat`, and the
  gateway keeps working without the page either way.
- **Architecture names are the ones 18.06 reports**, suffixed FPU forms
  included: `x86_64`, `i386_pentium4`, `aarch64_generic`,
  `aarch64_cortex-a53`, `aarch64_cortex-a72`, `arm_cortex-a7_neon-vfpv4`,
  `arm_cortex-a15_neon-vfpv4`, `arm_cortex-a9_vfpv3`. opkg matches on that field
  and nothing else, so one binary is published under every name it is valid for
  and each directory gets its own `Packages`.
- **32-bit MIPS is excluded, with the reason recorded.** `modernc.org/sqlite`
  has no mips/mipsle port; supporting those routers would need cgo SQLite and a
  per-target C cross-compiler, ending the single portable binary guarantee at
  once. `docs/OPENWRT.md` says so rather than leaving the gap unexplained.
- **Two compilation bugs found by adding the 32-bit targets.** An untyped
  `100_000_000_000` constant and an `int`-typed `1e12` comparison do not compile
  where `int` is 32 bits (armv7, mips); both are now `float64`.
- **Self-update and `opkg upgrade` coexist.** The package binary lives on the
  read-only squashfs at `/usr/libexec/9router-go/`, postinst copies it to
  `/opt/9router/bin/`, and `/usr/bin/9router-go` execs that copy. The updater
  rewrites the writable copy; the next `opkg upgrade` compares
  `/etc/9router-go/runtime-version` with the package version and reinstalls the
  package binary when the package is newer.
- **Feed.** The release job builds the packages, points the index at the tag's
  download URL and uploads `Packages.<arch>` alongside them;
  `.github/workflows/opkg-feed.yml` discovers every architecture from the
  release's asset names and publishes each index to
  `https://luqman-v1.github.io/9router-go/openwrt/<arch>`.
- **Helpers.** `9router-go-feed` registers or removes the feed,
  `9router-go-backup` saves and restores `data.sqlite` to
  `/etc/9router-go/backup` (which sysupgrade keeps), and
  `9router-go-healthcheck` restarts a process that is alive but no longer
  answering `/health`, with its counter in `/var/run` so it never wears flash.
- **Verified.** `go test ./tools/ipkbuild/` pins the ar member layout, the
  Packages/file size and digest agreement, the architecture aliasing (one
  binary, several names, byte-identical), the `Architecture: all` LuCI package,
  the Lua tree in the LuCI package and its absence from the main one, absolute
  `Filename` entries, and no CRLF in any shipped script. `linux/amd64`,
  `linux/386`, `linux/arm64` and `linux/armv7` all build with `CGO_ENABLED=0`.
  The generated ipk was extracted with an independent ar splitter and GNU tar;
  the postinst, wrapper, feed, backup and health-check scripts were exercised
  against stand-in `uci`/`logger`/`wget` for first install, same-version
  reinstall, package-overrides-self-update, feed idempotency and health-check
  escalation. A CI job (`.github/workflows/openwrt-vm-test.yml`) boots the
  official 18.06.9 x86-64 image in QEMU, installs the built package inside it
  and asserts the service starts through procd, answers `/health`, and the
  helper scripts run — the only test that exercises the service start for real,
  since a chroot or docker rootfs cannot run procd as PID 1. The job builds
  only the `x86_64` package (`make ipk IPK_ARCHES=...` now compiles just the
  Go targets the requested names need) and caches the 273 MB image keyed on
  its pinned sha256, so a PR run is one cross-build and a cache hit.

Documentation: `docs/OPENWRT.md`.
