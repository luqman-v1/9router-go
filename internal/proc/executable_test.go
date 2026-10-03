package proc

import (
	"os"
	"os/exec"
	"testing"
)

// The daemon verifies a pid-file claim by comparing the recorded image against
// the live process's. A stub that returned an empty path would make every claim
// fail closed, and the daemon would then be unable to find itself — so the live
// child has to report the very binary that started it.
func TestExecutable_ReportsSelfForLiveChild(t *testing.T) {
	child := longLivedPID(t)
	t.Cleanup(func() { _ = ForceKill(child) })

	path, err := Executable(child)
	if err != nil {
		t.Fatalf("Executable(%d): %v", child, err)
	}
	if path == "" {
		t.Fatal("Executable returned an empty path for a live process")
	}

	self, err := SelfExecutable()
	if err != nil {
		t.Fatalf("SelfExecutable: %v", err)
	}
	if !sameExecutable(path, self) {
		t.Errorf("Executable(%d) = %q, want the running binary %q", child, path, self)
	}
}

func TestExecutable_RejectsInvalidPID(t *testing.T) {
	for _, pid := range []int{0, -1} {
		if path, err := Executable(pid); err == nil {
			t.Errorf("Executable(%d) = %q, want an error", pid, path)
		}
	}
}

// The comparison is what the pid-file check turns on, so a case-only difference
// on Windows must not read as a different binary — and an empty path must never
// compare equal to anything.
func TestSameExecutable(t *testing.T) {
	self, err := SelfExecutable()
	if err != nil {
		t.Skipf("cannot resolve this binary: %v", err)
	}

	tests := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{name: "identical paths", a: self, b: self, want: true},
		{name: "trailing separator resolves to the same file", a: self, b: self + string(os.PathSeparator), want: true},
		{name: "different binaries", a: self, b: "/some/other/9router-go", want: false},
		{name: "an empty path never matches", a: "", b: self, want: false},
		{name: "two empty paths never match", a: "", b: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sameExecutable(tt.a, tt.b); got != tt.want {
				t.Errorf("sameExecutable(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// A dead process has no image path, which is the whole point: the daemon treats
// that as "unverified" rather than trusting a recycled PID.
func TestExecutable_DeadProcessHasNoPath(t *testing.T) {
	child := exec.Command(os.Args[0], "-test.run=TestProcSleepHelper")
	child.Env = append(os.Environ(), "9ROUTER_PROC_HELPER=1")
	if err := child.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	dead := child.Process.Pid
	_ = child.Process.Kill()
	_ = child.Wait()
	if path, err := Executable(dead); err == nil {
		t.Errorf("Executable(%d) = %q for a dead process, want an error", dead, path)
	}
}