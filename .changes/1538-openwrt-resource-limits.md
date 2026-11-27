### OpenWrt: bound the gateway's memory and file descriptors

Two new UCI options, both `0` meaning "leave the system default":

- `mem_limit` (MiB) is exported as `GOMEMLIMIT`, Go's soft heap ceiling. On a
  128 MB router an unbounded gateway is not the only casualty of the OOM killer —
  it takes `sshd` or the kernel mid-flash with it.
- `nofile_limit` (default 4096) raises `RLIMIT_NOFILE` through
  `procd_set_param limits`. Every proxied stream holds a client socket and an
  upstream socket, so the default 1024 is reached well before the box is busy.

Neither is an `RLIMIT_AS`. Go reserves a large virtual address space up front, so
an address-space limit aborts the process at start-up while the real heap is
still small: the limit meant to protect the gateway would be what kills it.
`GOMEMLIMIT` is the mechanism Go provides for exactly this and needs no code
change, since the runtime reads it from the environment.

Both values pass a numeric guard before use (`case … *[!0-9]*`), because a
malformed `GOMEMLIMIT` is a fatal start-up error in Go rather than a warning, and
a service that refuses to start is worse than one without a ceiling.

The `limits` value is written as `<n> <n>` (soft and hard). procd leaves
`rlim_max` at the inherited value when only one number is given, so raising just
the soft limit above it fails with `EINVAL` and nothing is raised at all.

Verified by five new `scripts/openwrt-shell-harness.py` cases: a set limit
becomes `env GOMEMLIMIT=64MiB` and `limits nofile=4096 4096`; `0` and a
non-numeric value emit neither; a malformed value does not abort the start.
Reverting `openwrt/9router-go.init` makes the first two fail (11/13), so the
cases are not passing vacuously.

Reviewer follow-up (PR #1538):

- The numeric guard did not catch an oversized digit string. `case … *[!0-9]*`
  only rejects non-digits; `9000000000000` passed `-gt 0` and reached the Go
  runtime, which throws `fatal error: malformed GOMEMLIMIT` on a value too
  large to parse — exactly the fatal start-up the guard was supposed to stop.
  Both limits are now clamped to `1048576` and the change is logged. Verified
  by four new harness cases: `9999999999999999` and `9000000000000` arrive at
  procd as `1048576`; `1048576` passes through unchanged; oversized values do
  not abort the start. Reverting the init makes the three clamp cases fail
  (15/18).
- The LuCI form is unchanged by this round, but a before/after capture of the
  page is attached (see `screenshots/openwrt-luci-{before,after}.png`). Both
  sides were rendered from the real LuCI Lua CBI renderer on an OpenWrt
  21.02.7 x86-64 rootfs with `luci-compat` and `uhttpd-mod-lua` installed and
  the app files copied from the PR tree; the after side is at HEAD, the before
  side is the pre-PR tree (`33afbf4c`), same UCI config, same viewport.
  `Memory limit (MiB)` and `Open file limit` render between `Open LAN firewall`
  and `Log level`, both visible in a 1280×1500 window.
- The `nofile_limit` upgrade behaviour is now spelled out in `docs/OPENWRT.md`:
  an existing install picks up the packaged default of `4096` on its first
  restart after `opkg upgrade`; setting the option to `0` restores the system
  default.
