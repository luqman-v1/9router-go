#!/usr/bin/env python3
"""Boot a real OpenWrt x86-64 image in QEMU and test the 9router-go opkg package
inside it, end to end.

Why a VM and not a chroot: only a booted kernel runs procd as PID 1, so this is
the only way to prove `/etc/init.d/9router-go start` actually brings the gateway
up and answers /health. A chroot or docker rootfs can run `opkg install` but not
the service, which is the part that has failed in the past.

How the test file gets in: QEMU's user-mode network (slirp) lets the guest reach
the host at 10.0.2.2. We serve the built .ipk files from a local HTTP server on
the host; slirp delivers 10.0.2.2:<port> to the host's loopback, so the guest's
`wget` pulls the package with no ssh keys or shared folders.

Usage:
  python3 openwrt-vm-test.py --image openwrt.img --ipk-dir dist/ipk/x86_64 \
      --version 1.9.15 --port 20130

Exit 0 when every assertion passes; non-zero on the first failure, with the
captured serial output so the CI log shows where it broke.
"""
import argparse
import http.server
import os
import re
import shutil
import socket
import subprocess
import sys
import tempfile
import threading
import time

# QEMU is the only external binary required. Prefer a KVM-accelerated machine
# (GitHub runners expose /dev/kvm, so boot is ~30s not ~5min); fall back to TCG.
QEMU = shutil.which("qemu-system-x86_64")
if QEMU is None:
    sys.exit("qemu-system-x86_64 not found; install qemu-system-x86")

SERIAL_TIMEOUT = 240  # seconds to wait for the login prompt after boot
CMD_TIMEOUT = 90      # seconds per in-guest command


def have_kvm():
    return os.path.exists("/dev/kvm")


class Serial:
    """Drive the QEMU serial console.

    A reader thread continuously drains stdout into `buf` under a lock, so the
    main thread can poll for a pattern against a deadline without ever blocking
    forever on a quiet guest (which is what a direct `read(1)` would do if QEMU
    hangs). Each `wait` consumes the matched pattern out of `buf`, so the next
    wait starts from a clean buffer and cannot match a stale prompt left by the
    previous command."""

    def __init__(self, proc):
        self.proc = proc
        self.buf = b""
        self.lock = threading.Lock()
        self.log = []
        self.loglock = threading.Lock()
        self.alive = True
        self.reader = threading.Thread(target=self._read, daemon=True)
        self.reader.start()

    def _read(self):
        try:
            # Raw os.read on the fd, not a buffered read: on Linux a pipe
            # returns data as it arrives, but on Windows a buffered read(4096)
            # blocks until the whole buffer is full, so the console would
            # appear to hang. os.read returns whatever is available now.
            while True:
                chunk = os.read(self.proc.stdout.fileno(), 4096)
                if not chunk:
                    break
                with self.lock:
                    self.buf += chunk
                with self.loglock:
                    self.log.append(chunk)
                    if len(self.log) > 4000:
                        self.log = self.log[-2000:]
        except Exception:
            pass
        finally:
            self.alive = False

    def _wait_for(self, pattern, timeout):
        pat = pattern if isinstance(pattern, bytes) else pattern.encode()
        deadline = time.time() + timeout
        while time.time() < deadline:
            with self.lock:
                idx = self.buf.find(pat)
                if idx != -1:
                    # Consume up to and including the match so the next wait
                    # starts clean and cannot match a stale prompt.
                    self.buf = self.buf[idx + len(pat):]
                    return True
            if not self.alive:
                # stdout reached EOF, so QEMU has exited: the pattern can never
                # arrive. Return at once instead of burning the whole timeout,
                # which is what made a dead QEMU look like a slow boot.
                return False
            time.sleep(0.05)
        return False

    def wait_any(self, patterns, timeout):
        """Wait for the first of several patterns; return the one that matched.

        Used for the boot handshake, where the guest either prints the
        `askfirst` banner (and then needs a keypress before a login prompt) or
        goes straight to the prompt. Consumes through the match, like
        `_wait_for`, so the buffer cannot hand the same text to a later wait.
        """
        pats = [p if isinstance(p, bytes) else p.encode() for p in patterns]
        deadline = time.time() + timeout
        while time.time() < deadline:
            with self.lock:
                best = None
                for pat in pats:
                    idx = self.buf.find(pat)
                    if idx != -1 and (best is None or idx < best[0]):
                        best = (idx, pat)
                if best is not None:
                    idx, pat = best
                    self.buf = self.buf[idx + len(pat):]
                    return pat
            if not self.alive:
                return None
            time.sleep(0.05)
        return None
    def send(self, line):
        self.proc.stdin.write(line.encode() + b"\r")
        self.proc.stdin.flush()
    def send_key(self, ch="\r"):
        """Send a bare keypress.

        QEMU's `-nographic` serial is a raw terminal, so Enter arrives as CR.
        busybox `askfirst` blocks on exactly this keypress before it opens the
        console.
        """
        self.proc.stdin.write(ch.encode())
        self.proc.stdin.flush()

    def wait(self, pattern, timeout=CMD_TIMEOUT):
        return self._wait_for(pattern, timeout)

    def command(self, cmd, expect, timeout=CMD_TIMEOUT):
        """Run `cmd` in the guest and wait for `expect`. Returns (matched,
        output) where output is what the guest printed after the command was
        sent, for assertions."""
        with self.loglock:
            start = len(self.log)
        self.send(cmd)
        ok = self.wait(expect, timeout)
        with self.loglock:
            out = b"".join(self.log[start:])
        text = out.decode(errors="replace")
        # The raw stream ends with the next shell prompt (root@host:~# ). Drop
        # it so assertions on the last line see the command's real output, not
        # the prompt that follows it.
        text = re.sub(r"\S+@\S+:[^#]*#\s*$", "", text)
        return ok, text

    def command_retry(self, cmd, want, tries, timeout=CMD_TIMEOUT):
        """Run `cmd` up to `tries` times until `want` is found in the output,
        for checks that race a service coming up (the gateway needs a few
        seconds to bind its port after opkg install). `want` is a substring or
        a compiled regex. Returns the last (matched, output)."""
        ok, text = False, ""
        for _ in range(tries):
            ok, text = self.command(cmd, "#", timeout)
            if re.search(want, text):
                return True, text
            time.sleep(2)
        return False, text

    def dump(self):
        with self.loglock:
            return b"".join(self.log).decode(errors="replace")


