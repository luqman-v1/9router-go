package db

import (
	"fmt"
	"net/http"
	"strings"
)

// ProviderOverrides is what an operator may add to one provider's outbound
// request on top of what the registry already sends. It mirrors upstream's
// `providerOverrides` settings entry (decolua/9router v0.5.95,
// src/app/api/providers/[id]/overrides/route.js).
type ProviderOverrides struct {
	Headers map[string]string `json:"headers,omitempty"`
}

const (
	// maxOverrideHeaders and maxOverrideHeaderValue bound what an operator can
	// put into one request. They are upstream's numbers, not ours: the whole
	// point of the feature is that an operator who has read the upstream
	// limits is not surprised by a different ceiling.
	maxOverrideHeaders         = 20
	maxOverrideHeaderValueSize = 8192

	// MaxOverrideHeaders and MaxOverrideHeaderValueSize are the exported form
	// of the same limits, for callers that report them rather than enforce
	// them — the migration dry run warns instead of rejecting.
	MaxOverrideHeaders         = maxOverrideHeaders
	MaxOverrideHeaderValueSize = maxOverrideHeaderValueSize
)

// blockedOverrideHeaders are the request-structure and credential headers an
// override must never touch. Host and Content-Length describe a request whose
// body and destination are already decided by the gateway; Content-Type and
// Transfer-Encoding break framing; Connection breaks keep-alive; and
// Authorization/Cookie would let an override silently re-point the traffic at
// another account. The list is upstream's.
var blockedOverrideHeaders = map[string]bool{
	"host":              true,
	"content-length":    true,
	"content-type":      true,
	"connection":        true,
	"transfer-encoding": true,
	"authorization":     true,
	"cookie":            true,
}

// NormalizeProviderOverrides validates a payload and returns the stored form.
// A payload with no usable headers normalizes to nil, which callers store as a
// deletion — so "clear this provider" needs no separate endpoint.
//
// Validation lives here rather than in the HTTP handler so it cannot be
// bypassed by a second write path, and so the same rules apply to whatever
// reads the settings back.
func NormalizeProviderOverrides(headers map[string]string) (*ProviderOverrides, error) {
	if len(headers) == 0 {
		return nil, nil
	}
	if len(headers) > maxOverrideHeaders {
		return nil, fmt.Errorf("too many headers (max %d)", maxOverrideHeaders)
	}
	clean := make(map[string]string, len(headers))
	canonicalMap := make(map[string]string, len(headers))
	for name, value := range headers {
		if err := validateOverrideHeader(name, value); err != nil {
			return nil, err
		}
		// Two spellings of one header are one header on the wire. Accepting
		// both would send whichever the map iterated last, so a typo in casing
		// would silently replace a value instead of being reported.
		lower := strings.ToLower(name)
		if prev, seen := canonicalMap[lower]; seen && prev != value {
			return nil, fmt.Errorf("conflicting case-variant headers for %s", name)
		}
		canonicalMap[lower] = value
		clean[http.CanonicalHeaderKey(name)] = value
	}
	return &ProviderOverrides{Headers: clean}, nil
}

// isRFC7230Tchar reports whether r is a tchar per RFC 7230 §3.2.6: the full
// token alphabet, not the hyphen-and-alphanumerics subset this used to allow.
// Vendors do use the rest of it (`X-!#$%&'*+.^_\`|~`), and a name rejected for
// being legal is a bug in the validator, not in the request.
func isRFC7230Tchar(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == '!' || r == '#' || r == '$' || r == '%' || r == '&' || r == '\'' ||
		r == '*' || r == '+' || r == '-' || r == '.' || r == '^' || r == '_' ||
		r == '`' || r == '|' || r == '~':
		return true
	default:
		return false
	}
}

// validateOverrideHeaderValue rejects the bytes net/http would refuse to send or
// would read as a delimiter. CR and LF are the injection case; NUL and DEL are
// in no field of an HTTP message; the remaining C0 controls are rejected too,
// with HTAB the one exception the grammar allows.
func validateOverrideHeaderValue(name, val string) error {
	for i := 0; i < len(val); i++ {
		b := val[i]
		if b == 0x00 || b == 0x7f || b == '\r' || b == '\n' {
			return fmt.Errorf("header %s forbidden char", name)
		}
		if b < 0x20 && b != 0x09 {
			return fmt.Errorf("header %s invalid byte 0x%02x", name, b)
		}
	}
	return nil
}

// validateOverrideHeader applies upstream's trust-boundary rules to one pair.
func validateOverrideHeader(name, value string) error {
	if name == "" {
		return fmt.Errorf("header name cannot be empty")
	}
	// Rejecting a non-tchar keeps a name from carrying a colon (which net/http
	// reads as name/value) or a space (which splits the line); the value check
	// below is what stops header injection.
	for _, r := range name {
		if !isRFC7230Tchar(r) {
			return fmt.Errorf("invalid header name: %s", name)
		}
	}
	if blockedOverrideHeaders[strings.ToLower(name)] {
		return fmt.Errorf("header %s cannot be overridden", name)
	}
	if err := validateOverrideHeaderValue(name, value); err != nil {
		return err
	}
	if len(value) > maxOverrideHeaderValueSize {
		return fmt.Errorf("header %s value too long (max %d)", name, maxOverrideHeaderValueSize)
	}
	return nil
}

// HeaderOverrideBlocked reports whether name is one an override may never
// carry. The API answers with it so the UI can disable the field and say why
// instead of letting the operator discover it through a 400.
func HeaderOverrideBlocked(name string) bool {
	return blockedOverrideHeaders[strings.ToLower(strings.TrimSpace(name))]
}
