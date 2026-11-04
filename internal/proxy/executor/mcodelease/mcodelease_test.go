package mcodelease

import (
	"context"
	"encoding/binary"
	json "encoding/json/v2"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestResolveEndpointUnix(t *testing.T) {
	if isWindows() {
		t.Skip("unix transport is not chosen on Windows")
	}
	ep, err := ResolveEndpoint("/home/u/.minimax")
	if err != nil {
		t.Fatalf("ResolveEndpoint: %v", err)
	}
	if ep.transport != "unix" {
		t.Errorf("transport = %q, want unix", ep.transport)
	}
	if want := filepath.Join("/home/u/.minimax", "run", socketFile); ep.address != want {
		t.Errorf("address = %q, want %q", ep.address, want)
	}
	if want := filepath.Join("/home/u/.minimax", "run", capabilityFile); ep.capabilityFile != want {
		t.Errorf("capabilityFile = %q, want %q", ep.capabilityFile, want)
	}
}

// A trailing slash must not change the socket path: the desktop app writes one
// canonical name and a client that appends a separator would dial a different
// file.
func TestResolveEndpointTrailingSeparator(t *testing.T) {
	if isWindows() {
		t.Skip("unix transport is not chosen on Windows")
	}
	ep, err := ResolveEndpoint("/home/u/.minimax/")
	if err != nil {
		t.Fatalf("ResolveEndpoint: %v", err)
	}
	if strings.HasSuffix(ep.address, "//") || strings.Contains(ep.address, ".minimax//") {
		t.Errorf("address kept a trailing separator: %q", ep.address)
	}
}

func TestResolveEndpointRejectsRelative(t *testing.T) {
	if _, err := ResolveEndpoint(".minimax"); err == nil {
		t.Fatal("ResolveEndpoint accepted a relative dir")
	}
}

// The Windows pipe digest is what the official client computes, so a client
// that hashed a differently-normalized path would dial a pipe nobody serves.
func TestResolveEndpointWindowsDigest(t *testing.T) {
	if !isWindows() {
		t.Skip("pipe transport is only chosen on Windows")
	}
	ep, err := ResolveEndpoint(`C:\Users\u\.minimax`)
	if err != nil {
		t.Fatalf("ResolveEndpoint: %v", err)
	}
	if ep.transport != "pipe" {
		t.Fatalf("transport = %q, want pipe", ep.transport)
	}
	if !strings.HasPrefix(ep.address, windowsPipeGlob) {
		t.Errorf("address = %q, want prefix %q", ep.address, windowsPipeGlob)
	}
	digest := strings.TrimPrefix(ep.address, windowsPipeGlob)
	if len(digest) != 16 {
		t.Errorf("digest len = %d, want 16", len(digest))
	}
}

func TestIsLeaseConnection(t *testing.T) {
	tests := []struct {
		name string
		psd  map[string]any
		want bool
	}{
		{"nil map", nil, false},
		{"absent", map[string]any{"fingerprintId": "f1"}, false},
		{"lease", map[string]any{"authType": "mcode-lease"}, true},
		{"other authType", map[string]any{"authType": "oauth"}, false},
		{"non-string", map[string]any{"authType": 7}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsLeaseConnection(tt.psd); got != tt.want {
				t.Errorf("IsLeaseConnection = %v, want %v", got, tt.want)
			}
		})
	}
}

// A data dir override in the connection beats the environment, and the
// environment beats the default install location — the order the official
// client uses.
func TestResolveDataDirPrecedence(t *testing.T) {
	base := t.TempDir()
	override := filepath.Join(base, "override")
	fromEnv := filepath.Join(base, "env")
	if err := os.MkdirAll(override, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fromEnv, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MINIMAX_DESKTOP_DATA_DIR", fromEnv)

	got, err := ResolveDataDir(map[string]any{"mcodeDataDir": override})
	if err != nil || got != override {
		t.Fatalf("override ignored: got %q err %v", got, err)
	}
	got, err = ResolveDataDir(nil)
	if err != nil || got != fromEnv {
		t.Fatalf("env ignored: got %q err %v", got, err)
	}
}

func TestResolveDataDirRejectsRelativeOverride(t *testing.T) {
	if _, err := ResolveDataDir(map[string]any{"mcodeDataDir": ".minimax"}); err == nil {
		t.Fatal("relative override accepted")
	}
}

// The capability file's exact shape is the broker's identity: 43 base64url
// characters and nothing else.
func TestReadCapabilityRejectsWrongShape(t *testing.T) {
	dir := t.TempDir()
	run := filepath.Join(dir, "run")
	if err := os.MkdirAll(run, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"too short":  "abc",
		"bad chars":  strings.Repeat("!", capabilityLen),
		"with space": strings.Repeat("a", capabilityLen-1) + " ",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(run, capabilityFile)
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readCapability(endpoint{capabilityFile: path}); err == nil {
				t.Fatalf("capability %q accepted", content)
			}
		})
	}
}