def serve_dir(directory, port):
    """Serve `directory` over HTTP on the host loopback. slirp maps the guest's
    10.0.2.2:<port> here, so the guest can wget the packages."""
    handler = lambda *a, **k: http.server.SimpleHTTPRequestHandler(
        *a, directory=directory, **k)
    httpd = http.server.ThreadingHTTPServer(("127.0.0.1", port), handler)
    threading.Thread(target=httpd.serve_forever, daemon=True).start()
    return httpd


def free_port():
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    p = s.getsockname()[1]
    s.close()
    return p


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--image", required=True)
    ap.add_argument("--ipk-dir", required=True, help="dir with the .ipk files")
    ap.add_argument("--version", required=True)
    ap.add_argument("--port", default="20130")
    ap.add_argument("--memory", default="1024")
    args = ap.parse_args()

    ipk = os.path.join(args.ipk_dir, f"9router-go_{args.version}_x86_64.ipk")
    luci = os.path.join(args.ipk_dir, f"luci-app-9router-go_{args.version}_all.ipk")
    for f in (ipk, luci):
        if not os.path.exists(f):
            sys.exit(f"missing package: {f}")

    http_port = free_port()
    serve_dir(args.ipk_dir, http_port)
    print(f"serving {args.ipk_dir} on 127.0.0.1:{http_port}", flush=True)

    accel = "kvm" if have_kvm() else "tcg"
    print(f"booting OpenWrt (accel={accel})...", flush=True)
    def launch(accel):
        qargs = [
            QEMU,
            "-m", args.memory,
            "-drive", f"file={args.image},format=raw,if=virtio",
            "-netdev", "user,id=net0",
            "-device", "e1000,netdev=net0",
            "-nographic",
            "-no-reboot",
            "-machine", f"accel={accel}",
            "-smp", "2",
        ]
        # stderr is teed to a file rather than a dropped PIPE: if QEMU dies or
        # produces no serial output the reason only exists there, and a PIPE
        # nobody reads can also deadlock QEMU once its stderr buffer fills.
        err_path = os.path.join(tmp, f"qemu-{accel}.stderr")
        err_fh = open(err_path, "wb")
        p = subprocess.Popen(qargs, stdin=subprocess.PIPE,
                             stdout=subprocess.PIPE, stderr=err_fh)
        return p, err_path, err_fh

    # Prefer KVM (fast on GitHub runners) but fall back to TCG. Under some
    # KVM configurations the guest produces no serial output at all and never
    # reaches a login prompt within SERIAL_TIMEOUT, while the same image boots
    # fine under TCG (verified locally: SeaBIOS -> GRUB -> login in ~17 s). If a
    # KVM boot yields no serial output after a short probe, relaunch under TCG
    # so the suite can actually run instead of failing on an unusable machine.
    tmp = os.path.join(tempfile.gettempdir(), "vm-9router-go")
    os.makedirs(tmp, exist_ok=True)

    def boot_and_wait(accel):
        p, err_path, err_fh = launch(accel)
        s = Serial(p)
        # The serial console is a busybox `askfirst` console: it prints
        # "Please press Enter to activate this console." and blocks until a key
        # arrives. Then these images drop straight to a root shell
        # (`root@OpenWrt:/#`) instead of prompting `login:`, so waiting for a
        # login prompt was why no image ever "booted" in this suite. Accept
        # either outcome: a login prompt (some images do ask) or the shell.
        at_login, at_shell = b"login:", b"root@"
        first = s.wait_any([b"Please press Enter", at_login, at_shell],
                           SERIAL_TIMEOUT)
        if first is not None and first.startswith(b"Please press Enter"):
            s.send_key()
            first = s.wait_any([at_login, at_shell], CMD_TIMEOUT)
        if first == at_login:
            s.send("root")
            first = s.wait_any([at_shell], CMD_TIMEOUT)
        return p, s, err_path, err_fh, first == at_shell

    proc, ser, qemu_err_path, qemu_err_fh, boot_ok = boot_and_wait(
        "kvm" if have_kvm() else "tcg")
    accel_note = ""
    if not boot_ok and have_kvm():
        # KVM is present but the guest produced no serial output. That is an
        # environment property, not a package failure, so retry under TCG and
        # let the suite run. The QEMU stderr captured by launch() is the only
        # record of why, so surface it.
        proc.terminate()
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
        qemu_err_fh.close()
        sys.stderr.write(
            "[warn] no login prompt under accel=kvm, retrying with tcg\n"
            f"[warn] qemu stderr: {open(qemu_err_path).read()[-2000:]}\n")
        accel_note = " (kvm boot produced no login prompt; retried with tcg)"
        proc, ser, qemu_err_path, qemu_err_fh, boot_ok = boot_and_wait("tcg")
    boot_out = ser.dump()

    failures = []

    class BootFailure(Exception):
        """Raised by a fatal check to abort the boot/lifecycle sequence.

        A boot or login that never reached its prompt means none of the
        post-login assertions can run, so there is no point walking the rest of
        the sequence — but the failure still has to reach the summary. Raising
        here (instead of `return`-ing out of main) unwinds through the `finally`
        teardown and lands in the summary, which reports the failure and exits
        non-zero. Without this the job reported success while nothing was
        actually tested.
        """

    def check(name, ok, out, fatal=False):
        status = "PASS" if ok else "FAIL"
        print(f"[{status}] {name}", flush=True)
        if not ok:
            failures.append(name)
            print("---- output ----")
            print(out[-3000:])
            print("----------------", flush=True)
            if fatal:
                raise BootFailure(name)

    try:
        # --- boot to a root shell ---
        # boot_and_wait already drove the console to a root shell (or a login
        # prompt it answered) and consumed that match, so there is nothing left
        # to wait for here — re-waiting was the double-wait bug that made this
        # always fail. Report the result the handshake captured.
        check("boot to root shell" + accel_note, boot_ok, boot_out, fatal=True)
        # Flush anything left from the boot banner so the first command's
        # wait('#') cannot match a stale prompt and return before it has run.
        with ser.lock:
            ser.buf = b""

        # The stock image holds br-lan at 192.168.1.1/24, so the slirp network
        # the packages are served over (10.0.2.0/24, host gateway 10.0.2.2) is
        # unreachable. Adding the address ad-hoc is not enough: fw4 still binds
        # the lan zone to 192.168.1.0/24 and rejects the foreign source with
        # "Operation not permitted" (wget RC=4). Move the LAN interface onto the
        # slirp subnet through uci so the zone, the address and the default
        # route all agree.
        ok, out = ser.command(
            "uci set network.lan.ipaddr='10.0.2.15'; "
            "uci set network.lan.netmask='255.255.255.0'; "
            "uci set network.lan.gateway='10.0.2.2'; "
            "uci set network.lan.dns='10.0.2.3'; "
            "uci commit network; "
            "/etc/init.d/network restart >/dev/null 2>&1; "
            "sleep 5; echo NETDONE; ip -4 addr show br-lan", "#")
        # Split on the marker so the echoed command line cannot satisfy this.
        check("guest moves its LAN onto the slirp network",
              "10.0.2.15" in out.split("NETDONE")[-1], out, fatal=True)

        # --- install the gateway package ---
        # Assert the transfer by byte size: a check that only looks for the
        # file name matches the echoed command line and passes on a truncated
        # or 404 body, which then fails later as a malformed package.
        gw_size = os.path.getsize(ipk)
        ok, out = ser.command(
            f"wget -O /tmp/gw.ipk http://10.0.2.2:{http_port}/"
            f"9router-go_{args.version}_x86_64.ipk >/dev/null 2>&1; "
            "echo SZ=$(wc -c </tmp/gw.ipk)", "#")
        got = re.search(r"SZ=(\d+)", out)
        check("fetch gateway ipk over slirp",
              got is not None and int(got.group(1)) == gw_size, out)

        # Extracting a 21 MB package on an emulated CPU is slow, and the marker
        # makes the result observable: a bare `opkg install` can return on a
        # stale prompt with no output, which then looks like a silent success.
        ok, out = ser.command("opkg install /tmp/gw.ipk 2>&1; "
                              "echo INSTALL_RC=$?", "#", timeout=300)
        got = re.search(r"INSTALL_RC=(\d+)", out)
        check("opkg install 9router-go",
              got is not None and got.group(1) == "0", out)

        # --- the service must actually run ---
        # "is running" is procd's status line; a bare \d+ would match the
        # "9router" in the echoed command and pass without the service.
        ok, out = ser.command_retry(
            "/etc/init.d/9router-go status", r"is running", 8)
        check("init.d status reports running", ok, out)

        ok, out = ser.command_retry(
            f"wget -qO- http://127.0.0.1:{args.port}/health",
            r'"status":"ok"', 10)
        check("gateway answers /health", ok, out)

        # --- UCI config was created by postinst ---
        ok, out = ser.command("uci get 9router-go.main.port", "#")
        check("uci config present",
              ok and out.strip().splitlines()[-1].strip().isdigit(), out)

        # --- the helper scripts run without error ---
        ok, out = ser.command("9router-go-backup save >/dev/null 2>&1; echo rc=$?", "#")
        check("9router-go-backup save", ok and "rc=0" in out, out)

        ok, out = ser.command("9router-go-feed status >/dev/null 2>&1; echo rc=$?", "#")
        check("9router-go-feed status", ok and "rc=0" in out, out)

        # --- LuCI package installs and the Lua tree lands ---
        luci_size = os.path.getsize(luci)
        ok, out = ser.command(
            f"wget -O /tmp/luci.ipk http://10.0.2.2:{http_port}/"
            f"luci-app-9router-go_{args.version}_all.ipk >/dev/null 2>&1; "
            "echo SZ=$(wc -c </tmp/luci.ipk)", "#")
        got = re.search(r"SZ=(\d+)", out)
        check("fetch luci ipk",
              got is not None and int(got.group(1)) == luci_size, out)

        ok, out = ser.command("opkg install /tmp/luci.ipk", "#")
        check("opkg install luci-app-9router-go",
              ok and "Collected errors" not in out and "Configuring luci-app-9router-go" in out, out)

        ok, out = ser.command(
            "test -f /usr/lib/lua/luci/controller/9router-go.lua && echo LUCI_OK", "#")
        check("luci controller installed", ok and "LUCI_OK" in out, out)

        # --- the postinst logged the install ---
        ok, out = ser.command("logread | grep -c '9router-go installed'", "#")
        check("postinst logged the install",
              ok and out.strip().splitlines()[-1].strip() != "0", out)
        # --- the documented update path: feed add, opkg update, reinstall ---
        # Everything above installs from a file path, which bypasses the feed
        # entirely: the Packages index, the URL layout `9router-go-feed add`
        # builds, and opkg's own resolution are never exercised. The feed needs
        # the architecture directory under the served root, and /tmp/gw.ipk came
        # from the arch dir directly, so serve the parent on its own port.
        feed_root = os.path.dirname(os.path.abspath(args.ipk_dir))
        feed_port = free_port()
        serve_dir(feed_root, feed_port)
        ok, out = ser.command(
            f"9router-go-feed add http://10.0.2.2:{feed_port}; echo rc=$?", "#")
        check("9router-go-feed add", ok and "rc=0" in out, out)
        ok, out = ser.command("opkg update 2>&1 | tail -5", "#")
        check("opkg update reads the served feed",
              ok and "Collected errors" not in out, out)
        # The generated secret must survive an upgrade: postinst only generates
        # one when the option is empty, and a reinstall that minted a new secret
        # would invalidate every dashboard session on the device.
        ok, out = ser.command("uci get 9router-go.main.jwt_secret", "#")
        secret_before = out.strip().splitlines()[-1].strip() if ok else ""
        ok, out = ser.command(
            "grep -c 9router-go-healthcheck /etc/crontabs/root", "#")
        cron_before = out.strip().splitlines()[-1].strip() if ok else "?"
        ok, out = ser.command(
            "opkg --force-reinstall install 9router-go 2>&1 | tail -5", "#")
        check("opkg reinstalls 9router-go from the feed",
              ok and "Collected errors" not in out
              and ("Configuring 9router-go" in out or "Reinstalling" in out), out)
        ok, out = ser.command(
            "grep -c 9router-go-healthcheck /etc/crontabs/root", "#")
        cron_after = out.strip().splitlines()[-1].strip() if ok else "?"
        check("a reinstall keeps exactly one probe cron line",
              cron_after == "1" and cron_before == "1",
              f"before={cron_before!r} after={cron_after!r}")
        ok, out = ser.command("uci get 9router-go.main.jwt_secret", "#")
        secret_after = out.strip().splitlines()[-1].strip() if ok else ""
        check("a reinstall keeps the generated JWT secret",
              secret_before != "" and secret_before == secret_after,
              f"before={len(secret_before)} chars, after={len(secret_after)} chars")
        ok, out = ser.command_retry(
            "/etc/init.d/9router-go status", r"is running", 8)
        check("the service is running after a reinstall", ok, out)
        # --- open_lan: the LAN rule is added and removed by a restart ---
        ser.command("uci set 9router-go.main.open_lan=1; uci commit 9router-go", "#")
        ser.command("/etc/init.d/9router-go restart; sleep 2", "#")
        ok, out = ser.command(
            "uci show firewall | grep -c 9router-go-allow-lan", "#")
        check("open_lan=1 adds one LAN rule",
              ok and out.strip().splitlines()[-1].strip() == "1", out)
        ser.command("uci set 9router-go.main.open_lan=0; uci commit 9router-go", "#")
        ser.command("/etc/init.d/9router-go restart; sleep 2", "#")
        ok, out = ser.command(
            "uci show firewall | grep -c 9router-go-allow-lan", "#")
        check("open_lan=0 removes it again",
              ok and out.strip().splitlines()[-1].strip() == "0", out)
        # --- removal leaves no daemon, no cron line, no rule, and the database ---
        # luci-app-9router-go Depends on the gateway, so it has to be removed
        # first or opkg refuses with a dependency error.
        ser.command("opkg remove luci-app-9router-go 2>&1 | tail -2", "#")
        ok, out = ser.command("opkg remove 9router-go 2>&1 | tail -3", "#")
        check("opkg remove 9router-go", ok and "Collected errors" not in out, out)
        ok, out = ser.command(
            f"wget -qO- http://127.0.0.1:{args.port}/health >/dev/null 2>&1; echo rc=$?", "#")
        check("removal stops the gateway",
              "rc=0" not in out.strip().splitlines()[-1], out)
        ok, out = ser.command(
            "grep -q 9router-go-healthcheck /etc/crontabs/root; echo rc=$?", "#")
        check("removal clears the probe cron entry",
              "rc=1" in out.strip().splitlines()[-1], out)
        ok, out = ser.command(
            "uci show firewall | grep -c 9router-go-allow-lan", "#")
        check("removal clears the LAN rule",
              ok and out.strip().splitlines()[-1].strip() == "0", out)
        ok, out = ser.command(
            "test -f /opt/9router/db/data.sqlite && echo DBKEPT", "#")
        check("removal keeps the database", ok and "DBKEPT" in out, out)

    except BootFailure:
        # The summary below reports the failure and exits non-zero; this handler
        # exists so the `finally` teardown runs and control reaches the summary
        # instead of unwinding out of main with no report.
        pass

    finally:
        proc.terminate()
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()

    print("\n==== summary ====", flush=True)
    if failures:
        print(f"FAILED: {len(failures)} check(s): {', '.join(failures)}")
        print("\n---- last serial output ----")
        print(ser.dump()[-4000:])
        sys.exit(1)
    print("ALL CHECKS PASSED")


if __name__ == "__main__":
    main()
