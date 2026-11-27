package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/urfave/cli/v2"

	"9router/proxy/internal/config"
	"9router/proxy/internal/daemon"
)

// daemonURL is the address the background daemon is expected to answer on.
//
// The port comes from the same resolved config the server binds, not from
// os.Getenv. A deployment that sets PORT in .env — the common case, and every
// compose file — was probed on 20130 while the child bound its real port, so
// waitHealthy polled a dead address for the full 20s StartupTimeout and then
// reported a start that had actually succeeded as a failure.
func daemonURL() string {
	cfg := config.LoadConfig()
	if cfg.Port <= 0 {
		return "http://127.0.0.1:20130"
	}
	port := strconv.Itoa(cfg.Port)
	// BIND_ADDR defaults to a wildcard bind, which is not dialable; only an
	// explicit loopback address is used to build the probe URL.
	if host := strings.TrimSpace(cfg.Host); host != "" && !isWildcardHost(host) {
		return "http://" + joinHostPort(host, port)
	}
	return "http://127.0.0.1:" + port
}

// isWildcardHost reports whether a bind address accepts every interface, in
// which case probing it would target 0.0.0.0 rather than the local listener.
func isWildcardHost(host string) bool {
	switch host {
	case "0.0.0.0", "::", "[::]", "*":
		return true
	}
	return false
}

// joinHostPort wraps bare IPv6 literals in brackets.
func joinHostPort(host, port string) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		return "[" + host + "]:" + port
	}
	return host + ":" + port
}

// daemonStart is the seam the daemon-command tests swap to simulate the two
// "already running" shapes Start can return — the same pattern menu.go uses
// with menuDaemonStart.
var daemonStart = daemon.Start

// startDetached runs the server as a background daemon and returns, leaving
// the terminal free.
func startDetached(cCtx *cli.Context) error {
	url := daemonURL()
	res, err := daemonStart(url, forwardArgs(cCtx))
	// errors.Is rather than ==: Start returns the sentinel wrapped when the
	// port is busy but no PID file names the owner (`%w on %s (…) — stop it
	// first…`), and == missed that variant, so the friendly "already running"
	// message degraded to the raw error. menu.go's two comparisons already
	// use errors.Is for the same reason.
	if errors.Is(err, daemon.ErrAlreadyRunning) {
		fmt.Printf("9router-go already running (pid %d) → %s\n", res.PID, res.URL)
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Printf("9router-go started in background (pid %d)\n", res.PID)
	fmt.Printf("  Dashboard: %s\n", res.URL)
	fmt.Printf("  Log:       %s\n", daemon.LogPath())
	fmt.Println("  Stop with: 9router-go stop")
	return nil
}

// stopDetached stops the background daemon.
func stopDetached(_ *cli.Context) error {
	res, err := daemon.Stop()
	if err != nil {
		return err
	}
	if !res.Stopped {
		fmt.Println("9router-go is not running in background.")
		return nil
	}
	fmt.Printf("9router-go stopped (pid %d).\n", res.PID)
	return nil
}

// restartDetached replaces a running daemon with a fresh one.
func restartDetached(cCtx *cli.Context) error {
	url := daemonURL()
	res, err := daemon.Restart(url, forwardArgs(cCtx))
	if err != nil {
		return err
	}
	fmt.Printf("9router-go restarted in background (pid %d)\n", res.PID)
	fmt.Printf("  Dashboard: %s\n", res.URL)
	fmt.Printf("  Log:       %s\n", daemon.LogPath())
	return nil
}

// statusDetached reports whether a background daemon is live.
func statusDetached(_ *cli.Context) error {
	pid := daemon.RunningPID()
	if pid == 0 {
		fmt.Println("9router-go is not running in background.")
		return nil
	}
	fmt.Printf("9router-go is running in background (pid %d)\n", pid)
	fmt.Printf("  Dashboard: %s\n", daemonURL())
	fmt.Printf("  Log:       %s\n", daemon.LogPath())
	return nil
}

// logsDetached prints the tail of the background run log.
func logsDetached(cCtx *cli.Context) error {
	lines := 40
	if cCtx.IsSet("lines") {
		if v := cCtx.Int("lines"); v > 0 {
			lines = v
		}
	}
	tail := daemon.LogTail(lines)
	if tail == "" {
		fmt.Printf("No background log yet at %s\n", daemon.LogPath())
		return nil
	}
	fmt.Println(tail)
	return nil
}

// forwardArgs returns the global flags to hand to the background child, as
// `--name=value` pairs so an explicit false survives the hand-off.
// Sub-command arguments are dropped: the child only runs the server.
func forwardArgs(cCtx *cli.Context) []string {
	names := []string{"rtk", "caveman", "ponytail", "adhd", "auto-update", "no-injection-guard"}
	args := make([]string, 0, len(names))
	for _, name := range names {
		if !cCtx.IsSet(name) {
			continue
		}
		args = append(args, fmt.Sprintf("--%s=%t", name, cCtx.Bool(name)))
	}
	return args
}