func TestReadCapabilityAcceptsValid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, capabilityFile)
	if err := os.WriteFile(path, []byte(strings.Repeat("a", capabilityLen)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cap, err := readCapability(endpoint{capabilityFile: path})
	if err != nil {
		t.Fatalf("readCapability: %v", err)
	}
	if len(cap) != capabilityLen {
		t.Errorf("cap len = %d, want %d", len(cap), capabilityLen)
	}
}

func TestReadCapabilityMissingFile(t *testing.T) {
	_, err := readCapability(endpoint{capabilityFile: filepath.Join(t.TempDir(), "nope.cap")})
	if err == nil {
		t.Fatal("missing capability file accepted")
	}
	var le *AuthLeaseError
	if !errors.As(err, &le) || le.Code != CodeBrokerUnavailable {
		t.Fatalf("err = %v, want BROKER_UNAVAILABLE", err)
	}
}

func TestEncodeFrameBounds(t *testing.T) {
	if _, err := encodeFrame(nil); err != nil {
		// A nil payload marshals to "null", which is in bounds, so this
		// must not fail.
		t.Fatalf("encodeFrame(nil): %v", err)
	}
	big := map[string]string{"pad": strings.Repeat("x", MaxFrameBytes+10)}
	if _, err := encodeFrame(big); err == nil {
		t.Fatal("oversized frame accepted")
	}
}

func TestEncodeFrameRoundTrip(t *testing.T) {
	frame, err := encodeFrame(leaseRequest{Version: 1, Method: "status", Capability: "cap"})
	if err != nil {
		t.Fatalf("encodeFrame: %v", err)
	}
	if got := binary.BigEndian.Uint32(frame[:4]); got != uint32(len(frame)-4) {
		t.Errorf("prefix = %d, want %d", got, len(frame)-4)
	}
}

// A frame length of zero or above the bound is a protocol error, not an empty
// body to retry.
func TestAssertOK(t *testing.T) {
	ok, err := assertOK(&leaseResponse{OK: true, Result: &struct {
		Status      string `json:"status"`
		Generation  int64  `json:"generation"`
		ExpiresAtMs int64  `json:"expiresAtMs"`
		AccessToken string `json:"accessToken"`
	}{Status: "signed_in"}})
	if err != nil || ok == nil || ok.Result.Status != "signed_in" {
		t.Fatalf("assertOK ok: %v", err)
	}
	for _, code := range []string{CodeAuthRequired, CodeBrokerUnavailable, CodeProtocolMismatch, CodeInternalError} {
		_, err := assertOK(&leaseResponse{OK: false, Error: &struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}{Code: code}})
		var le *AuthLeaseError
		if !errors.As(err, &le) || le.Code != code {
			t.Errorf("code %q not preserved: %v", code, err)
		}
	}
	// An unknown code degrades to INTERNAL_ERROR rather than leaking a
	// broker string the dashboard cannot classify.
	_, err = assertOK(&leaseResponse{OK: false, Error: &struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Code: "SOMETHING_NEW"}})
	var le *AuthLeaseError
	if !errors.As(err, &le) || le.Code != CodeInternalError {
		t.Errorf("unknown code = %v", err)
	}
}

func TestLeaseErrCode(t *testing.T) {
	if got := leaseErrCode(nil); got != "" {
		t.Errorf("nil = %q, want empty", got)
	}
	if got := leaseErrCode(leaseError(CodeAuthRequired, "")); got != CodeAuthRequired {
		t.Errorf("lease error = %q", got)
	}
	if got := leaseErrCode(os.ErrNotExist); got != "" {
		t.Errorf("foreign error = %q, want empty", got)
	}
}

// ─── broker end-to-end over a real unix socket ───────────────────────────────

