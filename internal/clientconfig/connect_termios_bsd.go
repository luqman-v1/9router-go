//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package clientconfig

import "golang.org/x/sys/unix"

// The BSD family (macOS included) spells the termios ioctl TIOCGETA/TIOCSETA
// rather than Linux's TCGETS/TCSETS. Same operation, different numbers.
const (
	termiosGetReq = unix.TIOCGETA
	termiosSetReq = unix.TIOCSETA
)
