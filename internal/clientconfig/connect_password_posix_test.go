//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package clientconfig

import (
	"testing"

	"golang.org/x/sys/unix"
)

// P5 (POSIX half) — the bits the prompt clears are ECHO and ISIG.
//
// ECHO off is the password staying off the screen. ISIG off is what keeps the
// terminal usable: with ISIG set, Ctrl+C raises SIGINT, the process dies
// before the deferred termios restore runs, and the user's shell is left with
// echo off — every keystroke after that is invisible. That is a real harm to
// the user, not a theoretical one.
//
// The cases build their termios through untyped constants because Termios.Lflag
// is uint32 on Linux and uint64 on macOS; assigning a uint32 here would fail to
// compile on darwin, and a P5 that only builds on one platform is not the gate
// §7.6 asks for.
//
// Pure state transition, not a live terminal: the gate §7.6 describes is "P5 on
// a real TTY", and running this on CI is a weaker check — recorded as
// unverified rather than counted as the gate.
func TestEchoOff_ClearsEchoAndIsig(t *testing.T) {
	const otherFlags = unix.ICANON

	tests := []struct {
		name string
		mk   func() *unix.Termios
	}{
		{"echo plus signal bits", func() *unix.Termios {
			return &unix.Termios{Lflag: unix.ECHO | unix.ISIG | otherFlags}
		}},
		{"echo already off", func() *unix.Termios {
			return &unix.Termios{Lflag: unix.ISIG | otherFlags}
		}},
		{"signal already off", func() *unix.Termios {
			return &unix.Termios{Lflag: unix.ECHO | otherFlags}
		}},
		{"everything on", func() *unix.Termios {
			v := &unix.Termios{}
			v.Lflag = ^v.Lflag
			return v
		}},
		{"nothing on", func() *unix.Termios { return &unix.Termios{} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := tt.mk()
			want := in.Lflag &^ unix.ECHO &^ unix.ISIG
			got := echoOff(in)
			if got.Lflag&unix.ECHO != 0 {
				t.Errorf("echoOff left ECHO set: Lflag = %#x", got.Lflag)
			}
			if got.Lflag&unix.ISIG != 0 {
				t.Errorf("echoOff left ISIG set: Lflag = %#x", got.Lflag)
			}
			if got.Lflag != want {
				t.Errorf("echoOff Lflag = %#x, want %#x (only ECHO and ISIG cleared)", got.Lflag, want)
			}
		})
	}
}

// ICANON must survive: backspace still has to work while the password is
// typed.
func TestEchoOff_KeepsCanonicalMode(t *testing.T) {
	in := &unix.Termios{Lflag: unix.ECHO | unix.ISIG | unix.ICANON}
	got := echoOff(in)
	if got.Lflag&unix.ICANON == 0 {
		t.Errorf("echoOff dropped ICANON: Lflag = %#x", got.Lflag)
	}
}

// The restore path writes the *original* termios back, so the copy echoOff
// returns must not be the one that gets restored — and echoOff must not touch
// the caller's termios at all.
func TestEchoOff_DoesNotMutateInput(t *testing.T) {
	in := &unix.Termios{Lflag: unix.ECHO | unix.ISIG | unix.ICANON, Iflag: unix.ICRNL}
	before := in.Lflag

	out := echoOff(in)
	if out == in {
		t.Fatal("echoOff returned the same pointer; the caller's termios would be modified")
	}
	if in.Lflag != before {
		t.Errorf("input termios mutated: Lflag = %#x, want %#x", in.Lflag, before)
	}
	if out.Iflag != unix.ICRNL {
		t.Errorf("echoOff dropped an unrelated flag: Iflag = %#x", out.Iflag)
	}
}
