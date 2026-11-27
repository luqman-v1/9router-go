#!/usr/bin/env python3
"""Exercise the OpenWrt helpers' shell logic without a router.

`openwrt-vm-test.py` proves the package installs and starts on a real 18.06
image, but it costs a 273 MB download and a VM boot, so it runs once per PR and
cannot afford to run a helper three times to check an idempotency bug. That gap
is how the firewall rule shipped non-idempotent: `uci add` creates an anonymous
section, so a lookup by `firewall.<name>` never matched and every start appended
another rule.

This harness copies the packaged scripts into a temp dir, points their `uci` and
`wget` calls at stubs by absolute path, and drives them through the settings a
router would supply. Running the real script text is the point: a rewrite would
test a second implementation nobody ships.

The stub `uci` keeps a real config file and mimics the parts of uci the scripts
use (`add` anonymous sections, `set` on `@rule[-1]` and named sections, `get`,
`delete`, `show`, `commit`). It is a logic test, not a claim about uci fidelity.

Usage:
    python3 scripts/openwrt-shell-harness.py [--src openwrt]
"""

import argparse
import os
import re
import shutil
import subprocess
import sys
import tempfile

UCI_STUB = r'''import os, re, sys

db = os.environ["UCI_DB"]

def load():
    rows = []
    if os.path.exists(db):
        for line in open(db):
            line = line.rstrip("\n")
            if not line or "=" not in line or "." not in line:
                continue
            sect, rest = line.split(".", 1)
            if "=" not in rest:
                continue
            opt, val = rest.split("=", 1)
            rows.append([sect, opt, val])
    return rows

def save(rows):
    with open(db, "w") as fh:
        for sect, opt, val in rows:
            fh.write("%s.%s=%s\n" % (sect, opt, val))

def order(rows):
    seen = []
    for sect, _, _ in rows:
        if sect not in seen:
            seen.append(sect)
    return seen

def resolve(rows, ref):
    # The scripts write both `@rule[-1]` and `firewall.@rule[-1]`, and address
    # existing sections as `firewall.cfg0001`. Strip the config prefix once so
    # every form resolves against the section ids the stub stores.
    if "." in ref:
        head, tail = ref.split(".", 1)
        if head == "firewall" and (tail.startswith("@") or tail in order(rows)):
            ref = tail
    m = re.match(r"^@[^\[]+\[(-?\d+)\]$", ref)
    if m:
        secs = order(rows)
        if not secs:
            return None
        idx = int(m.group(1))
        if idx < 0:
            idx += len(secs)
        return secs[idx] if 0 <= idx < len(secs) else None
    return ref if ref in order(rows) else None

def main(argv):
    args = [a for a in argv[1:] if a != "-q"]
    if not args:
        return 1
    cmd, rest = args[0], args[1:]
    rows = load()

    if cmd == "get":
        parts = rest[0].split(".")
        if len(parts) == 1:
            return 0 if order(rows) else 1
        sect = resolve(rows, ".".join(parts[:-1]))
        if sect is None:
            return 1
        for s, o, v in rows:
            if s == sect and o == parts[-1]:
                sys.stdout.write(v + "\n")
                return 0
        return 1

    if cmd == "add":
        existing = order(rows)
        n = len([s for s in existing if s.startswith("cfg")])
        name = "cfg%04d" % (n + 1)
        rows.append([name, "__type", "rule"])
        save(rows)
        sys.stdout.write(name + "\n")
        return 0

    if cmd == "set":
        # uci accepts both `set key=value` (the form the scripts use) and
        # `set key value`; handle both so the stub is not the reason a test fails.
        if "=" in rest[0]:
            target, value = rest[0].split("=", 1)
        else:
            target, value = rest[0], rest[1]
        parts = target.split(".")
        if len(parts) == 2:
            scope, opt = parts
        else:
            scope, opt = ".".join(parts[:-1]), parts[-1]
        sect = resolve(rows, scope)
        if sect is None:
            return 1
        for row in rows:
            if row[0] == sect and row[1] == opt:
                row[2] = value
                break
        else:
            rows.append([sect, opt, value])
        save(rows)
        return 0

    if cmd == "delete":
        sect = resolve(rows, rest[0])
        if sect is None:
            return 1
        save([r for r in rows if r[0] != sect])
        return 0

    if cmd == "show":
        # Every section in this stub belongs to the one config, so `show` prints
        # them all with the config name prefix, exactly as `uci show firewall`
        # does. Filtering by the section name here was wrong: sections are
        # anonymous ids (cfgNNNN), never equal to the config name.
        scope = rest[0] if rest else "firewall"
        for s, o, v in rows:
            if o == "__type":
                continue
            sys.stdout.write("%s.%s.%s='%s'\n" % (scope, s, o, v))
        return 0

    if cmd == "commit":
        return 0

    return 1

sys.exit(main(sys.argv))
'''

