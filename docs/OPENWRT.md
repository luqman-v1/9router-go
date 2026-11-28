# 9router-go on OpenWrt

Minimum supported release: **OpenWrt 18.06**. Everything in the package (procd
service, busybox shell, Lua LuCI) is written to that baseline; later releases
run the same files unchanged, and the architecture list covers the names 18.06
itself reports.

| Package | Contents | Architecture |
| --- | --- | --- |
| `9router-go` | the binary, the procd service, the UCI config, the helper scripts | per target, see below |
| `luci-app-9router-go` | the LuCI page (status, start/stop/restart, settings) | `all` |

## Architectures

`opkg` installs a package only when its `Architecture` field matches the
device's `DISTRIB_ARCH` exactly, so one package is published per name. The
names below are what 18.06 and later actually report.

| OpenWrt architecture | Typical devices |
| --- | --- |
| `x86_64` | x86/64, PC-based routers, VMs |
| `i386_pentium4` | x86/legacy |
| `aarch64_generic` | armvirt/64, generic arm64 images |
| `aarch64_cortex-a53` | mediatek/mt7622, bcm2710, mvebu/cortexa53 |
| `aarch64_cortex-a72` | bcm27xx/bcm2711, mvebu/cortexa72 |
| `arm_cortex-a7_neon-vfpv4` | ipq40xx, mediatek/mt7623, bcm2709, sunxi |
| `arm_cortex-a15_neon-vfpv4` | ipq806x, armvirt/32 |
| `arm_cortex-a9_vfpv3` | mvebu/cortexa9 |

**32-bit MIPS is not supported** (ramips `mipsel_24kc`, ath79/ar71xx
`mips_24kc`, lantiq). The blocker is the database driver, not this packaging:
`modernc.org/sqlite` is pure Go and ships no mips/mipsle port, so the gateway
cannot be built for those targets without cgo and a per-target C cross-compiler.
On such a router the gateway has no install path at all, package or otherwise.

Check the device's architecture before installing:

```sh
. /etc/openwrt_release && echo "$DISTRIB_ARCH"
```

## Install

```sh
# 1. Register the feed (writes /etc/opkg/customfeeds.conf)
echo "src/gz 9router-go https://luqman-v1.github.io/9router-go/openwrt/$(. /etc/openwrt_release && echo "$DISTRIB_ARCH")" \
	>> /etc/opkg/customfeeds.conf

# 2. Install
opkg update
opkg install 9router-go
opkg install luci-app-9router-go   # optional, the router UI
```

`9router-go-feed add` does the same with the architecture detected for you, and
is idempotent:

```sh
9router-go-feed add          # register
9router-go-feed status       # show what is registered
9router-go-feed remove       # drop it
```

The feed is `https`, and `opkg` needs a TLS provider plus a CA bundle to fetch
it: `libustream-*` and `ca-bundle`. Both are present on any image with LuCI, and
a minimal image can lack them — `opkg update` then fails with `SSL support not
available`, which names neither. `9router-go-feed add` checks before it registers
and prints the exact `opkg install` line when something is missing; the feed line
is still written, because the provider has to be installable before the feed can
be used.

Manual install works too: download
`9router-go_<version>_<arch>.ipk` from the release page and run
`opkg install ./9router-go_<version>_<arch>.ipk`.

The postinst starts the service and prints the dashboard URL. Open
`http://<router-lan-ip>:20130`.

## Layout on the router

```
/usr/libexec/9router-go/9router-go       package binary (squashfs, read-only)
/usr/bin/9router-go                      launcher: execs the runtime copy when present
/etc/init.d/9router-go                   procd service
/etc/config/9router-go                   UCI config (conffile, survives upgrade)
/opt/9router/db/data.sqlite              database
/opt/9router/bin/9router-go              runtime copy (overlay, writable)
/opt/9router/bin/cloudflared             downloaded on first tunnel enable
/etc/9router-go/runtime-version          version of the runtime copy
/etc/9router-go/backup/                  database backups
/var/run/9router-go.pid                  pid file, written by the launcher
```

The split between the package binary and the runtime copy is deliberate. On
squashfs the package file cannot be rewritten, so a self-update would either
fail or corrupt the image. The launcher prefers `/opt/9router/bin/9router-go`,
which is writable, and postinst installs it.

