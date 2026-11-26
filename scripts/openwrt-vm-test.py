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
            time.sleep(0.05)
        return False

    def send(self, line):
        self.proc.stdin.write(line.encode() + b"\n")
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
    proc = subprocess.Popen(
        qargs, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    ser = Serial(proc)

    failures = []

    def check(name, ok, out):
        status = "PASS" if ok else "FAIL"
        print(f"[{status}] {name}", flush=True)
        if not ok:
            failures.append(name)
            print("---- output ----")
            print(out[-3000:])
            print("----------------", flush=True)

    try:
        # --- boot to login ---
        ok = ser.wait("login:", SERIAL_TIMEOUT)
        check("boot to login prompt", ok, ser.dump())
        if not ok:
            return

        ser.send("root")
        # Wait for the root prompt (root@host:~# ), not a bare '#', so a stray
        # '#' in the boot banner cannot be mistaken for a successful login.
        ok = ser.wait("root@", CMD_TIMEOUT)
        check("root login (no password)", ok, ser.dump())
        if not ok:
            return
        # The login prompt (root@OpenWrt:~# ) is still partly in the buffer.
        # Flush it so the first command's wait('#') does not match the stale
        # prompt and return before the command has run.
        with ser.lock:
            ser.buf = b""

        # --- install the gateway package ---
        ok, out = ser.command(
            f"wget -q -O /tmp/gw.ipk http://10.0.2.2:{http_port}/"
            f"9router-go_{args.version}_x86_64.ipk", "#")
        check("fetch gateway ipk over slirp", ok, out)

        ok, out = ser.command("opkg install /tmp/gw.ipk", "#")
        check("opkg install 9router-go",
              ok and "Collected errors" not in out and "Configuring 9router-go" in out, out)

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
        ok, out = ser.command(
            f"wget -q -O /tmp/luci.ipk http://10.0.2.2:{http_port}/"
            f"luci-app-9router-go_{args.version}_all.ipk", "#")
        check("fetch luci ipk", ok, out)

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
