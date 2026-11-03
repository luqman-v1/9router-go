package uptime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// countingPusher records the pushes it was asked to make.
type countingPusher struct {
	calls  atomic.Int32
	status atomic.Value // string
	msg    atomic.Value // string
	err    error
}

func (c *countingPusher) Push(_ context.Context, status, msg string) error {
	c.calls.Add(1)
	c.status.Store(status)
	c.msg.Store(msg)
	return c.err
}

func TestConfigConfigured(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"default off", Config{}, false},
		{"enabled with no URL", Config{Enabled: true}, false},
		{"enabled with blank URL", Config{Enabled: true, PushURL: "   "}, false},
		{"disabled with a URL", Config{PushURL: "https://push.example.com/abc"}, false},
		{"enabled with a URL", Config{Enabled: true, PushURL: "https://push.example.com/abc"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.Configured(); got != tt.want {
				t.Errorf("Configured() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConfigInterval(t *testing.T) {
	if got := (Config{}).Interval(); got != DefaultInterval {
		t.Errorf("zero interval = %v, want %v", got, DefaultInterval)
	}
	if got := (Config{IntervalSeconds: 30}).Interval(); got != 30*time.Second {
		t.Errorf("30s interval = %v, want 30s", got)
	}
	// A negative value is a misconfiguration, not a request for a busy loop.
	if got := (Config{IntervalSeconds: -5}).Interval(); got != DefaultInterval {
		t.Errorf("negative interval = %v, want the default", got)
	}
}

// TestHTTPPusherSendsStatusWord proves the status word is appended to the push
// URL, which is the whole wire contract.
func TestHTTPPusherSendsStatusWord(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	p := &HTTPPusher{URL: srv.URL}
	if err := p.Push(context.Background(), "up", ""); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if gotPath != "/up" {
		t.Errorf("path = %q, want /up", gotPath)
	}
	if gotQuery != "" {
		t.Errorf("query = %q, want empty for a push with no note", gotQuery)
	}
}

// TestHTTPPusherSendsNote proves a note travels as the msg parameter, escaped
// so a space or a newline cannot break the request line.
func TestHTTPPusherSendsNote(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	p := &HTTPPusher{URL: srv.URL}
	if err := p.Push(context.Background(), "down", "disk full\nretry"); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if gotQuery != "msg=disk%20full%0Aretry" {
		t.Errorf("query = %q, want msg=disk%%20full%%0Aretry", gotQuery)
	}
}

// TestHTTPPusherTrimsPastedStatus proves a URL copied from the Uptime Kuma UI —
// which shows the full form including a status word — does not end up with two
// status words in the path.
func TestHTTPPusherTrimsPastedStatus(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	for _, pasted := range []string{srv.URL + "/up", srv.URL + "/up/"} {
		p := &HTTPPusher{URL: pasted}
		if err := p.Push(context.Background(), "up", ""); err != nil {
			t.Fatalf("Push(%q): %v", pasted, err)
		}
		if gotPath != "/up" {
			t.Errorf("path from %q = %q, want /up", pasted, gotPath)
		}
	}
}

// TestHTTPPusherRejectsEmptyURL proves an empty URL is an error rather than a
// request to a relative path that would resolve against nothing.
func TestHTTPPusherRejectsEmptyURL(t *testing.T) {
	p := &HTTPPusher{URL: ""}
	if err := p.Push(context.Background(), "up", ""); err == nil {
		t.Error("Push with an empty URL must fail")
	}
}

// TestHTTPPusherSurfacesNonOK proves a monitor answering 500 is reported as a
// failure, so the loop logs it rather than assuming the push landed.
func TestHTTPPusherSurfacesNonOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	p := &HTTPPusher{URL: srv.URL}
	if err := p.Push(context.Background(), "up", ""); err == nil {
		t.Error("Push against a 500 monitor must fail")
	}
}

// TestLoopPushesWhenConfigured proves a configured monitor receives a push on
// its first tick.
func TestLoopPushesWhenConfigured(t *testing.T) {
	pusher := &countingPusher{}
	loop := NewLoop(
		func() Config { return Config{Enabled: true, PushURL: "https://push.example.com/x", IntervalSeconds: 1} },
		func() Pusher { return pusher },
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go loop.Run(ctx)
	waitFor(t, 2*time.Second, func() bool { return pusher.calls.Load() >= 1 })
	if got := pusher.status.Load(); got != "up" {
		t.Errorf("status = %v, want up", got)
	}
}

// TestLoopNoopWhenUnconfigured proves an operator who never set a push URL
// produces no pushes and no errors — the loop must be silent, not failing.
func TestLoopNoopWhenUnconfigured(t *testing.T) {
	pusher := &countingPusher{}
	loop := NewLoop(
		func() Config { return Config{} },
		func() Pusher { return pusher },
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go loop.Run(ctx)
	time.Sleep(150 * time.Millisecond)
	if got := pusher.calls.Load(); got != 0 {
		t.Errorf("pushes = %d, want 0 for an unconfigured monitor", got)
	}
}

// TestLoopReadsConfigPerTick proves a URL enabled after the loop started takes
// effect without a restart, which is the whole reason the config is read per
// tick rather than cached.
func TestLoopReadsConfigPerTick(t *testing.T) {
	pusher := &countingPusher{}
	var enabled atomic.Bool
	loop := NewLoop(
		func() Config {
			return Config{Enabled: enabled.Load(), PushURL: "https://push.example.com/x", IntervalSeconds: 1}
		},
		func() Pusher { return pusher },
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go loop.Run(ctx)
	// Let one tick pass with the monitor off.
	time.Sleep(200 * time.Millisecond)
	if got := pusher.calls.Load(); got != 0 {
		t.Fatalf("pushes = %d before enabling, want 0", got)
	}
	enabled.Store(true)
	waitFor(t, 3*time.Second, func() bool { return pusher.calls.Load() >= 1 })
}

// TestLoopSurvivesPushFailure proves a failing push does not stop the loop: the
// next tick must still fire, because a monitor that was briefly unreachable
// must not cost the operator every later heartbeat.
func TestLoopSurvivesPushFailure(t *testing.T) {
	pusher := &countingPusher{err: errors.New("monitor unreachable")}
	loop := NewLoop(
		func() Config { return Config{Enabled: true, PushURL: "https://push.example.com/x", IntervalSeconds: 1} },
		func() Pusher { return pusher },
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go loop.Run(ctx)
	waitFor(t, 4*time.Second, func() bool { return pusher.calls.Load() >= 2 })
}

// TestLoopStopsOnCancel proves the loop exits when its context is cancelled, so
// it does not outlive the gateway's shutdown.
func TestLoopStopsOnCancel(t *testing.T) {
	pusher := &countingPusher{}
	loop := NewLoop(
		func() Config { return Config{Enabled: true, PushURL: "https://push.example.com/x", IntervalSeconds: 1} },
		func() Pusher { return pusher },
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { loop.Run(ctx); close(done) }()
	waitFor(t, 2*time.Second, func() bool { return pusher.calls.Load() >= 1 })
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not exit after its context was cancelled")
	}
}

// TestNilLoopIsInert proves a nil loop is safe to run, so a wiring mistake
// cannot panic the gateway at startup.
func TestNilLoopIsInert(t *testing.T) {
	var loop *Loop
	loop.Run(context.Background())
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v", timeout)
}