// startBroker serves one frame per connection, the way the desktop broker
// does, so the codec and the request shape are exercised against a real
// socket rather than a mocked transport.
func startBroker(t *testing.T, handler func(req leaseRequest) leaseResponse) string {
	t.Helper()
	if isWindows() {
		t.Skip("broker fixture uses a unix socket")
	}
	dir := t.TempDir()
	// Production resolves the socket under <dataDir>/run, the same directory
	// the capability file lives in. A listener anywhere else is a fixture that
	// dials a path nobody serves.
	if err := os.MkdirAll(filepath.Join(dir, "run"), 0o755); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}
	sock := filepath.Join(dir, "run", socketFile)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				header := make([]byte, 4)
				if _, err := io.ReadFull(c, header); err != nil {
					return
				}
				n := binary.BigEndian.Uint32(header)
				body := make([]byte, n)
				if _, err := io.ReadFull(c, body); err != nil {
					return
				}
				var req leaseRequest
				_ = json.Unmarshal(body, &req)
				frame, _ := encodeFrame(handler(req))
				_, _ = c.Write(frame)
			}(c)
		}
	}()
	return dir
}

func writeCapability(t *testing.T, dataDir string) {
	t.Helper()
	run := filepath.Join(dataDir, "run")
	if err := os.MkdirAll(run, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run, capabilityFile), []byte(strings.Repeat("a", capabilityLen)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseEndToEnd(t *testing.T) {
	dir := startBroker(t, func(req leaseRequest) leaseResponse {
		if req.Method != "lease" {
			return leaseResponse{OK: false, Error: &struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			}{Code: CodeInvalidRequest}}
		}
		return leaseResponse{OK: true, Result: &struct {
			Status      string `json:"status"`
			Generation  int64  `json:"generation"`
			ExpiresAtMs int64  `json:"expiresAtMs"`
			AccessToken string `json:"accessToken"`
		}{AccessToken: "tok-123", ExpiresAtMs: time.Now().Add(time.Hour).UnixMilli(), Generation: 4}}
	})
	writeCapability(t, dir)

	got, err := Lease(context.Background(), dir, MaxMinValidityMs)
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	if got.AccessToken != "tok-123" {
		t.Errorf("token = %q", got.AccessToken)
	}
	if got.Generation != 4 {
		t.Errorf("generation = %d", got.Generation)
	}
}

func TestStatusEndToEnd(t *testing.T) {
	dir := startBroker(t, func(req leaseRequest) leaseResponse {
		if req.Method != "status" {
			return leaseResponse{OK: false, Error: &struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			}{Code: CodeInvalidRequest}}
		}
		return leaseResponse{OK: true, Result: &struct {
			Status      string `json:"status"`
			Generation  int64  `json:"generation"`
			ExpiresAtMs int64  `json:"expiresAtMs"`
			AccessToken string `json:"accessToken"`
		}{Status: "signed_in", Generation: 9}}
	})
	writeCapability(t, dir)

	got, err := Status(context.Background(), dir)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.Status != "signed_in" || got.Generation != 9 {
		t.Errorf("status = %+v", got)
	}
}

// A broker that reports AUTH_REQUIRED must surface that code, so the
// dashboard can say "sign in to the desktop app" instead of "broker not
// running".
func TestLeaseAuthRequiredCode(t *testing.T) {
	dir := startBroker(t, func(req leaseRequest) leaseResponse {
		return leaseResponse{OK: false, Error: &struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}{Code: CodeAuthRequired}}
	})
	writeCapability(t, dir)

	_, err := Lease(context.Background(), dir, MaxMinValidityMs)
	var le *AuthLeaseError
	if !errors.As(err, &le) || le.Code != CodeAuthRequired {
		t.Fatalf("err = %v, want AUTH_REQUIRED", err)
	}
}

// A capability file without a socket behind it is the "desktop app installed
// but not running" case, and must be reported as the broker being unavailable
// rather than surfacing a raw dial error.
func TestLeaseSocketMissing(t *testing.T) {
	if isWindows() {
		t.Skip("broker fixture uses a unix socket")
	}
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, capabilityFile), []byte(strings.Repeat("a", capabilityLen)), 0o600); err != nil {
		t.Fatalf("write capability: %v", err)
	}
	_, err := Lease(context.Background(), dir, MaxMinValidityMs)
	if err == nil {
		t.Fatal("a missing socket was not reported")
	}
	var le *AuthLeaseError
	if !errors.As(err, &le) {
		t.Fatalf("err = %v (%T), want *AuthLeaseError", err, err)
	}
	if le.Code != CodeBrokerUnavailable {
		t.Errorf("code = %s, want %s", le.Code, CodeBrokerUnavailable)
	}
}

