package proxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestIsPrivateIP(t *testing.T) {
	tests := []struct {
		ip   string
		want bool
	}{
		{"127.0.0.1", true},
		{"127.0.0.2", true},
		{"127.255.255.254", true},
		{"10.0.0.1", true},
		{"10.255.255.254", true},
		{"172.16.0.1", true},
		{"172.31.255.254", true},
		{"192.168.0.1", true},
		{"192.168.1.254", true},
		{"169.254.169.254", true},
		{"169.254.1.1", true},
		{"0.0.0.0", true},
		{"::1", true},
		{"fe80::1", true},
		{"fc00::1", true},
		{"fd00::1", true},
		{"::ffff:127.0.0.1", true},
		{"::ffff:10.0.0.1", true},
		{"::ffff:169.254.169.254", true},
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"93.184.216.34", false},
		{"2606:4700:4700::1111", false},
	}

	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			parsed := net.ParseIP(tt.ip)
			if parsed == nil {
				t.Fatalf("failed to parse IP %q", tt.ip)
			}
			if got := IsPrivateIP(parsed); got != tt.want {
				t.Errorf("IsPrivateIP(%q) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}

func TestNewSafeDialer_BlocksPrivateWhenForbidden(t *testing.T) {
	t.Setenv("ALLOW_PRIVATE_NETWORKING", "false")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dialer := NewSafeDialer()

	conn, err := dialer.DialContext(context.Background(), "tcp", srv.Listener.Addr().String())
	if conn != nil {
		conn.Close()
	}
	if err == nil {
		t.Fatal("expected dial to private address to be blocked, but succeeded")
	}
	if !errors.Is(err, ErrSSRFBlocked) {
		t.Fatalf("expected ErrSSRFBlocked, got %v", err)
	}
}

func TestNewSafeDialer_PermitsPrivateWhenAllowed(t *testing.T) {
	t.Setenv("ALLOW_PRIVATE_NETWORKING", "true")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dialer := NewSafeDialer()

	conn, err := dialer.DialContext(context.Background(), "tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("expected dial to succeed when allowed, got %v", err)
	}
	if conn != nil {
		conn.Close()
	}
}

func TestNewSafeDialer_PermitsLoopbackViaContext(t *testing.T) {
	t.Setenv("ALLOW_PRIVATE_NETWORKING", "false")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dialer := NewSafeDialer()

	// With loopback allowed in context, local test server dial should succeed
	ctx := WithAllowLoopback(context.Background())
	conn, err := dialer.DialContext(ctx, "tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("expected loopback dial to succeed with WithAllowLoopback, got %v", err)
	}
	if conn != nil {
		conn.Close()
	}
}

func TestNewSafeTransport_BlocksPrivateRequests(t *testing.T) {
	t.Setenv("ALLOW_PRIVATE_NETWORKING", "false")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{
		Transport: NewSafeTransport(),
	}

	_, err := client.Get(srv.URL)
	if err == nil {
		t.Fatal("expected request to local server to fail with SSRF block")
	}
	if !errors.Is(err, ErrSSRFBlocked) {
		t.Fatalf("expected ErrSSRFBlocked in error chain, got %v", err)
	}
}

// TestNewSafeDialer_BlocksZonedLoopbackLiteral pins the zone gap in this guard.
// net.ParseIP answers nil for "::1%lo", and the hook's nil branch is "not an
// address, allow it" — so the zoned spelling of the same destination dialled
// straight through. A zone selects the outbound interface; it does not make the
// address public.
func TestNewSafeDialer_BlocksZonedLoopbackLiteral(t *testing.T) {
	t.Setenv("ALLOW_PRIVATE_NETWORKING", "false")

	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback on this host: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port

	dialer := NewSafeDialer()
	conn, err := dialer.DialContext(context.Background(), "tcp",
		net.JoinHostPort("::1%lo", strconv.Itoa(port)))
	if conn != nil {
		conn.Close()
	}
	if !errors.Is(err, ErrSSRFBlocked) {
		t.Fatalf("zoned loopback literal dialled: err = %v, want ErrSSRFBlocked", err)
	}
}
