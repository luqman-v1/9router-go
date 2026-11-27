package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/urfave/cli/v2"

	"9router/proxy/internal/config"
	"9router/proxy/internal/daemon"
)

// doctorCommand reports why the gateway is not reachable, rather than only
// that it is not running.
//
// `status` answers one question: is a daemon alive. When it answers no, the
// operator is left with a port that may be held by a foreign process, a pid
// file that may be stale, and a log that may carry the real cause. Every one
// of those is already known to the code that starts the daemon; doctor reads
// the same probes and prints them together with the fix for the platform.
func doctorCommand() *cli.Command {
	return &cli.Command{
		Name:  "doctor",
		Usage: "Diagnose why the gateway is not reachable, with a platform-aware fix",
		Flags: []cli.Flag{
			&cli.IntFlag{Name: "port", Usage: "port to diagnose (overrides PORT and .env)"},
		},
		Action: func(cCtx *cli.Context) error {
			return runDoctorWithPort(cCtx.Int("port"), cCtx.IsSet("port"))
		},
	}
}

// DoctorCheck is one diagnosis line: what was observed, whether it is wrong,
// and what to run next. String renders it as the two lines the CLI prints.
type DoctorCheck struct {
	Name   string
	OK     bool
	Detail string
	Fix    string
}

// Format renders a check as an aligned status line plus an indented fix.
func (c DoctorCheck) Format() string {
	mark := "ok  "
	if !c.OK {
		mark = "FAIL"
	}
	out := fmt.Sprintf("  [%s] %-14s %s", mark, c.Name, c.Detail)
	if !c.OK && c.Fix != "" {
		out += "\n         fix: " + c.Fix
	}
	return out
}

// runDoctor prints every check and returns a non-nil error when any failed, so
// `9router-go doctor` exits non-zero in a script the same way it reads in a
// terminal.
func runDoctor(portOverride int) error {
	return runDoctorWithPort(portOverride, portOverride != 0)
}

func runDoctorWithPort(portOverride int, portSet bool) error {
	cfg := config.LoadConfig()
	probePort := portOverride
	if !portSet && probePort <= 0 {
		probePort = cfg.Port
	}
	url := urlForPort(cfg, probePort)

	checks := []DoctorCheck{
		checkConfig(cfg, probePort, url),
		checkDaemon(url),
		checkPort(url),
		checkDataDir(cfg),
		checkDatabase(cfg),
		checkLog(),
	}

	fmt.Printf("9router-go doctor\n  probe: %s\n\n", url)
	failed := 0
	for _, c := range checks {
		fmt.Println(c.Format())
		if !c.OK {
			failed++
		}
	}

	if failed == 0 {
		fmt.Printf("\nAll %d checks passed.\n", len(checks))
		return nil
	}
	fmt.Printf("\n%d of %d checks failed.\n", failed, len(checks))
	return fmt.Errorf("doctor: %d check(s) failed", failed)
}

// urlForPort builds the probe URL for an explicit port using the same host
// rules as daemonURL, so `--port` diagnoses the address the server would bind.
func urlForPort(cfg *config.Config, port int) string {
	if port <= 0 {
		return daemonURL()
	}
	if host := strings.TrimSpace(cfg.Host); host != "" && !isWildcardHost(host) {
		return "http://" + joinHostPort(host, itoa(port))
	}
	return "http://127.0.0.1:" + itoa(port)
}

func itoa(v int) string { return fmt.Sprintf("%d", v) }

// checkConfig reports the resolved port and the source that decided it. A
// mismatched probe port against a live listener is the single most common
// "status says not running" report: the CLI probed 20130 while .env had
// another PORT.
func checkConfig(cfg *config.Config, port int, url string) DoctorCheck {
	c := DoctorCheck{Name: "config", OK: true}
	c.Detail = fmt.Sprintf("port %d → %s", port, url)
	if port <= 0 || port > 65535 {
		c.OK = false
		c.Detail = fmt.Sprintf("port %d is out of range", port)
		c.Fix = "set PORT to a value between 1 and 65535 in .env, or pass --port"
	}
	return c
}

// checkDaemon reports the pid claim and whether that process is still alive.
//
// A live claim is not the same as a working gateway: the process can hold the
// port and never answer, which is the wedged case checkPort names. The claim
// alone therefore does not pass this check; a claim plus an unanswered /health
// fails it, so `doctor` never prints "running" for a gateway that is not.
func checkDaemon(url string) DoctorCheck {
	c := DoctorCheck{Name: "daemon"}
	pid := runningPID()
	switch {
	case pid > 0 && !healthyAt(url):
		// A live claim is not a working gateway.
		c.Detail = fmt.Sprintf("pid %d is alive but /health does not answer: wedged", pid)
		c.Fix = "9router-go restart"
	case pid > 0:
		c.OK = true
		c.Detail = fmt.Sprintf("running (pid %d, log %s)", pid, daemon.LogPath())
	case healthyAt(url):
		c.Detail = "the port answers but no daemon owns it"
		c.Fix = "started outside this binary; stop that process before running start"
	default:
		c.Detail = "not running (no live pid claim)"
		c.Fix = "9router-go start"
	}
	return c
}

