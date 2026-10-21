package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"9router/proxy/internal/constants"
)

// windowsStartupFile is the launcher this package owns in the Startup folder.
const windowsStartupFile = "9router-go.vbs"

// windowsStartupMarker tags the launcher as ours, so uninstall recognizes it
// even if the binary was renamed and never touches an unrelated file.
const windowsStartupMarker = "9router-go-autostart"

// windowsStartupDir resolves the per-user Startup folder that Windows runs at
// logon. %APPDATA% is the authority; the home-relative path is the fallback for
// a stripped environment where it is unset.
func windowsStartupDir() string {
	if appData := strings.TrimSpace(os.Getenv("APPDATA")); appData != "" {
		return filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "Startup")
	}
	home, err := userHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "AppData", "Roaming", "Microsoft", "Windows", "Start Menu", "Programs", "Startup")
}

func windowsStartupPath() string {
	dir := windowsStartupDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, windowsStartupFile)
}

// vbsString renders s as a VBScript string literal, doubling embedded quotes.
func vbsString(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// renderWindowsStartupVBS renders the launcher. A .vbs runs under wscript.exe,
// which owns no console, so the logon launch spawns no console window; Run's
// window-style argument is 0 (hidden) for the same reason. The command is
// `start`, which detaches its own pid-file daemon and returns, so the launcher
// does not linger.
func renderWindowsStartupVBS(exe string, args []string) string {
	cmd := windowsCommandLine(exe, append([]string{"start"}, args...))
	return "' 9router-go gateway - managed by `9router-go service install/uninstall`.\r\n" +
		"' " + windowsStartupMarker + "\r\n" +
		"CreateObject(\"WScript.Shell\").Run " + vbsString(cmd) + ", 0, False\r\n"
}

func installWindowsStartup(exe string, args []string) (Info, error) {
	if goos != "windows" {
		return Info{}, fmt.Errorf("service.Install: windows-startup backend only runs on windows")
	}
	path := windowsStartupPath()
	if path == "" {
		return Info{}, fmt.Errorf("service.Install: cannot resolve the Windows Startup folder")
	}
	if err := os.MkdirAll(filepath.Dir(path), constants.FilePermDir); err != nil {
		return Info{}, fmt.Errorf("service.Install: create Startup folder: %w", err)
	}
	cmd := windowsCommandLine(exe, append([]string{"start"}, args...))
	if err := os.WriteFile(path, []byte(renderWindowsStartupVBS(exe, args)), constants.FilePermFile); err != nil {
		return Info{}, fmt.Errorf("service.Install: write %s: %w", path, err)
	}
	info := Info{Backend: BackendWindowsStartup, Path: path, Exec: cmd, Enabled: true}
	// Best-effort launch now, mirroring systemd/launchd install: a wscript
	// failure still leaves a valid entry that runs at next logon.
	if _, err := run("wscript.exe", path); err == nil {
		info.Active = true
	}
	return info, nil
}

func uninstallWindowsStartup() (Info, bool) {
	if goos != "windows" {
		return Info{}, false
	}
	path := windowsStartupPath()
	if path == "" {
		return Info{}, false
	}
	if _, err := os.Stat(path); err != nil {
		return Info{}, false
	}
	// Removal is the source of truth: never report success when the launcher
	// survived.
	if err := os.Remove(path); err != nil {
		return Info{}, false
	}
	return Info{Backend: BackendWindowsStartup, Path: path}, true
}

func statusWindowsStartup() (Info, bool) {
	if goos != "windows" {
		return Info{}, false
	}
	path := windowsStartupPath()
	if path == "" {
		return Info{}, false
	}
	if _, err := os.Stat(path); err != nil {
		return Info{}, false
	}
	// The launcher fires once at logon and exits, so "active" cannot be read
	// from it; the daemon's own `status` answers whether the gateway is up.
	return Info{Backend: BackendWindowsStartup, Path: path, Enabled: true, ActiveUnknown: true}, true
}
