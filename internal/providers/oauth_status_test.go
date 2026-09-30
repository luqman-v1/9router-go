package providers

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsRefreshUnauthorized(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"no error", nil, false},
		{"rejected grant", &OAuthRefreshError{Status: http.StatusUnauthorized, Body: "invalid_grant"}, true},
		{"wrapped rejection", fmt.Errorf("OAuth refresh for antigravity: %w", &OAuthRefreshError{Status: http.StatusUnauthorized}), true},
		{"invalid request", &OAuthRefreshError{Status: http.StatusBadRequest, Body: "invalid_grant"}, false},
		{"forbidden", &OAuthRefreshError{Status: http.StatusForbidden}, false},
		{"token endpoint down", &OAuthRefreshError{Status: http.StatusInternalServerError}, false},
		{"transport failure", errors.New("OAuth refresh POST: dial tcp: i/o timeout"), false},
		// The status must be read off the error, not out of its text: a
		// message that merely mentions 401 proves nothing about the grant.
		{"untyped message naming 401", errors.New("token refresh rejected with status 401"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRefreshUnauthorized(tt.err); got != tt.want {
				t.Errorf("IsRefreshUnauthorized(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// The router decides whether to park an account off this status, so the token
// endpoint's answer has to survive the trip out of RefreshToken.
func TestRefreshToken_SurfacesRejectionStatus(t *testing.T) {
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

			_, err := RefreshToken(OAuthClientConfig{ClientID: "cid", ClientSecret: "cs", TokenURL: server.URL}, "rt")
			if err == nil {
				t.Fatalf("RefreshToken against a %d endpoint returned no error", tt.status)
			}
			if got := IsRefreshUnauthorized(err); got != tt.want {
				t.Errorf("IsRefreshUnauthorized(err) = %v, want %v (err: %v)", got, tt.want, err)
			}
			var refreshErr *OAuthRefreshError
			if !errors.As(err, &refreshErr) {
				t.Fatalf("error is not an *OAuthRefreshError: %v", err)
			}
			if refreshErr.Status != tt.status {
				t.Errorf("status = %d, want %d", refreshErr.Status, tt.status)
			}
		})
	}
}

// A rejected grant must not leak the submitted refresh token into logs: the
// token endpoint body can echo the request back.
func TestRefreshErrorTruncatesEchoedGrant(t *testing.T) {
	echoed := make([]byte, 512)
	for i := range echoed {
		echoed[i] = 'a'
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write(echoed)
	}))
	defer server.Close()

	_, err := RefreshToken(OAuthClientConfig{ClientID: "cid", TokenURL: server.URL}, "rt")
	if err == nil {
		t.Fatal("expected a rejection error")
	}
	var refreshErr *OAuthRefreshError
	if !errors.As(err, &refreshErr) {
		t.Fatalf("error is not an *OAuthRefreshError: %v", err)
	}
	if len(refreshErr.Body) > 200 {
		t.Errorf("body length = %d, want it truncated to 200", len(refreshErr.Body))
	}
}
