package observ

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// maxLabelLen bounds every label value. Model names and error classes come
// from outside the gateway, so an unbounded value in a label is a memory leak
// with a query attached.
const maxLabelLen = 64

// unknownLabel stands in for an empty value. An empty label value is legal in
// Prometheus but reads as a rendering bug in a dashboard and collides with
// "not set" from every other exporter.
const unknownLabel = "unknown"

// Label returns a bounded, non-empty label value that is safe to expose.
//
// Values that look like a credential are collapsed to unknownLabel rather
// than truncated: design rule 6 (never expose a plaintext key) has to hold
// even when a future call site hands the wrong argument to the wrong
// collector, and a truncated key is still a leaked prefix.
func Label(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || looksLikeCredential(v) {
		return unknownLabel
	}
	if len(v) > maxLabelLen {
		v = v[:maxLabelLen]
	}
	return v
}

// looksLikeCredential reports whether v is shaped like an API key or bearer
// token rather than like a provider name, model name or HTTP path.
func looksLikeCredential(v string) bool {
	lower := strings.ToLower(v)
	if strings.HasPrefix(lower, "sk-") || strings.HasPrefix(lower, "sk_") ||
		strings.HasPrefix(lower, "bearer ") || strings.HasPrefix(lower, "bearer\t") {
		return true
	}
	// A long opaque run of mixed-case letters and digits with no separator is
	// a token; every real provider/model/endpoint label contains a separator
	// or a lowercase word.
	if len(v) < 24 || strings.ContainsAny(v, "-_/.: ") {
		return false
	}
	digits, lowerCount, upperCount := 0, 0, 0
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r >= 'a' && r <= 'z':
			lowerCount++
		case r >= 'A' && r <= 'Z':
			upperCount++
		}
	}
	if digits+lowerCount+upperCount != len(v) {
		return false
	}
	// Mixed case plus digits over a long run: no such provider name exists.
	return upperCount > 0 && digits > 0
}

// KeyRef derives a short, stable, non-reversible id from a key identifier.
// It exists so per-key visibility is possible without ever putting a
// credential — or a value that identifies one by inspection — in a label.
func KeyRef(keyID string) string {
	keyID = strings.TrimSpace(keyID)
	if keyID == "" {
		return unknownLabel
	}
	sum := sha256.Sum256([]byte(keyID))
	return hex.EncodeToString(sum[:4])
}