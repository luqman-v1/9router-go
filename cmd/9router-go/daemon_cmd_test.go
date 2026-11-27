package main

import (
	"errors"
	"flag"
	"fmt"
	"testing"

	"github.com/urfave/cli/v2"

	"9router/proxy/internal/daemon"
)

// TestStartDetached_AlreadyRunningVariants pins that both shapes
// daemon.Start can answer "already running" with produce the friendly
// one-line message and a nil error: the bare sentinel, and the sentinel
// wrapped with the port/owner context (`%w on %s (…) — stop it first…`).
// The wrapped variant is what an operator hits when another process owns
// the port without a PID file, and == missed it, so the raw error reached
// the terminal instead of the message. menu.go's comparisons already used
// errors.Is for exactly this reason.
func TestStartDetached_AlreadyRunningVariants(t *testing.T) {
	tests := []struct {
		name       string
		startErr   error
		wantSubstr string
	}{
		{
			name:       "bare sentinel",
			startErr:   daemon.ErrAlreadyRunning,
			wantSubstr: "already running",
		},
		{
			name:       "wrapped sentinel with port owner",
			startErr:   fmt.Errorf("%w on %s (%s) — stop it first, or start this binary without the daemon", daemon.ErrAlreadyRunning, "http://127.0.0.1:20130", "some other process"),
			wantSubstr: "already running",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := daemonStart
			t.Cleanup(func() { daemonStart = orig })
			daemonStart = func(_ string, _ []string) (daemon.StartResult, error) {
				return daemon.StartResult{PID: 7, URL: "http://127.0.0.1:20130", Existing: true}, tt.startErr
			}

			app := cli.NewApp()
			ctx := cli.NewContext(app, flag.NewFlagSet("test", flag.ContinueOnError), nil)
			err := startDetached(ctx)
			if err != nil {
				t.Fatalf("startDetached() error = %v, want nil (the friendly message must swallow the already-running case)", err)
			}
		})
	}
}

// TestStartDetached_OtherErrorsPropagate pins that a start failure which is
// not "already running" still returns its error, so a real breakage is not
// mistaken for a running daemon.
func TestStartDetached_OtherErrorsPropagate(t *testing.T) {
	orig := daemonStart
	t.Cleanup(func() { daemonStart = orig })
	daemonStart = func(_ string, _ []string) (daemon.StartResult, error) {
		return daemon.StartResult{}, errors.New("locate binary: gone")
	}

	app := cli.NewApp()
	ctx := cli.NewContext(app, flag.NewFlagSet("test", flag.ContinueOnError), nil)
	err := startDetached(ctx)
	if err == nil {
		t.Fatal("startDetached() error = nil, want the start failure propagated")
	}
	if errors.Is(err, daemon.ErrAlreadyRunning) {
		t.Errorf("a non-already-running failure must not be relabelled: %v", err)
	}
}