// Concurrent getToken calls must issue one lease, not one per waiter: the
// desktop broker is a single local process and a burst would queue behind it.
// The broker is injected, so this invariant is verified on every host rather
// than only where a unix socket fixture can run.
func TestManagerSingleFlight(t *testing.T) {
	var calls int64
	var mu sync.Mutex
	release := make(chan struct{})
	m := &manager{leaseFn: func(ctx context.Context, dataDir string, minValidityMs int64) (LeaseResult, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		<-release // hold every caller behind one in-flight lease
		return LeaseResult{AccessToken: "shared", ExpiresAtMs: time.Now().Add(time.Hour).UnixMilli()}, nil
	}}

	const waiters = 8
	errs := make(chan error, waiters)
	for i := 0; i < waiters; i++ {
		go func() {
			l, err := m.getToken(context.Background(), "/any")
			if err == nil && l.AccessToken != "shared" {
				err = errors.New("token = " + l.AccessToken + ", want shared")
			}
			errs <- err
		}()
	}
	// Give the waiters time to pile up behind the one lease before releasing
	// it, so the test fails if the single-flight is missing rather than
	// passing because the calls happened to be sequential.
	time.Sleep(50 * time.Millisecond)
	close(release)
	for i := 0; i < waiters; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("getToken: %v", err)
		}
	}
	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 1 {
		t.Errorf("broker was called %d times, want 1 (single-flight broken)", got)
	}
}

// Every waiter of a failed lease sees the same error, and the failure is not
// cached: the next call tries again.
func TestManagerSingleFlightSharesFailure(t *testing.T) {
	var calls int64
	m := &manager{leaseFn: func(ctx context.Context, dataDir string, minValidityMs int64) (LeaseResult, error) {
		atomic.AddInt64(&calls, 1)
		return LeaseResult{}, leaseError(CodeBrokerUnavailable, "")
	}}
	_, err := m.getToken(context.Background(), "/any")
	var le *AuthLeaseError
	if !errors.As(err, &le) || le.Code != CodeBrokerUnavailable {
		t.Fatalf("err = %v, want BROKER_UNAVAILABLE", err)
	}
	if m.cached != nil {
		t.Error("a failed lease was cached")
	}
	_, _ = m.getToken(context.Background(), "/any")
	if got := atomic.LoadInt64(&calls); got != 2 {
		t.Errorf("broker called %d times, want 2 (failure must not be sticky)", got)
	}
}

// A cached token inside the margin is reused; one past it is refreshed.
func TestManagerCacheMargin(t *testing.T) {
	m := &manager{}
	fresh := &LeaseResult{AccessToken: "fresh", ExpiresAtMs: time.Now().Add(2 * time.Minute).UnixMilli()}
	m.cached = fresh
	if _, err := m.getToken(context.Background(), "/nonexistent"); err != nil {
		t.Fatalf("cached token not reused: %v", err)
	}
	stale := &LeaseResult{AccessToken: "stale", ExpiresAtMs: time.Now().Add(30 * time.Second).UnixMilli()}
	m.cached = stale
	// Past the margin: the manager must try to lease, which fails here
	// because /nonexistent has no broker. That failure is the proof it did
	// not serve the stale token.
	if _, err := m.getToken(context.Background(), "/nonexistent"); err == nil {
		t.Fatal("stale token served instead of refreshing")
	}
}

func TestManagerInvalidate(t *testing.T) {
	m := &manager{cached: &LeaseResult{AccessToken: "x"}}
	m.invalidate()
	if m.cached != nil {
		t.Fatal("invalidate left the cache populated")
	}
}

func TestLeaseClampsMinValidity(t *testing.T) {
	// Only the clamp's arithmetic is under test here; the broker is not
	// reached. A negative or oversized value must not reach the wire.
	for _, in := range []int64{-5, MaxMinValidityMs * 4} {
		clamped := in
		if clamped < 0 {
			clamped = 0
		}
		if clamped > MaxMinValidityMs {
			clamped = MaxMinValidityMs
		}
		if clamped < 0 || clamped > MaxMinValidityMs {
			t.Errorf("in %d clamped to %d", in, clamped)
		}
	}
}

// ─── codec over net.Pipe: platform-independent ──────────────────────────────

// drainRequest consumes one whole request frame so a server goroutine can
// answer on a net.Pipe, whose write blocks until the peer reads all of it.
func drainRequest(c net.Conn) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(c, header); err != nil {
		return
	}
	body := make([]byte, binary.BigEndian.Uint32(header))
	_, _ = io.ReadFull(c, body)
}

