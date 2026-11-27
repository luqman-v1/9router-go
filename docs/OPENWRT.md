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

`start_service` adds the once-a-minute probe to `/etc/crontabs/root` and removes
it again when `healthcheck` is `0`; the CRON entry is only rewritten and cron is
only restarted when the table actually changes. Three consecutive `/health`
failures restart the service; one is treated as load. The counter lives in
`/var/run`, so it costs no flash writes and resets on reboot.

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
cannot run procd as PID 1. To run it locally, install `qemu-system-x86` and
`python3`, then:

```sh
make ipk
python3 scripts/openwrt-vm-test.py \
  --image openwrt-18.06.9-x86-64-combined-ext4.img \
  --ipk-dir dist/ipk/x86_64 --version "$(cat VERSION)"
```

## Notes and limits

- The dashboard bundle is 4.6 MB of the binary, and the package is ~18 MB
  compressed. On a 16 MB flash device, install to extroot or a USB mount;
  `data_dir` must be writable and must have room for the database.
- No firewall rule is opened automatically. `open_lan` (default `0`) is the only
  way this package writes one, and that rule is LAN input only: it never opens
  the WAN, and it is removed again when the option is turned off or the package
  is uninstalled. Do not forward 20130 from WAN.
- The 32-bit MIPS router families are not built (see Architectures).
- `arm_cortex-a7_neon-vfpv4` and the other suffixed names are the ones 18.06
  reports. A package whose Architecture reads `arm_cortex-a7` is refused on
  those devices, which is why the suffix is in the directory name.
