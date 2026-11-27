### OpenWrt: warn when the image cannot fetch the https opkg feed

`9router-go-feed add` registered an `https` feed without checking that the image can fetch one. `opkg` resolves `https` through a `libustream-*` provider plus a CA bundle; a minimal image can lack either, and the failure arrives one command later as `opkg update` printing `SSL support not available` — a message that names neither the missing package nor the fix, and gives no hint that the registered feed line is correct.

- `check_https_support` runs before the feed line is written and prints the exact `opkg install libustream-mbedtls ca-bundle` that unblocks it. It only fires for `https` URLs, so a plain-`http` mirror passed to `add` produces no noise.
- The feed line is still registered. Refusing would be circular: the provider has to be installable before the feed can be used, and official feeds already carry it.

Verified with three new `scripts/openwrt-shell-harness.py` cases driving the real script against an `opkg` stub and a temp `customfeeds.conf`: a bare image warns and still registers the architecture's URL, and an image with the provider and CA bundle present is silent while the line says `https://`. Reverting `openwrt/9router-go-feed` gives **23/24**; the branch gives **24/24**.

The `opkg update` / `opkg upgrade` path against a served feed is covered end to end by the VM suite (#1549).

Stacked on #1547 (shared harness file).