// checkPort reports which of four states the port is in, because three of them
// look identical from outside (a connection accepted, no answer) and each has a
// different fix:
//
//  1. free — nothing listening
//  2. served by the gateway — /health answers 200
//  3. held by this binary's own daemon, but /health is silent: the gateway is
//     wedged. Telling the operator to kill that pid would kill the very thing
//     `doctor` exists to recover, so the fix is restart plus the log.
//  4. held by a process this binary does not own: a foreign listener, and the
//     platform command that finds and stops it is the right advice.
//
// The pid claim is what separates 3 from 4. It is not inferred from the port:
// a wedged gateway and a foreign process both accept a TCP connection and both
// answer nothing, so only the claim can tell them apart.
// The three probes are variables so the doctor tests can reach the wedged
// branch, which needs a live pid claim with a silent listener and cannot be
// produced by running the real daemon in a unit test.
var (
	runningPID = daemon.RunningPID
	portStatus = daemon.PortStatus
	healthyAt  = daemon.Healthy
)

func checkPort(url string) DoctorCheck {
	c := DoctorCheck{Name: "port"}
	busy, owner := portStatus(url)
	pid := runningPID()
	healthy := busy && healthyAt(url)

	switch {
	case !busy:
		c.OK = true
		c.Detail = "free"
	case healthy:
		c.OK = true
		c.Detail = "served by the gateway (" + owner + ")"
		return c
	case pid > 0:
		// Our own daemon owns the port but never answered: the listener is
		// alive and the request path is not, which a restart clears and a
		// forced kill of that pid would only turn into a harder outage.
		c.OK = false
		c.Detail = fmt.Sprintf("held by this gateway's daemon (pid %d), but /health does not answer: the gateway is wedged", pid)
		c.Fix = "9router-go restart, then 9router-go logs -n 40 if it wedges again"
	default:
		c.OK = false
		c.Detail = "held by another process (" + owner + "), /health does not answer"
		c.Fix = portReleaseHint(url)
	}
	return c
}

// checkDataDir reports whether the data directory the daemon will use is
// writable. A read-only or missing directory makes every later check fail for
// a reason that is not the gateway's fault.
func checkDataDir(cfg *config.Config) DoctorCheck {
	c := DoctorCheck{Name: "data-dir", OK: true}
	dir := config.ResolveDataDir()
	c.Detail = dir
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		c.OK = false
		c.Detail = dir + " does not exist"
		c.Fix = "create it, or set DATA_DIR in .env"
		return c
	}
	probe := filepath.Join(dir, ".doctor-write-probe")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		c.OK = false
		c.Detail = dir + " is not writable"
		c.Fix = "fix the directory permissions, or point DATA_DIR somewhere writable"
		return c
	}
	_ = f.Close()
	_ = os.Remove(probe)
	return c
}

// checkDatabase reports whether the database file exists and opens. The file
// is what carries every provider, key and usage row; a missing one is worth
// naming before the server reports it as a boot failure.
func checkDatabase(cfg *config.Config) DoctorCheck {
	c := DoctorCheck{Name: "database", OK: true}
	path := cfg.DatabasePath
	if strings.HasPrefix(path, "postgres://") || strings.HasPrefix(path, "postgresql://") {
		c.OK = true
		c.Detail = "postgres (configured via DATABASE_URL)"
		return c
	}
	c.Detail = path
	fi, err := os.Stat(path)
	if err != nil {
		c.OK = false
		c.Detail += " does not exist"
		c.Fix = "the gateway creates it on first boot; run 9router-go start"
		return c
	}
	if fi.IsDir() {
		c.OK = false
		c.Detail += " is a directory"
		c.Fix = "point SQLITE_PATH or DATA_DIR to a database file rather than a directory"
		return c
	}
	f, err := os.Open(path)
	if err != nil {
		c.OK = false
		c.Detail += " exists but cannot be opened"
		c.Fix = "fix the file permissions"
		return c
	}
	_ = f.Close()
	c.Detail = fmt.Sprintf("%s (%d bytes)", path, fi.Size())
	return c
}

// checkLog reports whether the log exists, and surfaces a failed-listen marker
// when the last start died before printing its own error.
func checkLog() DoctorCheck {
	c := DoctorCheck{Name: "log"}
	path := daemon.LogPath()
	if _, err := os.Stat(path); err != nil {
		c.OK = true
		c.Detail = "no log yet at " + path
		return c
	}
	if daemon.BindFailureLogged() {
		c.OK = false
		c.Detail = "the last start failed to bind its port"
		c.Fix = "something else owns the port; see the port check, then 9router-go start"
		return c
	}
	c.OK = true
	c.Detail = path + " (9router-go logs -n 40)"
	return c
}

// portReleaseHint names the platform command that frees a port, since the
// operator's next step after "held by another process" is always to find out
// who. Both forms are the ones that work on the target OS, not a generic
// lsof suggestion that fails on Windows.
func portReleaseHint(url string) string {
	_, port, ok := daemon.SplitHostPort(url)
	if !ok {
		return "find the process listening on the port and stop it"
	}
	if isWindows() {
		return "netstat -ano | findstr :" + port + "  then  taskkill /PID <pid> /F"
	}
	return "lsof -i :" + port + "  then  kill <pid>"
}

// isWindows is a variable so the doctor test can exercise both hint forms
// without a Windows build.
var isWindows = func() bool { return runtime.GOOS == "windows" }
