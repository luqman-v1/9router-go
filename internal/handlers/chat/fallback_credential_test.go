package chat

import (
	"net/http"
	"testing"
)

func TestCredentialRejected(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"401 always refreshes", http.StatusUnauthorized, `{"error":"nope"}`, true},
		{"kiro dead bearer token is a 403", http.StatusForbidden,
			`{"message":"The bearer token included in the request is invalid.","reason":null}`, true},
		{"aws sso expired access token", http.StatusForbidden,
			`{"message":"The provided access token has expired"}`, true},
		{"unentitled account is not a credential refusal", http.StatusForbidden,
			`{"message":"Your account is not entitled to this model"}`, false},
		{"quota refusal is not a credential refusal", http.StatusForbidden,
			`{"message":"Service quota exceeded"}`, false},
		{"rate limit is not a credential refusal", http.StatusTooManyRequests, `{"message":"slow down"}`, false},
		{"empty body 403", http.StatusForbidden, ``, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ue := &upstreamError{StatusCode: tc.status, Body: []byte(tc.body)}
			if got := credentialRejected(ue); got != tc.want {
				t.Errorf("credentialRejected(%d, %q) = %v, want %v", tc.status, tc.body, got, tc.want)
			}
		})
	}
	if credentialRejected(nil) {
		t.Error("credentialRejected(nil) = true")
	}
}
