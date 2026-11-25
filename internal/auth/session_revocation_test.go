package auth

import (
	"testing"
	"time"
)

// epochReset restores the package epoch after a test, so one test's revocation
// cannot leak into the next.
func epochReset(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { SetSessionEpoch(0, "") })
	SetSessionEpoch(0, "")
}

func TestSessionEpochRevokesOlderTokens(t *testing.T) {
	epochReset(t)
	const secret = "test-secret"

	// A token minted before the check existed carries no epoch, and must keep
	// working so deploying the feature logs nobody out.
	legacy, err := Sign(secret, time.Now())
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}
	if !Verify(legacy, secret) {
		t.Fatal("a token without an epoch must verify while the epoch is 0")
	}

	SetSessionEpoch(1, PasswordFingerprint("hash-a"))
	if Verify(legacy, secret) {
		t.Fatal("an epoch bump must revoke tokens issued before it")
	}

	current, err := Sign(secret, time.Now())
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}
	if !Verify(current, secret) {
		t.Fatal("a token issued at the current epoch must verify")
	}

	// Same epoch, different stored password hash: the fingerprint alone revokes.
	SetSessionEpoch(1, PasswordFingerprint("hash-b"))
	if Verify(current, secret) {
		t.Fatal("a changed password fingerprint must revoke the token")
	}
}

func TestSessionClaimSetRefusesRevokedToken(t *testing.T) {
	epochReset(t)
	const secret = "test-secret"

	token, err := Sign(secret, time.Now())
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}

	// Both readers of a session — the boolean gate and the claim decoder — are
	// subject to the epoch, so the two can never disagree about a revoked token.
	if decodeClaims(token, secret) == nil {
		t.Fatal("a live token must decode")
	}

	SetSessionEpoch(3, PasswordFingerprint("hash-c"))
	if decodeClaims(token, secret) != nil {
		t.Fatal("a revoked token must not decode")
	}
	if Verify(token, secret) {
		t.Fatal("a revoked token must not verify")
	}
}

func TestSessionTokenVersion(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want int
	}{
		{name: "absent", raw: map[string]any{}, want: 0},
		{name: "nil row", raw: nil, want: 0},
		{name: "json number", raw: map[string]any{"tokenVersion": float64(7)}, want: 7},
		{name: "negative is ignored", raw: map[string]any{"tokenVersion": float64(-1)}, want: 0},
		{name: "wrong type is ignored", raw: map[string]any{"tokenVersion": "9"}, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SessionTokenVersion(tt.raw); got != tt.want {
				t.Errorf("SessionTokenVersion() = %d, want %d", got, tt.want)
			}
		})
	}
}