# Resolve the interpreter once, in Python, and bake it into the wrapper. This
# removes PATH lookup from the equation entirely, which matters on Windows where
# a POSIX shell's PATH handling differs from the process environment's.
UCI_WRAPPER = '''#!/bin/sh
exec "{python}" "{stub}" "$@"
'''

WGET_WRAPPER = '''#!/bin/sh
exit "$(cat "$WGET_STATUS")"
'''

LOGGER_WRAPPER = '''#!/bin/sh
exit 0
'''


def write(path, text, mode=0o755):
    with open(path, "w", newline="\n") as fh:
        fh.write(text)
    os.chmod(path, mode)


def sh_path(path):
    """Forward-slash form of a path.

    A Windows path reaching `eval` inside the scripts loses its backslashes:
    eval treats backslash as an escape, so "C:\\Users\\x" becomes "C:Usersx".
    Every path handed to generated shell text goes through here. POSIX paths
    pass through unchanged.
    """
    return path.replace(os.sep, "/").replace("\\", "/")


def run(cmd, env, cwd=None):
    return subprocess.run(cmd, env=env, cwd=cwd, capture_output=True, text=True)


def sections(db):
    out = {}
    if not os.path.exists(db):
        return out
    for line in open(db):
        line = line.rstrip("\n")
        if "=" not in line or "." not in line:
            continue
        sect, rest = line.split(".", 1)
        if "=" not in rest:
            continue
        opt, val = rest.split("=", 1)
        out.setdefault(sect, {})
        if opt != "__type":
            out[sect][opt] = val
    return out


def rules_named(db, name):
    return sum(1 for o in sections(db).values() if o.get("name") == name)


