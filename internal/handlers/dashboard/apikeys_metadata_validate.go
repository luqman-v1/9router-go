package dashboard

import (
	json "encoding/json/v2"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"time"
)

// Validation for the two governance columns that carry a resale contract:
// apiKeys.expiresAt (when the contract ends) and apiKeys.metadata (who pays
// what, for how long). Both are written by HandleUpdateApiKey and read back by
// the dashboard, so a value that cannot be interpreted is refused at the write
// rather than stored and discovered later.

// maxAPIKeyMetadataBytes bounds the resale metadata blob. It is returned with
// every /api/keys listing and rendered in the dashboard dialog, so an unbounded
// blob would ride along on every call.
const maxAPIKeyMetadataBytes = 2048

// Bounds on the individual metadata fields. priceCents is a count of cents, so
// it stays a whole number far inside the range a float64 represents exactly.
const (
	maxMetadataCustomerIDLen = 128
	maxMetadataTextLen       = 500
	// Typed float64, and compared against the float64 the JSON decoder
	// produces: an untyped int constant here does not fit in a 32-bit int, so
	// the package failed to compile for armv7 and mips, which are the router
	// targets this ships to.
	maxMetadataPriceCents float64 = 100_000_000_000
)

// metadataBillingCycles is the accepted vocabulary for a contract's billing
// cycle. An unknown cycle would never match a renewal calculation, so it is
// refused here rather than stored as a string only its author understands.
var metadataBillingCycles = []string{"one-off", "monthly", "quarterly", "yearly"}

// metadataFieldRules maps every accepted metadata key to the rule its value must
// satisfy. An unknown key is refused instead of stored: this blob is the resale
// bookkeeping, and a typo ("customerID", "pricecent") would silently drop a key
// out of the revenue report instead of failing where an operator can see it.
var metadataFieldRules = map[string]func(value any) error{
	"customerId":   metadataString(maxMetadataCustomerIDLen),
	"contact":      metadataString(maxMetadataTextLen),
	"notes":        metadataString(maxMetadataTextLen),
	"priceCents":   metadataPriceCents,
	"billingCycle": metadataBillingCycle,
}

// validateAPIKeyMetadata accepts the blob stored on a key, or returns the reason
// to refuse it. An empty value is a deliberate clear.
func validateAPIKeyMetadata(meta string) error {
	if meta == "" {
		return nil
	}
	if len(meta) > maxAPIKeyMetadataBytes {
		return errValue(fmt.Sprintf("metadata must be at most %d bytes", maxAPIKeyMetadataBytes))
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(meta), &parsed); err != nil || parsed == nil {
		// JSON null parses into a nil map without an error, and a null blob is
		// not bookkeeping anyone can read back.
		return errValue("metadata must be a JSON object")
	}
	// Keys are visited in sorted order so the message names the same field every
	// time; a map's iteration order would otherwise pick a different one on each
	// attempt, which reads like a different problem each save.
	for _, key := range slices.Sorted(maps.Keys(parsed)) {
		rule, ok := metadataFieldRules[key]
		if !ok {
			return errValue(fmt.Sprintf("metadata has unknown key %q (accepted: %s)",
				key, strings.Join(slices.Sorted(maps.Keys(metadataFieldRules)), ", ")))
		}
		if err := rule(parsed[key]); err != nil {
			return errValue(fmt.Sprintf("metadata.%s %s", key, err))
		}
	}
	return nil
}

// metadataString requires a non-empty string within maxLen bytes.
func metadataString(maxLen int) func(any) error {
	return func(value any) error {
		s, ok := value.(string)
		if !ok {
			return errValue("must be a string")
		}
		if strings.TrimSpace(s) == "" {
			return errValue("must not be empty")
		}
		if len(s) > maxLen {
			return errValue(fmt.Sprintf("must be at most %d characters", maxLen))
		}
		return nil
	}
}

// metadataPriceCents requires a whole, non-negative amount. A fractional amount
// is refused rather than rounded: the operator typed a price, and changing it by
// a cent behind their back is worse than asking again.
func metadataPriceCents(value any) error {
	cents, ok := value.(float64)
	if !ok || math.IsNaN(cents) || math.IsInf(cents, 0) || math.Trunc(cents) != cents {
		return errValue("must be a whole number of cents")
	}
	if cents < 0 || cents > maxMetadataPriceCents {
		return errValue(fmt.Sprintf("must be between 0 and %.0f", maxMetadataPriceCents))
	}
	return nil
}

// metadataBillingCycle requires one of the known cycles.
func metadataBillingCycle(value any) error {
	cycle, ok := value.(string)
	if !ok {
		return errValue("must be a string")
	}
	if !slices.Contains(metadataBillingCycles, cycle) {
		return errValue("must be one of " + strings.Join(metadataBillingCycles, ", "))
	}
	return nil
}

// validateAPIKeyExpiry accepts the contract end date written on a key, or
// returns the reason to refuse it. An empty value means "no expiry".
//
// The format is load-bearing: the auth middleware enforces an expiry with
// time.Parse(time.RFC3339, ...) and skips a value that fails to parse, so a
// mistyped timestamp used to be stored as a contract that never ends.
func validateAPIKeyExpiry(expiresAt string) error {
	if expiresAt == "" {
		return nil
	}
	if _, err := time.Parse(time.RFC3339, expiresAt); err != nil {
		return errValue("expiresAt must be an RFC3339 timestamp such as 2026-12-31T23:59:59Z, or empty for no expiry")
	}
	return nil
}

// errValue is a validation failure whose message is returned verbatim in the 400
// body. It is the general counterpart of errNegative, which predates it.
type errValue string

func (e errValue) Error() string { return string(e) }