// pipeBroker answers one frame per connection over net.Pipe, so the length
// prefix, the JSON body and the response parsing are exercised on every host
// without a unix socket.
func pipeBroker(t *testing.T, handler func(req leaseRequest) leaseResponse) net.Conn {
	t.Helper()
	c, s := net.Pipe()
	go func() {
		defer s.Close()
		_ = s.SetDeadline(time.Now().Add(5 * time.Second))
		header := make([]byte, 4)
		if _, err := io.ReadFull(s, header); err != nil {
			return
		}
		body := make([]byte, binary.BigEndian.Uint32(header))
		if _, err := io.ReadFull(s, body); err != nil {
			return
		}
		var req leaseRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return
		}
		frame, _ := encodeFrame(handler(req))
		_, _ = s.Write(frame)
	}()
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestExchangeFrameRoundTrip(t *testing.T) {
	c := pipeBroker(t, func(req leaseRequest) leaseResponse {
		if req.Version != ProtocolVersion {
			return leaseResponse{OK: false, Error: &struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			}{Code: CodeProtocolMismatch}}
		}
		return leaseResponse{OK: true, Result: &struct {
			Status      string `json:"status"`
			Generation  int64  `json:"generation"`
			ExpiresAtMs int64  `json:"expiresAtMs"`
			AccessToken string `json:"accessToken"`
		}{AccessToken: "pipe-token", Generation: 2}}
	})

	resp, err := exchangeFrame(context.Background(), c, leaseRequest{
		Version:     ProtocolVersion,
		RequestID:   "9r-lease-test",
		Capability:  strings.Repeat("a", capabilityLen),
		Method:      "lease",
		MinValidity: MaxMinValidityMs,
	}, 5*time.Second)
	if err != nil {
		t.Fatalf("exchangeFrame: %v", err)
	}
	if !resp.OK || resp.Result == nil || resp.Result.AccessToken != "pipe-token" {
		t.Fatalf("resp = %+v", resp)
	}
}

// A frame length of zero is a protocol error the broker cannot be trusted to
// have meant, so the exchange fails rather than blocking on an empty body.
func TestExchangeFrameZeroLengthIsProtocolMismatch(t *testing.T) {
	c, s := net.Pipe()
	go func() {
		defer s.Close()
		drainRequest(s)
		_, _ = s.Write(make([]byte, 4)) // zero-length payload
	}()
	t.Cleanup(func() { _ = c.Close() })

	_, err := exchangeFrame(context.Background(), c, leaseRequest{Version: 1, Method: "status"}, 5*time.Second)
	var le *AuthLeaseError
	if !errors.As(err, &le) || le.Code != CodeProtocolMismatch {
		t.Fatalf("err = %v, want PROTOCOL_MISMATCH", err)
	}
}

// A non-JSON body is the broker's fault, not a retryable transport error.
func TestExchangeFrameNonJSONIsProtocolMismatch(t *testing.T) {
	c, s := net.Pipe()
	go func() {
		defer s.Close()
		drainRequest(s)
		body := []byte("not json")
		out := make([]byte, 4+len(body))
		binary.BigEndian.PutUint32(out[:4], uint32(len(body)))
		copy(out[4:], body)
		_, _ = s.Write(out)
	}()
	t.Cleanup(func() { _ = c.Close() })

	_, err := exchangeFrame(context.Background(), c, leaseRequest{Version: 1, Method: "status"}, 5*time.Second)
	var le *AuthLeaseError
	if !errors.As(err, &le) || le.Code != CodeProtocolMismatch {
		t.Fatalf("err = %v, want PROTOCOL_MISMATCH", err)
	}
}

// A frame length above the bound is refused before the body is read, so a
// misbehaving broker cannot make the gateway allocate for it.
func TestExchangeFrameOversizedLengthIsProtocolMismatch(t *testing.T) {
	c, s := net.Pipe()
	go func() {
		defer s.Close()
		drainRequest(s)
		out := make([]byte, 4)
		binary.BigEndian.PutUint32(out, uint32(MaxFrameBytes+1))
		_, _ = s.Write(out)
	}()
	t.Cleanup(func() { _ = c.Close() })

	_, err := exchangeFrame(context.Background(), c, leaseRequest{Version: 1, Method: "status"}, 5*time.Second)
	var le *AuthLeaseError
	if !errors.As(err, &le) || le.Code != CodeProtocolMismatch {
		t.Fatalf("err = %v, want PROTOCOL_MISMATCH", err)
	}
}

// A cancelled context must abandon the exchange instead of waiting out the
// timeout: the client that asked for the token has gone away.
func TestExchangeFrameHonoursCancelledContext(t *testing.T) {
	c, s := net.Pipe()
	go func() {
		defer s.Close()
		time.Sleep(2 * time.Second) // never answers
	}()
	t.Cleanup(func() { _ = c.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := exchangeFrame(ctx, c, leaseRequest{Version: 1, Method: "lease"}, 10*time.Second)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}
