//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package clientconfig

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// IsTerminal and echo-off use x/sys/unix, already a direct dependency, instead
// of adding golang.org/x/term (docs/specs/connect-security.md §6.2.1, D1).

// isTerminal reports whether f is a terminal: an ioctl that only a terminal
// answers.
func isTerminal(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), termiosGetReq)
	return err == nil
}

// echoOff returns a copy of t with ECHO and ISIG cleared.
//
// ECHO off is the point: the password must not appear on screen. ISIG off is
// what keeps the terminal usable if the prompt is interrupted: with ISIG set,
// Ctrl+C raises SIGINT, the process dies, the deferred termios restore never
// runs, and the user's shell is left with echo off — every keystroke after
// that is invisible. With ISIG clear, Ctrl+C is delivered as the byte 0x03
// and lands in the password like any other character, so the terminal is
// always restored.
//
// ICANON and the control characters are untouched, so backspace still works.
//
// It takes and returns a value rather than a *os.File so P5 can assert the
// bits are cleared by the same code the prompt uses, with no terminal involved.
func echoOff(t *unix.Termios) *unix.Termios {
	next := *t
	next.Lflag &^= unix.ECHO
	next.Lflag &^= unix.ISIG
	return &next
}

// readPasswordFromConsole reads one line with echo off. The original termios is
// restored on every path, so an interrupted read does not leave the user
// typing invisibly.
func readPasswordFromConsole(f *os.File) ([]byte, error) {
	fd := int(f.Fd())
	old, err := unix.IoctlGetTermios(fd, termiosGetReq)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoTTY, err)
	}
	if err := unix.IoctlSetTermios(fd, termiosSetReq, echoOff(old)); err != nil {
		return nil, fmt.Errorf("disable echo: %w", err)
	}
	defer func() { _ = unix.IoctlSetTermios(fd, termiosSetReq, old) }()

	return readLine(f)
}
