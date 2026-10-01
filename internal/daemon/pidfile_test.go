package daemon

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"9router/proxy/internal/proc"
)

// pidLine renders a claim the way writePID does.
func pidLine(pid int, exe string) string {
	return strconv.Itoa(pid) + pidFieldSep + exe
}

// selfExecutable is the binary this test process runs: a claim naming it names
// a copy of this binary, which is what livePID spawns.
func selfExecutable(t *testing.T) string {
	t.Helper()
	self, err := proc.SelfExecutable()
	if err != nil {
		t.Fatalf("resolve this binary: %v", err)
	}
	return self
}

// foreignExecutable is a path no live process runs, so a claim carrying it is a
// verifiable-looking number pointing at somebody else.
func foreignExecutable(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "not-this-binary")
}

// TestLiveExecutableMatchesSelfExecutable pins the assumption the whole identity
// check rests on: what a process looks like on disk is the path this process
// itself would record. If that stopped holding, every claim written by a
// running daemon would look like a stranger's.
func TestLiveExecutableMatchesSelfExecutable(t *testing.T) {
	pid := livePID(t)

	got, err := proc.Executable(pid)
	if err != nil {
		t.Fatalf("read the image of live pid %d: %v", pid, err)
	}
	if want := selfExecutable(t); got != want {
		t.Fatalf("proc.Executable(%d) = %q, want %q", pid, got, want)
	}
}

// TestRunningPIDReportsThisProcess is the positive that needs no child process:
// this process wrote its own claim and is running the binary that claim names.
func TestRunningPIDReportsThisProcess(t *testing.T) {
	isolate(t)
	RegisterPID()

	raw, err := os.ReadFile(PIDFile())
	if err != nil {
		t.Fatalf("read pid file: %v", err)
	}
	if want := pidLine(os.Getpid(), selfExecutable(t)); strings.TrimSpace(string(raw)) != want {
		t.Fatalf("pid file = %q, want %q", raw, want)
	}
	if pid := RunningPID(); pid != os.Getpid() {
		t.Fatalf("RunningPID() = %d, want this process %d", pid, os.Getpid())
	}
}

// A bare number is still read as a number, so an old file does not turn into a
// parse error — but it carries nothing that can be checked, and an unverifiable
// claim is not a running daemon.
func TestParseClaim(t *testing.T) {
	self := "/usr/local/bin/9router-go"

	tests := []struct {
		name string
		raw  string
		want claim
	}{
		{
			name: "pid with its executable verifies",
			raw:  "20130 " + self + "\n",
			want: claim{pid: 20130, exePath: self},
		},
		{
			name: "bare pid parses without an identity",
			raw:  "20130\n",
			want: claim{pid: 20130},
		},
		{
			name: "missing file is not a claim",
			raw:  "",
			want: claim{},
		},
		{
			name: "malformed pid is not a claim",
			raw:  "not-a-pid\n",
			want: claim{},
		},
		{
			name: "zero is not a claim",
			raw:  "0 " + self,
			want: claim{},
		},
		{
			name: "negative pid is not a claim",
			raw:  "-1 " + self,
			want: claim{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseClaim(tt.raw)
			if got.pid != tt.want.pid || got.exePath != tt.want.exePath {
				t.Fatalf("parseClaim(%q) = %+v, want %+v", tt.raw, got, tt.want)
			}
		})
	}
}

// Only a claim that resolves to this binary is an owner; the rest must not be.
func TestClaimOwner(t *testing.T) {
	isolate(t)
	self := selfExecutable(t)

	tests := []struct {
		name  string
		claim claim
		want  bool
	}{
		{
			name:  "no pid at all",
			claim: claim{},
			want:  false,
		},
		{
			name:  "bare number carries nothing to check",
			claim: claim{pid: os.Getpid()},
			want:  false,
		},
		{
			name:  "this process running the recorded binary",
			claim: claim{pid: os.Getpid(), exePath: self},
			want:  true,
		},
		{
			name:  "this process running some other binary",
			claim: claim{pid: os.Getpid(), exePath: filepath.Join(t.TempDir(), "other")},
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.claim.owner(); got != tt.want {
				t.Fatalf("owner() = %v, want %v", got, tt.want)
			}
		})
	}
}
