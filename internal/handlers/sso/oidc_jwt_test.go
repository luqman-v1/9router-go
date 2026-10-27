package sso

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// testIdP is a minimal issuer: it publishes a JWKS and signs tokens the way a
// real provider does, so the verification path runs against real crypto rather
// than a stub that agrees with itself.
type testIdP struct {
	key    *rsa.PrivateKey
	kid    string
	server *httptest.Server
}

func newTestIdP(t *testing.T) *testIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp := &testIdP{key: key, kid: "test-key-1"}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		pub := key.PublicKey
		set := map[string]any{"keys": []any{map[string]any{
			"kty": "RSA",
			"kid": idp.kid,
			"use": "sig",
			"alg": "RS256",
			"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(set)
	})
	idp.server = httptest.NewServer(mux)
	t.Cleanup(idp.server.Close)
	// The cache is keyed by URI; each test gets its own so they cannot share a
	// key they did not mint.
	jwksMu.Lock()
	delete(jwksCache, idp.server.URL+"/.well-known/jwks.json")
	jwksMu.Unlock()
	return idp
}

func (i *testIdP) jwksURI() string { return i.server.URL + "/.well-known/jwks.json" }

// sign builds a JWS with the given claims.
func (i *testIdP) sign(t *testing.T, claims map[string]any) string {
	t.Helper()
	header := fmt.Sprintf(`{"alg":"RS256","typ":"JWT","kid":%q}`, i.kid)
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	unsigned := enc([]byte(header)) + "." + enc(claimsJSON)
	sum := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, i.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return unsigned + "." + enc(sig)
}

func (i *testIdP) validClaims() map[string]any {
	now := time.Now().Unix()
	return map[string]any{
		"iss":   "https://idp.example.com",
		"aud":   "9router-client",
		"sub":   "user-123",
		"email": "operator@example.com",
		"name":  "Operator",
		"nonce": "the-nonce",
		"iat":   now,
		"exp":   now + 300,
	}
}

func TestVerifyOIDCIDTokenAcceptsAValidToken(t *testing.T) {
	idp := newTestIdP(t)
	token := idp.sign(t, idp.validClaims())

	claims, err := verifyOIDCIDToken(context.Background(), token,
		"https://idp.example.com", "9router-client", idp.jwksURI(), "the-nonce")
	if err != nil {
		t.Fatalf("a valid token was rejected: %v", err)
	}
	if claims["email"] != "operator@example.com" {
		t.Errorf("email = %v", claims["email"])
	}
}

// A token signed by a different key must not verify: that is the whole point of
// the signature check.
func TestVerifyOIDCIDTokenRejectsAForeignSignature(t *testing.T) {
	idp := newTestIdP(t)
	other := newTestIdP(t)

	// Same header (kid, alg) but the wrong private key.
	header := fmt.Sprintf(`{"alg":"RS256","typ":"JWT","kid":%q}`, idp.kid)
	claimsJSON, _ := json.Marshal(idp.validClaims())
	enc := base64.RawURLEncoding.EncodeToString
	unsigned := enc([]byte(header)) + "." + enc(claimsJSON)
	sum := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, other.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	token := unsigned + "." + enc(sig)

	if _, err := verifyOIDCIDToken(context.Background(), token,
		"https://idp.example.com", "9router-client", idp.jwksURI(), "the-nonce"); err == nil {
		t.Fatal("a token signed by the wrong key was accepted")
	}
}

// `alg: none` is the classic JWT bypass; it must be refused before any key
// lookup happens.
func TestVerifyOIDCIDTokenRejectsAlgNone(t *testing.T) {
	idp := newTestIdP(t)
	enc := base64.RawURLEncoding.EncodeToString
	header := enc([]byte(`{"alg":"none","typ":"JWT"}`))
	claimsJSON, _ := json.Marshal(idp.validClaims())
	token := header + "." + enc(claimsJSON) + "."

	if _, err := verifyOIDCIDToken(context.Background(), token,
		"https://idp.example.com", "9router-client", idp.jwksURI(), "the-nonce"); err == nil {
		t.Fatal("an alg:none token was accepted")
	}
}

func TestVerifyOIDCIDTokenRejectsWrongAudience(t *testing.T) {
	idp := newTestIdP(t)
	token := idp.sign(t, idp.validClaims())
	if _, err := verifyOIDCIDToken(context.Background(), token,
		"https://idp.example.com", "some-other-client", idp.jwksURI(), "the-nonce"); err == nil {
		t.Fatal("a token for another audience was accepted")
	}
}

// Some IdPs put the audience in an array.
func TestVerifyOIDCIDTokenAcceptsArrayAudience(t *testing.T) {
	idp := newTestIdP(t)
	claims := idp.validClaims()
	claims["aud"] = []any{"other-client", "9router-client"}
	token := idp.sign(t, claims)
	if _, err := verifyOIDCIDToken(context.Background(), token,
		"https://idp.example.com", "9router-client", idp.jwksURI(), "the-nonce"); err != nil {
		t.Fatalf("array audience rejected: %v", err)
	}
}

func TestVerifyOIDCIDTokenRejectsWrongIssuer(t *testing.T) {
	idp := newTestIdP(t)
	token := idp.sign(t, idp.validClaims())
	if _, err := verifyOIDCIDToken(context.Background(), token,
		"https://other-idp.example.com", "9router-client", idp.jwksURI(), "the-nonce"); err == nil {
		t.Fatal("a token from another issuer was accepted")
	}
}

func TestVerifyOIDCIDTokenRejectsAnExpiredToken(t *testing.T) {
	idp := newTestIdP(t)
	claims := idp.validClaims()
	claims["exp"] = time.Now().Add(-time.Minute).Unix()
	token := idp.sign(t, claims)
	if _, err := verifyOIDCIDToken(context.Background(), token,
		"https://idp.example.com", "9router-client", idp.jwksURI(), "the-nonce"); err == nil {
		t.Fatal("an expired token was accepted")
	}
}

// The nonce binds the token to the request that started the flow; a mismatch is
// a replay.
func TestVerifyOIDCIDTokenRejectsWrongNonce(t *testing.T) {
	idp := newTestIdP(t)
	token := idp.sign(t, idp.validClaims())
	if _, err := verifyOIDCIDToken(context.Background(), token,
		"https://idp.example.com", "9router-client", idp.jwksURI(), "a-different-nonce"); err == nil {
		t.Fatal("a token with the wrong nonce was accepted")
	}
}

func TestPKCEChallengeIsS256(t *testing.T) {
	verifier := "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJ"
	got := pkceChallenge(verifier)
	sum := sha256.Sum256([]byte(verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if got != want {
		t.Errorf("challenge = %q, want %q", got, want)
	}
}

func TestRandomURLTokenIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		tok := randomURLToken(32)
		if len(tok) < 40 {
			t.Errorf("token too short: %q", tok)
		}
		if seen[tok] {
			t.Fatalf("duplicate token: %q", tok)
		}
		seen[tok] = true
	}
}
