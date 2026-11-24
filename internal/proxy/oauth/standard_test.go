package oauth

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/providers"
)

// Antigravity and every other provider on a plain OAuth2 grant refresh through
// this refresher, so a revoked grant has to arrive at the router as a status
// it can read — not as a sentence it has to parse.
func TestStandardRefresher_SurfacesRejectionStatus(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   bool
	}{
		{"revoked grant", http.StatusUnauthorized, true},
		{"invalid request", http.StatusBadRequest, false},
		{"token endpoint down", http.StatusInternalServerError, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			}))
			defer server.Close()

			refresher := NewStandardRefresher(server.URL, "cid", "cs")
			_, err := refresher(context.Background(), &Params{Client: server.Client(), Provider: "antigravity", RefreshToken: "rt"})
			if err == nil {
				t.Fatalf("refresh against a %d endpoint returned no error", tt.status)
			}
			if got := providers.IsRefreshUnauthorized(err); got != tt.want {
				t.Errorf("IsRefreshUnauthorized(err) = %v, want %v (err: %v)", got, tt.want, err)
			}
			var refreshErr *providers.OAuthRefreshError
			if !errors.As(err, &refreshErr) {
				t.Fatalf("error is not a *providers.OAuthRefreshError: %v", err)
			}
			if refreshErr.Status != tt.status {
				t.Errorf("status = %d, want %d", refreshErr.Status, tt.status)
			}
		})
	}
}

// The grant is in the body the token endpoint gets; an error that echoes it
// would write a live credential into the logs.
func TestStandardRefresher_TruncatesEchoedGrant(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write(make([]byte, 4096))
	}))
	defer server.Close()

	refresher := NewStandardRefresher(server.URL, "cid", "cs")
	_, err := refresher(context.Background(), &Params{Client: server.Client(), Provider: "antigravity", RefreshToken: "rt"})
	var refreshErr *providers.OAuthRefreshError
	if !errors.As(err, &refreshErr) {
		t.Fatalf("error is not a *providers.OAuthRefreshError: %v", err)
	}
	if len(refreshErr.Body) > 200 {
		t.Errorf("body length = %d, want it truncated to 200", len(refreshErr.Body))
	}
}

func TestStandardRefresher_NilClientFallsBackToBoundedDefault(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok-1","expires_in":3600}`))
	}))
	defer server.Close()

	refresher := NewStandardRefresher(server.URL, "cid", "cs")
	got, err := refresher(context.Background(), &Params{Client: nil, Provider: "antigravity", RefreshToken: "rt"})
	if err != nil {
		t.Fatalf("expected nil-client refresh to succeed, got %v", err)
	}
	if got.AccessToken != "tok-1" {
		t.Fatalf("AccessToken = %q, want tok-1", got.AccessToken)
	}
}

func TestStandardRefresher_CapsOversizedErrorBody(t *testing.T) {
	const maxBytes = 1 << 20
	payload := bytes.Repeat([]byte("a"), 2<<20)
	countingBody := &countingReadCloser{Reader: bytes.NewReader(payload)}
	client := &http.Client{
		Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusInternalServerError,
				Body:       countingBody,
				Header:     make(http.Header),
			}, nil
		}),
	}

	refresher := NewStandardRefresher("https://oauth.example.test/token", "cid", "cs")
	_, err := refresher(context.Background(), &Params{Client: client, Provider: "antigravity", RefreshToken: "rt"})
	var refreshErr *providers.OAuthRefreshError
	if !errors.As(err, &refreshErr) {
		t.Fatalf("expected *providers.OAuthRefreshError, got %v", err)
	}
	if len(refreshErr.Body) != 200 {
		t.Fatalf("body length = %d, want 200", len(refreshErr.Body))
	}
	if countingBody.n > maxBytes {
		t.Fatalf("read %d bytes from oversized body, want <= %d", countingBody.n, maxBytes)
	}
}

type countingReadCloser struct {
	io.Reader
	n int64
}

func (c *countingReadCloser) Read(p []byte) (int, error) {
	n, err := c.Reader.Read(p)
	c.n += int64(n)
	return n, err
}

func (c *countingReadCloser) Close() error { return nil }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
