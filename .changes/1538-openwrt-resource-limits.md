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
