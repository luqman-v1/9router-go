//go:build windows

package service

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// hideCommandWindow prevents console-subsystem helpers (schtasks.exe,
// powershell.exe) from flashing visible windows when called from a
// windowsgui process such as 9router-tray's 1 s refresh loop.
func hideCommandWindow(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
}
