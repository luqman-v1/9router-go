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
        elif tail in order(rows):
            # Any other config (`9router-go.main.data_dir`): the harness stores
            # those as a section named after the UCI section, so the lookup only
            # has to drop the config prefix.
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
# A test can force one option value without teaching the stub every
# config shape: UCI_PORT answers the gateway port lookup, so the health
# probe can be run against a value uci would never store.
case "$*" in
  *9router-go.main.port*)
    if [ -n "$UCI_PORT" ]; then printf '%s\n' "$UCI_PORT"; exit 0; fi ;;
esac
exec "{python}" "{stub}" "$@"
'''

WGET_WRAPPER = '''#!/bin/sh
printf '%s\n' "$*" > "$WGET_STATUS.args"
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
        # The health probe asks procd whether the service is running, and
        # restarts it when the threshold is reached. Both are stubbed: a
        # flag file answers `status`, and `restart` appends to a log.
        running_flag = os.path.join(tmp, "running")
        restart_log = os.path.join(tmp, "restarts.log")
        initd_stub = os.path.join(tmp, "initd-9router-go")
        # The stub's `status` output is read from INITD_STATUS_OUT so the
        # harness can present each of the verdicts a real rc.common release
        # produces: bare `running` on 21.02+, `not running` when the service
        # is stopped, and the help text a 18.06/19.07 `status` call returns
        # because that action is not registered there.
        write(initd_stub,
              "#!/bin/sh\n"
              'case "$1" in\n'
              '  status)\n'
              "    if [ -n \"${INITD_STATUS_OUT+x}\" ]; then printf '%s\\n' \"$INITD_STATUS_OUT\";\n"
              '    elif [ -f "' + sh_path(running_flag) + '" ]; then echo running; else echo "not running"; fi ;;\n'
              '  restart) echo x >> "' + sh_path(restart_log) + '" ;;\n'
              "esac\n")
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
            "procd_set_param() { printf '%s\\n' \"$*\" >> \"${PROCD_LOG:-/dev/null}\"; }\n"
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
            text = text.replace("/etc/init.d/9router-go", sh_path(initd_stub))
            return text

        def start(open_lan, port, mem_limit=0, nofile_limit=4096):
            injected = (
                "CFG_enabled=1\n"
                "CFG_port=%s\n"
                "CFG_host=''\n"
                "CFG_data_dir=%s\n"
                "CFG_auto_update=0\n"
                "CFG_jwt_secret=''\n"
                "CFG_log_level=info\n"
                "CFG_healthcheck=1\n"
                "CFG_open_lan=%s\n"
                "CFG_mem_limit=%s\n"
                "CFG_nofile_limit=%s\n" % (port, data_dir, open_lan, mem_limit, nofile_limit)
            )
            path = os.path.join(tmp, "start.sh")
            write(path, prelude + injected + retarget(body) + "\nstart_service\n")
            procd_log = os.path.join(tmp, "procd.log")
            open(procd_log, "w").close()
            env2 = dict(env)
            env2["PROCD_LOG"] = procd_log
            r = run([sh, path], env2, cwd=tmp)
            r.procd = open(procd_log).read()
            return r

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
        # 4b. resource limits reach procd, and a malformed value reaches nothing.
        # GOMEMLIMIT is a soft heap ceiling rather than an address-space rlimit on
        # purpose: Go reserves a large virtual address space, so RLIMIT_AS kills
        # the process at startup. A non-numeric option must emit no variable at
        # all, because Go treats a malformed GOMEMLIMIT as a fatal start-up error.
        r = start(0, "20131", mem_limit=64, nofile_limit=4096)
        case("mem_limit becomes GOMEMLIMIT", "env GOMEMLIMIT=64MiB" in r.procd,
             "procd params: %r" % (r.procd,))
        case("nofile_limit sets soft and hard", "limits nofile=4096 4096" in r.procd,
             "procd params: %r" % (r.procd,))
        r = start(0, "20131", mem_limit=0, nofile_limit=0)
        case("zero limits emit nothing", "GOMEMLIMIT" not in r.procd and "limits" not in r.procd,
             "procd params: %r" % (r.procd,))
        r = start(0, "20131", mem_limit="lots", nofile_limit="many")
        case("malformed limits emit nothing",
             "GOMEMLIMIT" not in r.procd and "limits" not in r.procd,
             "procd params: %r" % (r.procd,))
        case("malformed limits do not abort start", r.returncode == 0,
             "exit %d, stderr %r" % (r.returncode, r.stderr[-200:]))
        # An oversized value reaches neither procd nor the Go runtime untouched:
        # Go throws `fatal error: malformed GOMEMLIMIT` on a number too large to
        # parse (9000000000000 MiB), and Linux rejects an NR_OPEN above
        # 1048576 with EINVAL — either way the service would not start. The
        # guard clamps both to the largest safe value and logs the change.
        r = start(0, "20131", mem_limit=9999999999999999, nofile_limit=9999999999999999)
        case("oversized mem_limit clamped",
             "env GOMEMLIMIT=1048576MiB" in r.procd,
             "procd params: %r" % (r.procd,))
        case("oversized nofile_limit clamped",
             "limits nofile=1048576 1048576" in r.procd,
             "procd params: %r" % (r.procd,))
        case("oversized limits do not abort start", r.returncode == 0,
             "exit %d, stderr %r" % (r.returncode, r.stderr[-200:]))
        r = start(0, "20131", mem_limit=9000000000000, nofile_limit=0)
        case("large mem_limit at the parser edge clamped",
             "env GOMEMLIMIT=1048576MiB" in r.procd,
             "procd params: %r" % (r.procd,))
        r = start(0, "20131", mem_limit=64, nofile_limit=1048576)
        case("nofile_limit at the ceiling passes through unchanged",
             "limits nofile=1048576 1048576" in r.procd and "GOMEMLIMIT=64MiB" in r.procd,
             "procd params: %r" % (r.procd,))

        # 5. the probe survives a config that predates every option
        hc = retarget(open(os.path.join(src, "9router-go-healthcheck")).read())
        run_dir = os.path.join(tmp, "run")
        os.makedirs(run_dir, exist_ok=True)
        hc = re.sub(r"RUN_DIR=.*", lambda m: "RUN_DIR=" + sh_path(run_dir), hc)
        hc_path = os.path.join(tmp, "healthcheck.sh")
        write(hc_path, hc)

        env2 = dict(env)
        env2["UCI_DB"] = empty
        write(running_flag, "1\n", 0o644)
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
        # 7b. a deliberately stopped service is not a failure. backup stops
        # the gateway to copy the database; counting that as a failed probe
        # could reach the threshold and restart it mid-copy.
        os.remove(running_flag)
        write(status, "1\n", 0o644)
        if os.path.exists(counter):
            os.remove(counter)
        run([sh, hc_path], env2)
        case("a stopped service is not counted as a failed probe",
             not os.path.exists(counter), "counter: %r" % (
                 open(counter).read() if os.path.exists(counter) else None,))
        # 7b'. the gate must only skip on a verdict that names the service
        # stopped. 18.06/19.07 do not register `status` as an rc.common
        # action, so the call falls through to `help` and its first line is
        # not `running`. Skipping on that would silently disable the
        # watchdog on the oldest supported releases, so anything the stub
        # cannot classify as "not running" from procd is treated as "probe
        # anyway". Each verdict below is pinned against the counter the
        # probe writes.
        env4 = dict(env2)
        write(status, "1\n", 0o644)
        if os.path.exists(counter):
            os.remove(counter)
        # (a) a bare `running` verdict, as 21.02+ emits.
        env4["INITD_STATUS_OUT"] = "running"
        run([sh, hc_path], env4)
        case("a bare 'running' verdict probes", os.path.exists(counter),
             "counter: %r" % (open(counter).read() if os.path.exists(counter) else None,))
        # (b) the 18.06/19.07 help output when `status` is not an action.
        # The first line of `rc.common help` on those releases does not
        # name the service state.
        env4["INITD_STATUS_OUT"] = "Usage: /etc/init.d/9router-go [command]"
        if os.path.exists(counter):
            os.remove(counter)
        run([sh, hc_path], env4)
        case("an unrecognised status verdict (18.06 help) still probes",
             os.path.exists(counter),
             "counter: %r" % (open(counter).read() if os.path.exists(counter) else None,))
        # (c) an explicit `not running` verdict still skips, because it is
        # the one case the gate exists to catch.
        env4["INITD_STATUS_OUT"] = "not running"
        if os.path.exists(counter):
            os.remove(counter)
        run([sh, hc_path], env4)
        case("a 'not running' verdict skips the probe",
             not os.path.exists(counter),
             "counter: %r" % (open(counter).read() if os.path.exists(counter) else None,))
        # (d) a `stopped` verdict also skips — 19.07 procd's status_service
        # reports `stopped` for a service that has exited.
        env4["INITD_STATUS_OUT"] = "stopped"
        if os.path.exists(counter):
            os.remove(counter)
        run([sh, hc_path], env4)
        case("a 'stopped' verdict skips the probe",
             not os.path.exists(counter),
             "counter: %r" % (open(counter).read() if os.path.exists(counter) else None,))
        # Restore the flag file the remaining cases assume.
        write(running_flag, "1\n", 0o644)
        # 7c. a non-numeric port must not turn every probe into a failure.
        # The URL is what the stub records, so this asserts the fallback the
        # script applies rather than the probe outcome.
        write(running_flag, "1\n", 0o644)
        env3 = dict(env)
        env3["UCI_PORT"] = "not-a-port"
        run([sh, hc_path], env3)
        probed = open(status + ".args").read().strip() if os.path.exists(status + ".args") else ""
        case("a non-numeric port falls back to the default URL",
             probed.endswith(":20130/health"), "probed: %r" % (probed,))
        # 8. 9router-go-backup and the SQLite WAL.
        #
        # The gateway sets `journal_mode = WAL` (internal/db/client.go), which is
        # persistent in the database header. A process that was killed instead
        # of stopped leaves a non-empty -wal holding committed frames the
        # database file does not, so a copy of the database alone is silently
        # missing the most recent writes; and a -wal left beside a database it
        # does not belong to gets replayed into the restored file. Both
        # directions are exercised here because neither is visible without a
        # router: the failure is a clean exit code and a wrong database.
        #
        # The stub resolves `9router-go.main.data_dir` through a section named
        # `main`, which is the shape it can store (see resolve() in UCI_STUB).
        with open(db, "a") as fh:
            fh.write("main.data_dir=%s\n" % sh_path(data_dir))
        helper_env = dict(env)
        pidfile = os.path.join(tmp, "run", "9router-go.pid")
        os.makedirs(os.path.dirname(pidfile), exist_ok=True)
        state_dir = os.path.join(tmp, "state")
        os.makedirs(state_dir, exist_ok=True)

        def retarget_helper(text):
            text = retarget(text)
            text = text.replace("/var/run/9router-go.pid", sh_path(pidfile))
            text = text.replace("/etc/9router-go", sh_path(state_dir))
            text = re.sub(r"/etc/init\.d/9router-go\s+(stop|start)", "true", text)
            return text

        bk_path = os.path.join(tmp, "9router-go-backup")
        write(bk_path, retarget_helper(open(os.path.join(src, "9router-go-backup")).read()))
        db_file = os.path.join(data_dir, "db", "data.sqlite")
        out = os.path.join(state_dir, "backup", "data.sqlite")
        # a. save carries an uncheckpointed -wal beside the database
        write(db_file, "checkpointed\n", 0o644)
        write(db_file + "-wal", "frames-from-a-killed-process\n", 0o644)
        run([sh, bk_path, "save", out], helper_env, cwd=tmp)
        case("save carries an uncheckpointed -wal",
             os.path.exists(out + "-wal")
             and open(out + "-wal").read() == "frames-from-a-killed-process\n",
             "wal: %r" % (os.path.exists(out + "-wal"),))
        # b. save does not leave a -wal from an earlier backup behind
        os.remove(db_file + "-wal")
        run([sh, bk_path, "save", out], helper_env, cwd=tmp)
        case("save drops a stale backup -wal", not os.path.exists(out + "-wal"),
             "stale -wal still present")
        # c. restore drops the -wal and -shm of the database it replaces
        write(db_file, "previous-database\n", 0o644)
        write(db_file + "-wal", "frames-for-the-previous-database\n", 0o644)
        write(db_file + "-shm", "shared-memory-index\n", 0o644)
        incoming = os.path.join(tmp, "incoming.sqlite")
        write(incoming, "restored-database\n", 0o644)
        run([sh, bk_path, "restore", incoming], helper_env, cwd=tmp)
        case("restore drops the replaced database's -wal and -shm",
             not os.path.exists(db_file + "-wal") and not os.path.exists(db_file + "-shm"),
             "wal=%r shm=%r" % (os.path.exists(db_file + "-wal"),
                                os.path.exists(db_file + "-shm")))
        case("restore writes the incoming database",
             open(db_file).read() == "restored-database\n",
             "db: %r" % (open(db_file).read()[:40],))
        case("restore keeps the replaced database for rollback",
             os.path.exists(db_file + ".before-restore"),
             "no .before-restore file")
        # d. restore puts a saved -wal back beside the database
        write(incoming + "-wal", "frames-from-the-backup\n", 0o644)
        run([sh, bk_path, "restore", incoming], helper_env, cwd=tmp)
        case("restore brings a saved -wal back",
             os.path.exists(db_file + "-wal")
             and open(db_file + "-wal").read() == "frames-from-the-backup\n",
             "wal: %r" % (os.path.exists(db_file + "-wal"),))
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
