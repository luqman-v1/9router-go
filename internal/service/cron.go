package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"9router/proxy/internal/constants"
)

// cronMarker tags the @reboot line this package owns, so install stays
// idempotent and uninstall never touches the user's own crontab entries.
const cronMarker = "# 9router-go-autostart"

// renderCronLine renders the @reboot entry: cron has no supervisor, so it
// launches the existing `start` daemon which detaches and owns its pid file.
func renderCronLine(exe string, args []string) string {
	return "@reboot " + commandLine(exe, appendSubcommand(args, "start")) + " " + cronMarker
}

// renderAutostartDesktop renders the XDG autostart entry, a companion for
// graphical logins on distros where cron is not enabled by default. The
// ownership marker travels as a comment line so uninstall recognizes the file
// even when the binary was renamed.
func renderAutostartDesktop(exe string, args []string) string {
	return "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=9router-go gateway\n" +
		"Comment=9router-go AI API proxy gateway\n" +
		"Exec=" + commandLine(exe, appendSubcommand(args, "start")) + "\n" +
		"Terminal=false\n" +
		"X-GNOME-Autostart-enabled=true\n" +
		cronMarker + "\n"
}

func autostartDesktopPath() string {
	home, err := userHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "autostart", "9router-go.desktop")
}

func installCron(exe string, args []string) (Info, error) {
	if err := addCronEntry(renderCronLine(exe, args)); err != nil {
		return Info{}, err
	}
	path := autostartDesktopPath()
	if path != "" {
		if err := os.MkdirAll(filepath.Dir(path), constants.FilePermDir); err == nil {
			_ = os.WriteFile(path, []byte(renderAutostartDesktop(exe, args)), constants.FilePermFile)
		}
	}
	return Info{Backend: BackendCron, Path: "crontab (@reboot)", Exec: commandLine(exe, args), Enabled: true}, nil
}

func uninstallCron() (Info, bool) {
	removedCrontab := removeCronEntry()
	desktopRemoved := removeOwnedDesktopEntry()
	if !removedCrontab && !desktopRemoved {
		return Info{}, false
	}
	return Info{Backend: BackendCron, Path: "crontab (@reboot)"}, true
}

// removeOwnedDesktopEntry deletes the autostart .desktop file, but only when
// this package wrote it, and it reports removal as the source of truth — the
// rule the launchd and OpenRC backends already follow. A file that survived
// (locked, permission denied) still fires at the next graphical login, so
// reporting it as removed told the user something untrue.
func removeOwnedDesktopEntry() bool {
	path := autostartDesktopPath()
	if path == "" {
		return false
	}
	if _, err := os.Stat(path); err != nil {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), cronMarker) {
		// Not written by this package, or unreadable: leave the user's own
		// autostart entry alone.
		return false
	}
	return remove(path) == nil
}

func statusCron() (Info, bool) {
	if !cronEntryPresent() {
		if path := autostartDesktopPath(); path != "" {
			if _, err := os.Stat(path); err == nil {
				return Info{Backend: BackendCron, Path: path, Enabled: true}, true
			}
		}
		return Info{}, false
	}
	return Info{Backend: BackendCron, Path: "crontab (@reboot)", Enabled: true}, true
}

// readCrontab and writeCrontab shell out to the real crontab(1). They are vars
// so tests can drive the cron backend without a crontab binary present.
var (
	readCrontab = func() string {
		cmd := exec.Command("crontab", "-l")
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		return string(out)
	}
	writeCrontab = func(content string) error {
		cmd := exec.Command("crontab", "-")
		cmd.Stdin = strings.NewReader(content)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("service.Install: write crontab: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
)

func cronEntryPresent() bool {
	for _, line := range strings.Split(readCrontab(), "\n") {
		if strings.Contains(line, cronMarker) {
			return true
		}
	}
	return false
}

func addCronEntry(line string) error {
	existing := readCrontab()
	merged, already := mergeCronEntry(existing, line)
	if already {
		return nil
	}
	return writeCrontab(merged)
}

// mergeCronEntry appends line to the crontab content unless the exact line is
// already present. It reports whether the entry already existed.
func mergeCronEntry(existing, line string) (string, bool) {
	for _, l := range strings.Split(existing, "\n") {
		if strings.TrimSpace(l) == strings.TrimSpace(line) {
			return existing, true
		}
	}
	content := strings.TrimRight(existing, "\n")
	if content != "" {
		content += "\n"
	}
	return content + line + "\n", false
}

func removeCronEntry() bool {
	existing := readCrontab()
	if existing == "" {
		return false
	}
	content, removed := stripCronEntries(existing)
	if !removed {
		return false
	}
	if err := writeCrontab(content); err != nil {
		return false
	}
	return true
}

// stripCronEntries removes every line this package owns from the crontab
// content, preserving the user's own entries byte for byte.
func stripCronEntries(existing string) (string, bool) {
	kept := []string{}
	removed := false
	for _, l := range strings.Split(existing, "\n") {
		if strings.Contains(l, cronMarker) {
			removed = true
			continue
		}
		kept = append(kept, l)
	}
	if !removed {
		return existing, false
	}
	content := strings.TrimRight(strings.Join(kept, "\n"), "\n")
	if content != "" {
		content += "\n"
	}
	return content, true
}
