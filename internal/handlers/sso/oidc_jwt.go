package sso

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ID-token verification.
//
// An OIDC id_token is a JWS: header.claims.signature, RS256-signed by a key the
// issuer publishes as a JWKS. Verifying it means checking the signature against
// that key, then the claims that make the token mean what it says — issuer,
// audience, expiry, and the nonce this flow generated.
//
// Only RS256 is implemented, because that is what every mainstream IdP
// (Okta, Entra, Auth0, Keycloak, Google, Authentik) signs with. A provider
// offering only ES256 would fail closed here rather than have its token
// accepted unverified, which is the safe direction.

// jwksCache keeps the discovery keys for a while: fetching them on every
// callback would put the IdP in the login critical path twice.
const jwksCacheTTL = 10 * time.Minute

type jwksEntry struct {
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
}

var (
	jwksMu    sync.Mutex
	jwksCache = map[string]jwksEntry{}
)

// jwkSet is the JWKS document.
type jwkSet struct {
	Keys []struct {
		Kty string `json:"kty"`
		Kid string `json:"kid"`
		Use string `json:"use"`
		Alg string `json:"alg"`
		N   string `json:"n"`
		E   string `json:"e"`
	} `json:"keys"`
}

// oidcSigningKeys returns the issuer's RSA keys, cached.
func oidcSigningKeys(ctx context.Context, jwksURI string) (map[string]*rsa.PublicKey, error) {
	if jwksURI == "" {
		return nil, fmt.Errorf("oidc: discovery has no jwks_uri")
	}
	jwksMu.Lock()
	if entry, ok := jwksCache[jwksURI]; ok && time.Since(entry.fetchedAt) < jwksCacheTTL {
		jwksMu.Unlock()
		return entry.keys, nil
	}
	jwksMu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jwksURI, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := oidcHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oidc: fetch jwks: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oidc: jwks answered %d", resp.StatusCode)
	}

	var set jwkSet
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		return nil, fmt.Errorf("oidc: parse jwks: %w", err)
	}
	keys := make(map[string]*rsa.PublicKey, len(set.Keys))
	for _, k := range set.Keys {
		if k.Kty != "RSA" || k.N == "" || k.E == "" {
			continue
		}
		// A key without a kid is still usable when it is the only one; store it
		// under the empty id so the lookup can fall back.
		modulus, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			continue
		}
		exponent, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			continue
		}
		pub := &rsa.PublicKey{
			N: new(big.Int).SetBytes(modulus),
			E: int(decodeBigEndian(exponent)),
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("oidc: jwks carries no usable RSA key")
	}

	jwksMu.Lock()
	jwksCache[jwksURI] = jwksEntry{keys: keys, fetchedAt: time.Now()}
	jwksMu.Unlock()
	return keys, nil
}

// decodeBigEndian turns the JWK exponent bytes into an int.
func decodeBigEndian(b []byte) uint64 {
	var n uint64
	for _, c := range b {
		n = n<<8 | uint64(c)
	}
	return n
}

// verifyOIDCIDToken validates the signature and the claims that matter, and
// returns the claim set.
func verifyOIDCIDToken(ctx context.Context, idToken, issuer, audience, jwksURI, nonce string) (map[string]any, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("oidc: id_token is not a JWS")
	}
	headerRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("oidc: decode header: %w", err)
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerRaw, &header); err != nil {
		return nil, fmt.Errorf("oidc: parse header: %w", err)
	}
	if header.Alg != "RS256" {
		// Refusing an algorithm the code did not implement is the whole
		// defence against "alg: none" and against a token signed with a key
		// type the verifier would otherwise misread.
		return nil, fmt.Errorf("oidc: unsupported alg %q", header.Alg)
	}

	keys, err := oidcSigningKeys(ctx, jwksURI)
	if err != nil {
		return nil, err
	}
	pub := keys[header.Kid]
	if pub == nil {
		// The kid may have rotated; refresh once.
		jwksMu.Lock()
		delete(jwksCache, jwksURI)
		jwksMu.Unlock()
		keys, err = oidcSigningKeys(ctx, jwksURI)
		if err != nil {
			return nil, err
		}
		pub = keys[header.Kid]
	}
	if pub == nil {
		if len(keys) == 1 {
			// A single-key set is unambiguous even when the kid is missing.
			for _, k := range keys {
				pub = k
			}
		} else {
			return nil, fmt.Errorf("oidc: no key matches kid %q", header.Kid)
		}
	}

	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("oidc: decode signature: %w", err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], signature); err != nil {
		return nil, fmt.Errorf("oidc: signature does not verify")
	}

	claimsRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("oidc: decode claims: %w", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(claimsRaw, &claims); err != nil {
		return nil, fmt.Errorf("oidc: parse claims: %w", err)
	}

	// Signature valid; now the claims have to mean what they say.
	if iss, _ := claims["iss"].(string); iss != "" && issuer != "" && iss != issuer {
		return nil, fmt.Errorf("oidc: issuer %q does not match %q", iss, issuer)
	}
	if !audienceMatches(claims["aud"], audience) {
		return nil, fmt.Errorf("oidc: audience does not include %q", audience)
	}
	if exp, ok := numericClaim(claims["exp"]); ok && time.Now().Unix() >= exp {
		return nil, fmt.Errorf("oidc: id_token expired")
	}
	if nbf, ok := numericClaim(claims["nbf"]); ok && time.Now().Unix() < nbf {
		return nil, fmt.Errorf("oidc: id_token is not valid yet")
	}
	// The nonce is what binds this token to the request that started the flow;
	// without it a token captured elsewhere could be replayed here.
	if nonce != "" {
		if got, _ := claims["nonce"].(string); got != nonce {
			return nil, fmt.Errorf("oidc: nonce mismatch")
		}
	}
	return claims, nil
}

// audienceMatches accepts either a string or an array `aud`.
func audienceMatches(raw any, want string) bool {
	switch v := raw.(type) {
	case string:
		return v == want
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && s == want {
				return true
			}
		}
	}
	return false
}

// numericClaim reads a JWT time claim, which JSON may deliver as a number.
func numericClaim(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int64:
		return n, true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	}
	return 0, false
}
