### test(openwrt): shell-logic harness for the firewall and health probe

The QEMU test runs once per PR and takes a 273 MB download, so it cannot afford
to run a helper three times and catch an idempotency bug. That gap is how the
firewall rule shipped non-idempotent in #1512, and how the health probe kept
dying on a 18.06-era config until the review found it.

`scripts/openwrt-shell-harness.py` runs the shipped script text against a stub
`uci` backed by a real config file, in about ten seconds, on any runner. Eight
cases cover: one rule across three starts, port-change sync without duplication,
`open_lan=0` removal, legacy duplicate removal, and the probe surviving a config
that predates every option plus its counter behaviour.

Proved against the pre-fix scripts: 3/8 pass. Against this branch: 8/8. The job
runs before the VM step, so a regression fails in seconds instead of after a
boot.

Two real bugs found while writing it, both fixed here:

- `firewall_allow_lan`/`firewall_disallow_lan` guarded on `uci get firewall`,
  which exits 1 when the config holds no rule yet, so the very first start did
  nothing. They guard on the config file now.
- `9router-go-healthcheck` read `port`/`host` without a fallback under `set -e`,
  so a config missing `port` killed the probe with exit 1 every minute.
