### test(openwrt): boot both 18.06.9 and 23.05.5 in the VM job

The package has always claimed "18.06 and later", but only 18.06.9 was ever
booted. Everything about 19.07+ was asserted from documentation: that the
service still starts, that the helpers still run, and that the LuCI page's
absence on a JS-only release is the only difference.

The job now boots two images in one run, oldest first:

- **18.06.9** unchanged: the strictest userspace, and the release the package
  targets first.
- **23.05.5** the current LTS line, where LuCI is JS-only. The Lua page needs
  `luci-compat` there and the postinst already says so; what this boot proves is
  that the gateway, the service start and the helper scripts are unaffected by
  the LuCI generation.

Both run inside the one job, so the package is built once and the required
status check keeps its name. Each image is pinned by the sha256 published in its
own release's `sha256sums` and verified before boot. The job timeout moves from
25 to 40 minutes to cover two boots under TCG when `/dev/kvm` is unavailable.
