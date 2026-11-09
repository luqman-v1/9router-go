//go:build windows

package clientconfig

import (
	"testing"

	"golang.org/x/sys/windows"
)

// P5 (Windows half) — the bit the prompt clears is ENABLE_ECHO_INPUT, and
// nothing else in the console mode is touched, so line editing and Ctrl+C keep
// working while the password is typed.
//
// It tests the pure mode transition rather than a live console: a CI runner has
// no console to attach to, and a t.Skip would not count as a pass (§7.6).
func TestEchoOffWindows_ClearsOnlyEchoInput(t *testing.T) {
	const otherBits = windows.ENABLE_LINE_INPUT | windows.ENABLE_PROCESSED_INPUT

	tests := []struct {
		name string
		mode uint32
	}{
		{"echo plus editing bits", windows.ENABLE_ECHO_INPUT | otherBits},
		{"echo already off", otherBits},
		{"everything on", ^uint32(0)},
		{"nothing on", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := echoOffWindows(tt.mode)
			if got&windows.ENABLE_ECHO_INPUT != 0 {
				t.Errorf("echoOffWindows(%#x) = %#x, still has ENABLE_ECHO_INPUT", tt.mode, got)
			}
			want := tt.mode &^ windows.ENABLE_ECHO_INPUT
			if got != want {
				t.Errorf("echoOffWindows(%#x) = %#x, want %#x (only the echo bit cleared)", tt.mode, got, want)
			}
		})
	}
}

func TestEchoOffWindows_DoesNotMutateInput(t *testing.T) {
	mode := uint32(windows.ENABLE_ECHO_INPUT | windows.ENABLE_LINE_INPUT)
	before := mode
	_ = echoOffWindows(mode)
	if mode != before {
		t.Errorf("echoOffWindows mutated its argument: %#x -> %#x", before, mode)
	}
}
