//go:build windows

package clientconfig

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// IsTerminal and the echo-off pair are done with x/sys/windows, already a direct
// dependency (go.mod), rather than golang.org/x/term, which would be a new one
// for about forty lines (docs/specs/connect-security.md §6.2.1, decision D1).

// isTerminal reports whether f is attached to a console: GetConsoleMode only
// succeeds on a console handle.
func isTerminal(f *os.File) bool {
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(f.Fd()), &mode) == nil
}

// echoOffWindows clears ENABLE_ECHO_INPUT and leaves every other console mode
// bit alone — line editing and processed input stay on, so the user still gets
// backspace and Ctrl+C.
func echoOffWindows(mode uint32) uint32 {
	return mode &^ windows.ENABLE_ECHO_INPUT
}

// readPasswordFromConsole reads one line from the console with echo disabled.
// The previous mode is restored on every path, including a read error, so a
// failed prompt cannot leave the user's terminal typing invisibly.
func readPasswordFromConsole(f *os.File) ([]byte, error) {
	h := windows.Handle(f.Fd())
	var old uint32
	if err := windows.GetConsoleMode(h, &old); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoTTY, err)
	}
	if err := windows.SetConsoleMode(h, echoOffWindows(old)); err != nil {
		return nil, fmt.Errorf("disable echo: %w", err)
	}
	defer func() { _ = windows.SetConsoleMode(h, old) }()

	return readLine(f)
}