`/opt` keeps its contents across a sysupgrade that keeps settings, but only
because this package ships `/lib/upgrade/keep.d/9router-go` naming
`/opt/9router/db` and `/opt/9router/uploads`. Without that drop-in OpenWrt's
default keep set (`/lib/upgrade/keep.d/base-files-essential` plus a
commented-out `/etc/sysupgrade.conf`) names nothing this package owns, and the
database would be discarded on every flash. `/tmp` and `/var` are still not
preserved, which is why the database is not there.

The pid file is written by the launcher (`echo $$` before `exec`), not by
`procd_set_param pidfile`: that parameter does not exist in every procd release
18.06 and later ship, and an unknown parameter makes procd.sh warn on every
boot.

## Configuration

UCI, `/etc/config/9router-go`:

```sh
uci set 9router-go.main.port='20130'      # dashboard is served here too
uci set 9router-go.main.host=''           # empty = all interfaces, 127.0.0.1 = LAN-off
uci set 9router-go.main.data_dir='/opt/9router'
uci set 9router-go.main.auto_update='0'
uci set 9router-go.main.log_level='info'
uci set 9router-go.main.initial_password='…'  # first dashboard login, empty = none
uci commit 9router-go
/etc/init.d/9router-go restart
```

The same settings appear in LuCI under **Services → 9router-go**, plus a status
badge, a dashboard link that uses the router's own hostname, and
Start/Stop/Restart buttons. Those buttons call `/etc/init.d/9router-go` through
a small LuCI endpoint, so they still work when the gateway's own HTTP listener
is wedged.

### LuCI variants

The package ships the **Lua** LuCI tree (`luasrc/`), because that is what 18.06
and 19.07 run natively. On 21.02 and later, LuCI is JS-only and the Lua runtime
lives in a separate package:

```sh
opkg install luci-compat     # only needed on 21.02+
```

Installing on such an image prints that hint. The gateway itself does not
depend on LuCI; the page is optional.

