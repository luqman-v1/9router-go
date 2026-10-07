// Package keikey hashes, verifies and masks client API keys.
//
// Keys are never stored in plaintext: the apiKeys table keeps an argon2id
// verifier (keyHash), a SHA-256 index for O(1) lookup (lookupHash) and a
// masked display value (keyDisplay). Legacy rows created before F-6 still
// hold the plaintext in `key` and are self-healed on first successful auth.
package keikey

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Frozen argon2id cost parameters (F-6). Changing any of these invalidates
// every stored verifier, so they are constants rather than config.
const (
	// Argon2Memory is the memory cost in KiB (64 MiB, OWASP recommendation).
	Argon2Memory uint32 = 64 * 1024
	// Argon2Iterations is the time cost.
	Argon2Iterations uint32 = 3
	// Argon2Parallelism is the number of lanes.
	Argon2Parallelism uint8 = 4
	// Argon2SaltLength is the random salt length in bytes.
	Argon2SaltLength = 32
	// Argon2KeyLength is the derived key length in bytes.
	Argon2KeyLength = 32
)

// maskPrefixLen / maskSuffixLen control the masked display value.
const (
	maskPrefixLen = 4 // "sk-a"
	maskSuffixLen = 4 // "wxyz"
	maskMinLen    = 12
)

// MaskedUnknown is returned when there is nothing meaningful to show.
const MaskedUnknown = "***"

// LookupHash returns the hex SHA-256 of the plaintext key. It is the indexed
// column used to find the row before spending an argon2id derivation.
func LookupHash(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// Hash derives a new argon2id verifier for plaintext in PHC string format:
//
//	$argon2id$v=19$m=65536,t=3,p=4$<b64 salt>$<b64 key>
func Hash(plaintext string) (string, error) {
	salt := make([]byte, Argon2SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("keikey.Hash: read salt: %w", err)
	}
	key := argon2.IDKey([]byte(plaintext), salt, Argon2Iterations, Argon2Memory, Argon2Parallelism, Argon2KeyLength)
	b64Salt := base64.RawStdEncoding.EncodeToString(salt)
	b64Key := base64.RawStdEncoding.EncodeToString(key)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, Argon2Memory, Argon2Iterations, Argon2Parallelism, b64Salt, b64Key), nil
}

// Verify reports whether plaintext produces the key encoded in the PHC
// verifier. A malformed or unsupported verifier returns false without
// erroring outward; callers only ever need a yes/no. A wrong plaintext still
// runs the full KDF against the stored parameters before the comparison, so
// the cost does not depend on how much of the secret was guessed.
func Verify(plaintext, encoded string) bool {
	memory, iterations, parallelism, salt, want, ok := decodeVerifier(encoded)
	if !ok {
		return false
	}
	got := argon2.IDKey([]byte(plaintext), salt, iterations, memory, parallelism, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// Mask renders a non-reversible display value, e.g. "sk-a…wxyz". Short or
// empty input collapses to "***" so no useful entropy leaks.
func Mask(plaintext string) string {
	if len(plaintext) <= maskMinLen {
		return MaskedUnknown
	}
	return plaintext[:maskPrefixLen] + "…" + plaintext[len(plaintext)-maskSuffixLen:]
}

// decodeVerifier parses a PHC argon2id string. ok is false for anything
// malformed; the returned values are zero when ok is false.
func decodeVerifier(encoded string) (memory uint32, iterations uint32, parallelism uint8, salt, want []byte, ok bool) {
	parts := strings.Split(encoded, "$")
	// "", "argon2id", "v=19", "m=..,t=..,p=..", salt, key
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return 0, 0, 0, nil, nil, false
	}
	if !strings.HasPrefix(parts[2], "v=") {
		return 0, 0, 0, nil, nil, false
	}
	var mem, t uint64
	var p uint64
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &t, &p); err != nil {
		return 0, 0, 0, nil, nil, false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return 0, 0, 0, nil, nil, false
	}
	want, err = base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return 0, 0, 0, nil, nil, false
	}
	return uint32(mem), uint32(t), uint8(p), salt, want, true
}
