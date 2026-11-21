//go:build windows

package main

import (
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

func TestHideWindow(t *testing.T) {
	cmd := exec.Command("9router-go", "status")
	hideWindow(cmd)
	hideWindow(nil)

	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.HideWindow {
		t.Fatalf("expected HideWindow=true on windows, got %+v", cmd.SysProcAttr)
	}
	if cmd.SysProcAttr.CreationFlags != windows.CREATE_NO_WINDOW {
		t.Fatalf("expected CreationFlags=CREATE_NO_WINDOW, got %#x", cmd.SysProcAttr.CreationFlags)
	}
}