def ports_named(db, name):
    return [o.get("dest_port") for o in sections(db).values() if o.get("name") == name]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--src", default="openwrt", help="packaging directory")
    args = ap.parse_args()
    src = os.path.abspath(args.src)
    if not os.path.isdir(src):
        sys.exit("no packaging directory at %s" % src)

    sh = shutil.which("sh")
    if not sh:
        print("SKIP: no POSIX shell on PATH")
        return 0

    cases = []

    def case(name, ok, detail=""):
        cases.append((name, bool(ok), detail))

    tmp = tempfile.mkdtemp(prefix="openwrt-harness-")
    try:
        bindir = os.path.join(tmp, "bin")
        os.makedirs(bindir)
        stub = os.path.join(bindir, "uci_stub.py")
        write(stub, UCI_STUB, 0o644)
        uci = os.path.join(bindir, "uci")
        write(uci, UCI_WRAPPER.format(python=sh_path(sys.executable), stub=sh_path(stub)))
        wget = os.path.join(bindir, "wget")
        write(wget, WGET_WRAPPER)
        logger_stub = os.path.join(bindir, "logger")
        write(logger_stub, LOGGER_WRAPPER)

        db = os.path.join(tmp, "uci.db")
        empty = os.path.join(tmp, "empty.db")
        write(empty, "", 0o644)
        data_dir = os.path.join(tmp, "data")
        os.makedirs(os.path.join(data_dir, "db"))
        status = os.path.join(tmp, "wget.status")
        write(status, "1\n", 0o644)
        # The init/prerm guard now tests for the firewall config file rather
        # than `uci get firewall`, so give the harness one.
        firewall_cfg = os.path.join(tmp, "firewall")
        write(firewall_cfg, "config defaults\n\toption input 'REJECT'\n", 0o644)

        env = dict(os.environ)
        env["UCI_DB"] = db
        env["WGET_STATUS"] = status

        init_text = open(os.path.join(src, "9router-go.init")).read()
        body = init_text.split("USE_PROCD=1", 1)[1].split("service_triggers()", 1)[0]

        prelude = (
            "procd_open_instance() { :; }\n"
            "procd_close_instance() { :; }\n"
            "procd_set_param() { :; }\n"
            "procd_add_reload_trigger() { :; }\n"
            "logger() { :; }\n"
            "config_load() { :; }\n"
            'config_get() { eval "$1=\\${CFG_$3:-$4}"; }\n'
            'config_get_bool() { eval "$1=\\${CFG_$3:-$4}"; }\n'
        )

        # Route the script's `uci`/`wget`/`firewall reload` calls at the stubs by
        # rewriting those tokens, so nothing depends on PATH inside the shell.
        def retarget(text):
            text = text.replace("uci -q ", '"%s" -q ' % sh_path(uci))
            text = text.replace("command -v uci", "test -x %s" % sh_path(uci))
            text = text.replace("command -v wget", "test -x %s" % sh_path(wget))
            text = text.replace("command -v tar", "test -x %s" % sh_path(shutil.which("tar") or "/bin/tar"))
            text = re.sub(r"\blogger -t\b", '"%s" -t' % sh_path(logger_stub), text)
            text = text.replace("wget -q ", '"%s" -q ' % sh_path(wget))
            text = text.replace("/etc/config/firewall", sh_path(firewall_cfg))
            text = re.sub(r"/etc/init\.d/firewall\s+reload", "true", text)
            text = re.sub(r"/etc/init\.d/cron\s+restart", "true", text)
            return text

        def start(open_lan, port):
            injected = (
                "CFG_enabled=1\n"
                "CFG_port=%s\n"
                "CFG_host=''\n"
                "CFG_data_dir=%s\n"
                "CFG_auto_update=0\n"
                "CFG_jwt_secret=''\n"
                "CFG_log_level=info\n"
                "CFG_healthcheck=1\n"
                "CFG_open_lan=%s\n" % (port, data_dir, open_lan)
            )
            path = os.path.join(tmp, "start.sh")
            write(path, prelude + injected + retarget(body) + "\nstart_service\n")
            return run([sh, path], env, cwd=tmp)

        # 1. three starts create exactly one rule
        ok, detail = True, ""
        for _ in range(3):
            r = start(1, "20130")
            if r.returncode != 0:
                ok, detail = False, "exit %d: %s" % (r.returncode, r.stderr[-300:])
                break
        n = rules_named(db, "9router-go-allow-lan")
        case("three starts create exactly one rule", ok and n == 1,
             detail or "found %d rules" % n)

        # 2. a port change updates the rule instead of adding another
        start(1, "20131")
        ps = ports_named(db, "9router-go-allow-lan")
        case("port change syncs the existing rule", ps == ["20131"], "ports: %r" % (ps,))
        case("port change does not duplicate the rule",
             rules_named(db, "9router-go-allow-lan") == 1,
             "found %d rules" % rules_named(db, "9router-go-allow-lan"))

        # 3. open_lan=0 removes the rule
        start(0, "20131")
        n = rules_named(db, "9router-go-allow-lan")
        case("open_lan=0 removes the rule", n == 0, "found %d rules" % n)

        # 4. duplicates left by an older package are all removed
        with open(db, "a") as fh:
            fh.write("cfg9000.name=9router-go-allow-lan\n")
            fh.write("cfg9000.dest_port=20130\n")
            fh.write("cfg9001.name=9router-go-allow-lan\n")
            fh.write("cfg9001.dest_port=20130\n")
        start(0, "20131")
        n = rules_named(db, "9router-go-allow-lan")
        case("open_lan=0 removes legacy duplicates", n == 0, "found %d rules" % n)

        # 5. the probe survives a config that predates every option
        hc = retarget(open(os.path.join(src, "9router-go-healthcheck")).read())
        run_dir = os.path.join(tmp, "run")
        os.makedirs(run_dir, exist_ok=True)
        hc = re.sub(r"RUN_DIR=.*", lambda m: "RUN_DIR=" + sh_path(run_dir), hc)
        hc_path = os.path.join(tmp, "healthcheck.sh")
        write(hc_path, hc)

        env2 = dict(env)
        env2["UCI_DB"] = empty
        r = run([sh, hc_path], env2)
        case("healthcheck exits 0 on a config without options", r.returncode == 0,
             "exit %d, stderr %r" % (r.returncode, r.stderr[-200:]))

        # 6. a failed probe increments the counter
        counter = os.path.join(run_dir, "9router-go-healthcheck.failures")
        if os.path.exists(counter):
            os.remove(counter)
        write(status, "1\n", 0o644)
        run([sh, hc_path], env2)
        got = open(counter).read().strip() if os.path.exists(counter) else None
        case("a failed probe increments the counter", got == "1", "counter: %r" % (got,))

        # 7. a healthy probe clears the counter
        write(status, "0\n", 0o644)
        run([sh, hc_path], env2)
        case("a healthy probe clears the counter", not os.path.exists(counter),
             "counter file still present")
    finally:
        shutil.rmtree(tmp, ignore_errors=True)

    failed = [c for c in cases if not c[1]]
    for name, ok, detail in cases:
        print("[%s] %s%s" % ("PASS" if ok else "FAIL", name,
                             "" if ok else " -- " + detail))
    print("\n%d/%d passed" % (len(cases) - len(failed), len(cases)))
    if failed:
        print("FAILED: " + ", ".join(n for n, _, _ in failed))
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