## Dashboard password
The gateway ships no password of its own. With `initial_password` empty, a
fresh install accepts the well-known default **only** from the router itself:
a login over the LAN is refused with "Default password must be changed" until
the password is changed from the machine running the gateway (`INITIAL_PASSWORD`
set, or the dashboard reached on the router's own loopback).

That guard is correct — a router sold with a known dashboard password is a
router anyone on the LAN can reconfigure — but it leaves an operator who only
has the web UI stuck until they have a shell. Setting the password at install
time skips it:
```sh
uci set 9router-go.main.initial_password='a-password-you-choose'
uci commit 9router-go
/etc/init.d/9router-go restart
```
The value is exported as `INITIAL_PASSWORD` and is **plain text in
`/etc/config/9router-go`** (mode `0644`, like every UCI file). Change it from
the dashboard after the first login, which stores a bcrypt hash in the settings
database, and then clear the option so the plain-text copy goes away:
```sh
uci set 9router-go.main.initial_password=''
uci commit 9router-go
```
Note that this file already holds `jwt_secret`, which is enough to forge a
session, so a local reader has an equivalent secret either way; the option does
not widen the exposure, it just makes the file worth protecting. It is
world-readable by default, so do not reuse a password from anywhere else.

## Updates: two paths, one version

| Path | Command | When to use |
| --- | --- | --- |
| opkg | `opkg update && opkg upgrade 9router-go` | default; the package owns the version |
| self-update | `9router-go update` | no feed registered, or a newer binary than the feed carries |

Self-update rewrites `/opt/9router/bin/9router-go` and records the version in
`/etc/9router-go/runtime-version`. The next `opkg upgrade` compares that record
with the package version and copies the package binary over the runtime copy when
the package is newer; the package always wins, so a device cannot be left running
a build opkg did not install.

Set `auto_update='1'` to let the gateway check GitHub on its own. It is off by
default: on a router, an uncontrolled binary replacement is both a surprise and a
flash write.

## Service control

```sh
/etc/init.d/9router-go start|stop|restart|status|enable|disable
/etc/init.d/9router-go sync_cron 0|1   # remove / install the probe cron entry
logread -e 9router-go          # service log
wget -qO- http://127.0.0.1:20130/health
```

procd respawns a crashed process up to 5 times per hour and then stops trying, so
a binary that cannot start does not fill the log. A process that stays alive but
stops answering is caught by the health check, which is installed automatically:

```sh
uci get 9router-go.main.healthcheck          # 1 (default) or 0
uci get 9router-go.main.healthcheck_threshold # failures before restart, 3
uci get 9router-go.main.healthcheck_timeout   # seconds per probe, 5
```

`sync_cron` is the only thing that writes the once-a-minute probe into
`/etc/crontabs/root`: `start_service` calls it on every start, `postinst` after
an install or upgrade, and `prerm` on removal. It compares the result with the
current table first, so the file is only rewritten and cron only restarted when
the entry actually changes — an upgrade that changes nothing costs no flash
write. Three consecutive `/health` failures restart the service; one is treated
as load. The counter lives in `/var/run`, so it costs no flash writes and resets
on reboot.

The probe runs unless procd reports the service as stopped. A stopped
gateway is procd's business — it respawns a crash, and applies its own backoff to
a binary that cannot start — and the probe covering that case would restart it
every `healthcheck_threshold` minutes, bypassing the backoff. It also keeps the
probe quiet while `9router-go-backup` stops the gateway to copy the database,
which otherwise counts a deliberate stop as a failure and can restart the service
mid-copy.

The gate reads `/etc/init.d/9router-go status` and skips only on a verdict that
names the service stopped (`not running` or `stopped`). Anything else — including
the help text a call returns on OpenWrt 18.06 and 19.07, where `status` is not a
registered rc.common action — is treated as "the gate cannot decide" and the probe
fires anyway, because a silently disabled watchdog costs a wedged gateway while a
redundant probe costs one HTTP request. A non-numeric `port` falls back to `20130`
in the probe rather than turning every check into a failed URL.

## Memory and file limits
```sh
uci set 9router-go.main.mem_limit='64'        # MiB, 0 = no limit (default)
uci set 9router-go.main.nofile_limit='4096'   # 0 = system default
uci commit 9router-go
/etc/init.d/9router-go restart
```
`mem_limit` is exported as `GOMEMLIMIT`, Go's **soft** heap ceiling: the garbage
collector works harder as the heap approaches it, instead of the process growing
until the router's OOM killer picks a victim. On a small router that victim is
often `sshd` or the kernel doing the flashing, so the ceiling protects more than
the gateway.

It is deliberately not an `RLIMIT_AS` (`procd_set_param limits as=…`). Go
reserves a large virtual address space up front, so an address-space limit
aborts the process at start-up while the actual heap is still small — the limit
that is supposed to protect the gateway would be what kills it. A malformed
value sets nothing at all rather than passing garbage through, because Go treats
an unparsable `GOMEMLIMIT` as a fatal start-up error. An oversized value is
clamped to `1048576` MiB and logged instead: Go also throws `fatal error:
malformed GOMEMLIMIT` on a number too large to parse, so the guard must bound the
magnitude as well as the syntax.

`nofile_limit` is an `RLIMIT_NOFILE` set through procd. Each proxied stream
holds one descriptor for the client socket and one for the upstream, so a busy
gateway reaches the default 1024 long before it is out of CPU. Both the soft and
the hard limit are set: procd leaves `rlim_max` at the inherited value when it is
omitted, and raising only the soft limit above it fails with `EINVAL`.

A fresh install and an upgraded install both start with `nofile_limit=4096`
from the packaged default, so an existing install picks up the raised limit on
its first restart after `opkg upgrade`. Setting the option to `0` restores the
system default (1024 on most images).

## LAN firewall

```sh
uci set 9router-go.main.open_lan=1   # default 0: LAN access stays closed
uci commit 9router-go
/etc/init.d/9router-go restart
```

When `open_lan` is `1` the init script creates one `uci-firewall` rule
(`9router-go-allow-lan`, `src=lan`, `proto=tcp udp`, `dest_port=<port>`,
`target=ACCEPT`). Turning the option back to `0` deletes every rule carrying that
name, as does removing the package. Nothing is opened automatically and nothing
is forwarded from WAN; the rule is LAN input only.

## Backup before a flash

```sh
9router-go-backup save                  # /etc/9router-go/backup/data.sqlite
9router-go-backup save /tmp/usb/data.sqlite
9router-go-backup restore [file]        # keeps the replaced file as *.before-restore
9router-go-backup list
```

The database is not the only state: uploaded media under `data_dir/uploads` is
archived to `<file>.uploads.tar` beside it and restored from the same path by
`restore`, so a dashboard logo does not go missing on a new router. When busybox
`tar` is absent the media is skipped with a message and the database backup still
succeeds.

`/etc/9router-go` survives sysupgrade with "keep settings", so the default backup
location comes back after the flash. Copy the file off the device before
overwriting the firmware anyway.

The gateway runs SQLite in WAL mode, so a `data.sqlite-wal` file can sit beside
the database holding transactions that have not been checkpointed into it yet.
`save` copies that file to `<file>-wal` when it exists, and `restore` puts it
back — a copy of `data.sqlite` alone would silently be missing the most recent
writes. In the other direction, `restore` deletes a `-wal`/`-shm` pair belonging
to the database it replaces before writing the incoming file: SQLite replays
whatever `-wal` it finds beside a database, and one from a different database
means corrupted or reverted rows. The `-shm` file is a shared-memory index, never
data, and is never copied.

## Tunnels

- **Cloudflare**: works with no extra package. The gateway downloads
  `cloudflared` into `/opt/9router/bin/` on first enable. Keep `/opt` writable
  and reachable; a firmware upgrade that wipes overlay storage drops the binary
  and it is downloaded again on the next start.
- **Tailscale**: install the OpenWrt `tailscale` package
  (`opkg install tailscale`) and bring the daemon up
  (`tailscale up --authkey=...`). The gateway shells out to the `tailscale`
  binary on `PATH`, so nothing else is needed.

## Build

```sh
make ipk                                   # local, relative feed entries
make ipk IPK_BASE_URL=https://...          # index entries point at release assets
IPK_ARCHES='x86_64=dist/ipk-bin/linux-amd64' make ipk   # one target only
```

Output: `dist/ipk/<openwrt-arch>/`, each holding both packages plus `Packages`
and `Packages.gz`. `tools/ipkbuild` writes the ar archives and the index;
`go test ./tools/ipkbuild/` checks the archive layout, the index/file agreement,
the architecture aliasing, the Lua LuCI tree and the absence of CRLF in shipped
scripts.

The release workflow builds the packages, uploads them as release assets, and
`.github/workflows/opkg-feed.yml` discovers every architecture from the asset
names and publishes each index to
`https://luqman-v1.github.io/9router-go/openwrt/<arch>`.

A CI job (`.github/workflows/openwrt-vm-test.yml`) boots the official 18.06.9
x86-64 image in QEMU on every push and PR to `main`, installs the built
package inside it, and asserts the service starts through procd, answers
`/health`, and the helper scripts run. It is the only test that exercises the
service start for real: a chroot or docker rootfs can run `opkg install` but
cannot run procd as PID 1. It also boots 23.05.5, and after the first
install it walks the documented lifecycle instead of stopping at "it started":
register the served feed with `9router-go-feed add`, `opkg update`, reinstall
the package through the feed, and assert the probe cron entry stays single,
the generated JWT secret survives and the service comes back; flip `open_lan`
on and off and assert the firewall rule follows; then `opkg remove` and assert
no listener, no cron entry, no rule, and a database left in place.
To run it locally, install `qemu-system-x86` and
`python3`, then:

```sh
make ipk
python3 scripts/openwrt-vm-test.py \
  --image openwrt-18.06.9-x86-64-combined-ext4.img \
  --ipk-dir dist/ipk/x86_64 --version "$(cat VERSION)"
```

## Notes and limits

- The gateway binary is ~50 MB installed (`Installed-Size` in the package is
  51046 KiB); the `.ipk` itself is ~21 MB compressed, and the dashboard bundle
  embedded in it is 4.6 MB of that. On a 16 MB flash device, install to extroot
  or a USB mount; `data_dir` must be writable and must have room for the
  database.
- No firewall rule is opened automatically. `open_lan` (default `0`) is the only
  way this package writes one, and that rule is LAN input only: it never opens
  the WAN, and it is removed again when the option is turned off or the package
  is uninstalled. Do not forward 20130 from WAN.
- The 32-bit MIPS router families are not built (see Architectures).
- `arm_cortex-a7_neon-vfpv4` and the other suffixed names are the ones 18.06
  reports. A package whose Architecture reads `arm_cortex-a7` is refused on
  those devices, which is why the suffix is in the directory name.
