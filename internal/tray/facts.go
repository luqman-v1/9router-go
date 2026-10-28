package tray

import (
	"context"
	"net"
	"strconv"
	"time"

	"9router/proxy/internal/config"
	"9router/proxy/internal/daemon"
	"9router/proxy/internal/service"
)

// Gather reads the facts the state machine needs. One function so the tray
// binary and any test agree on where each value comes from.
//
// Port comes from config, never from the pid file: the pid file records PID and
// executable only (internal/daemon/pidfile.go), so a port read from there would
// be invention.
func Gather(port int) Facts {
	f := Facts{DaemonPID: daemon.RunningPID()}
	info, ok := service.Status()
	f.ServiceInfo, f.ServiceOK = info, ok
	f.PortBusy, f.ForeignPID = portBusy(port)
	return f
}

// daemonPID is the verified daemon PID, or 0. Exposed for the platform files.
func daemonPID() int { return daemon.RunningPID() }

// portFromConfig is the configured listen port, shared with daemonLocalURL.
func portFromConfig() int { return config.LoadConfig().Port }

// portBusy reports whether anything is listening, and attributes the listener
// when the pid file explains it. A busy port with no verified daemon is exactly
// the PortConflict case.
func portBusy(port int) (bool, int) {
	if !dialable(port) {
		return false, 0
	}
	if pid := daemon.RunningPID(); pid > 0 {
		return true, pid
	}
	// Busy, but not ours: a foreign listener. The PID is unknown here because
	// the port answered and nobody claimed it; the menu reports it as such.
	return true, 0
}

// dialable is a TCP connect with a short budget: enough to know something is
// listening on loopback, short enough not to stall the menu.
func dialable(port int) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
