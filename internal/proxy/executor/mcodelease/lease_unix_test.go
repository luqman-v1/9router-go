//go:build !windows

package mcodelease

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// These tests drive Lease/Status against a real Unix socket speaking the real
// frame protocol, with a real capability file on disk. The net.Pipe tests in
// mcodelease_test.go prove the codec; these prove the client end to end —
// capability discovery, the dial, and the response parsing together — which is
// the part a mock cannot cover.
//
// Windows has no Unix socket, so they are tagged `unix` and run on the Linux
// and macOS CI jobs. The Windows named-pipe path is exercised by
// TestResolveEndpoint* and the Windows-only transport selection test.

// standUpBroker writes a capability file and serves the real frame protocol on
// a real Unix socket, so Lease is driven against something that behaves like
// the desktop app.
func standUpBroker(t *testing.T, handler func(req leaseRequest) leaseResponse) (dataDir string, calls *int64) {
	t.Helper()
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}
	cap := strings.Repeat("a", capabilityLen)
	if err := os.WriteFile(filepath.Join(runDir, capabilityFile), []byte(cap), 0o600); err != nil {
		t.Fatalf("write capability: %v", err)
	}
	sock := filepath.Join(runDir, socketFile)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	var n int64
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				hdr := make([]byte, 4)
				if _, err := readFullConn(c, hdr); err != nil {
					return
				}
				body := make([]byte, binary.BigEndian.Uint32(hdr))
				if _, err := readFullConn(c, body); err != nil {
					return
				}
				var req leaseRequest
				if err := json.Unmarshal(body, &req); err != nil {
					return
				}
				if req.Capability != cap {
					return // capability reject: answer nothing
				}
				atomic.AddInt64(&n, 1)
				out, err := encodeFrame(handler(req))
				if err != nil {
					return
				}
				_, _ = c.Write(out)
			}(c)
		}
	}()
	return dir, &n
}

func readFullConn(c net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := c.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func TestLeaseAgainstRealBroker(t *testing.T) {
	dir, calls := standUpBroker(t, func(req leaseRequest) leaseResponse {
		if req.Method != "lease" {
			return refused(CodeInvalidRequest)
		}
		if req.MinValidity != MaxMinValidityMs {
			return refused(CodeInvalidRequest)
		}
		return leaseResponse{OK: true, Result: &struct {
			Status      string `json:"status"`
			Generation  int64  `json:"generation"`
			ExpiresAtMs int64  `json:"expiresAtMs"`
			AccessToken string `json:"accessToken"`
		}{Status: "signed_in", AccessToken: "real-broker-token", Generation: 7,
			ExpiresAtMs: time.Now().Add(90 * time.Minute).UnixMilli()}}
	})

	res, err := Lease(context.Background(), dir, MaxMinValidityMs)
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	if res.AccessToken != "real-broker-token" {
		t.Errorf("AccessToken = %q, want the broker's token", res.AccessToken)
	}
	if res.Generation != 7 {
		t.Errorf("Generation = %d, want 7", res.Generation)
	}
	if got := atomic.LoadInt64(calls); got != 1 {
		t.Errorf("broker calls = %d, want 1", got)
	}
}

func TestStatusAgainstRealBroker(t *testing.T) {
	dir, _ := standUpBroker(t, func(req leaseRequest) leaseResponse {
		return leaseResponse{OK: true, Result: &struct {
			Status      string `json:"status"`
			Generation  int64  `json:"generation"`
			ExpiresAtMs int64  `json:"expiresAtMs"`
			AccessToken string `json:"accessToken"`
		}{Status: "signed_in", Generation: 3}}
	})
	st, err := Status(context.Background(), dir)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Status != "signed_in" || st.Generation != 3 {
		t.Errorf("status = %+v", st)
	}
}

// A data dir whose capability file exists but whose socket does not is the
// "desktop app installed but not running" case, and must be reported as the
// broker being unavailable rather than a hang.
func TestLeaseSocketMissing(t *testing.T) {
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

// refused builds the error envelope the broker answers a bad request with.
func refused(code string) leaseResponse {
	return leaseResponse{OK: false, Error: &struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Code: code, Message: "refused by the test broker"}}
}
