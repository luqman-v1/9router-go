//go:build windows

package tunnel

import (
	"fmt"
	"os/exec"
	"strconv"
	"syscall"
)

// configureDetached starts cloudflared in its own process group with no
// window, so closing the dashboard — or a console window the operator is not
// looking at — cannot take the tunnel down with it.
//
// CREATE_NEW_PROCESS_GROUP is what makes the child survive the parent's
// console: without it a Ctrl-C or a console close is delivered to every
// process attached to that console, cloudflared included.
func configureDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}

// killCloudflaredByPort terminates every cloudflared whose command line
// targets port, whatever host it binds.
//
// The PID file only covers the process this binary started. A tunnel left
// running by the upstream Node binary, or by an earlier run whose PID file was
// lost, still holds the port and the origin — and the new tunnel cannot bind.
// Matching the command line rather than the port alone is what keeps :20130
// from matching :201300 or :202130.
func killCloudflaredByPort(port int) {
	if port <= 0 {
		return
	}
	// Win32_Process is queried rather than taskkill /FI, because the filter
	// syntax cannot express "command line contains this port" portably across
	// Windows versions and locales.
	script := fmt.Sprintf(
		`Get-CimInstance Win32_Process -Filter "Name='cloudflared.exe'" | `+
			`Where-Object { $_.CommandLine -match ':%s(\D|$)' } | `+
			`ForEach-Object { Stop-Process -Id $_.ProcessId -Force }`,
		strconv.Itoa(port),
	)
	// -WindowStyle Hidden keeps the enable/disable call from flashing a
	// PowerShell window over whatever the operator is doing.
	//
	//nolint:noctx // deliberate: this runs during shutdown and during a
	// restart, where the caller's context is already cancelled or about to
	// be. A context-bound command would fail to kill the process it was
	// asked to kill.
	_ = exec.Command("powershell", "-NoProfile", "-NonInteractive",
		"-WindowStyle", "Hidden", "-Command", script).Run()
}
