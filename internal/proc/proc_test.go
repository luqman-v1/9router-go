package proc

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// longLivedPID spawns a child that sleeps long enough to be terminated on
// purpose, and is killed on cleanup if the test failed to stop it.
func longLivedPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestProcSleepHelper")
	cmd.Env = append(os.Environ(), "9ROUTER_PROC_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	go func() {
		_ = cmd.Wait()
	}()
	t.Cleanup(func() {
		_ = ForceKill(cmd.Process.Pid)
	})
	return cmd.Process.Pid
}

// TestProcSleepHelper is the payload longLivedPID spawns.
func TestProcSleepHelper(t *testing.T) {
	if os.Getenv("9ROUTER_PROC_HELPER") != "1" {
		t.Skip("helper payload, only meaningful as a spawned child")
	}
	time.Sleep(60 * time.Second)
}

func TestAliveRejectsDeadAndInvalidPIDs(t *testing.T) {
	if Alive(0) || Alive(-1) {
		t.Fatal("Alive must reject non-positive PIDs")
	}

	dead := 0
	for pid := 4_000_000; pid < 4_002_000; pid++ {
		if !Alive(pid) {
			dead = pid
			break
		}
	}
	if dead == 0 {
		t.Skip("no unused PID found in the probe range")
	}
	if Alive(dead) {
		t.Fatalf("Alive(%d) = true for a dead PID", dead)
	}
}

func TestAliveReportsRunningProcess(t *testing.T) {
	pid := longLivedPID(t)
	if !Alive(pid) {
		t.Fatalf("Alive(%d) = false for a process that just started", pid)
	}
}

// Stop must actually end the process — this is what `9router-go stop` depends
// on, and on Windows the graceful path is a forced TerminateProcess, so both
// platforms are pinned to "process is gone when Terminate returns".
func TestTerminateEndsProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Log("windows has no SIGTERM; Terminate escalates to TerminateProcess")
	}

	pid := longLivedPID(t)

	deadline := time.Now().Add(2 * time.Second)
	for !Alive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}

	if !Terminate(pid, 2000) {
		t.Fatalf("Terminate(%d) reported the process is still alive", pid)
	}
	if Alive(pid) {
		t.Fatalf("Terminate(%d) returned but the process is still running", pid)
	}
}

func TestTerminateDeadProcessIsAlreadyDone(t *testing.T) {
	if !Terminate(0, 100) {
		t.Fatal("Terminate(0) must report success: nothing to stop")
	}
}

func TestDetached(t *testing.T) {
	attr := Detached()
	if attr == nil {
		t.Fatal("expected non-nil SysProcAttr from Detached()")
	}
}

func TestSelfExecutable(t *testing.T) {
	p, err := SelfExecutable()
	if err != nil {
		t.Fatalf("SelfExecutable failed: %v", err)
	}
	if p == "" {
		t.Fatal("expected non-empty path from SelfExecutable()")
	}
}

func TestForceKill_InvalidPIDs(t *testing.T) {
	if err := ForceKill(0); err == nil {
		t.Error("expected error for ForceKill(0)")
	}
	if err := ForceKill(-1); err == nil {
		t.Error("expected error for ForceKill(-1)")
	}
}

func TestResolveImagePath_EmptyAndEdge(t *testing.T) {
	if got := resolveImagePath(""); got != "" {
		t.Errorf("resolveImagePath(\"\") = %q, want empty", got)
	}
	if got := resolveImagePath("   "); got != "" {
		t.Errorf("resolveImagePath(\"   \") = %q, want empty", got)
	}
}

func TestWaitExit_Timeout(t *testing.T) {
	pid := longLivedPID(t)
	t.Cleanup(func() { _ = ForceKill(pid) })
	// Wait for 1ms on a live process should time out and return false
	if waitExit(pid, 1) {
		t.Error("expected waitExit to return false on timeout")
	}
}
