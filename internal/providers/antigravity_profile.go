package providers

import "strings"

// AntigravityClientProfile selects which official client identity 9router
// presents to Google's Cloud Code / Antigravity backend.
//
// Google ships two clients that share the same OAuth client id and the same
// Code Assist backend but advertise different User-Agent fingerprints:
//
//   - "ide" — the Antigravity IDE desktop app (the historical behaviour).
//   - "cli" — the standalone Antigravity CLI (`agy`).
//
// The profile is an instance-wide setting, not a per-connection one: it names
// the client we imitate, which is the same for every account on the provider.
// It deliberately does not live on the connection, so accounts cannot drift
// onto different identities and a bug in one connection cannot change what
// another account presents.
//
// Why the profile matters: the Cloud Code backend answers HTTP 404 to the
// legacy `antigravity/ide/<ver> darwin/arm64` flavor for the Gemini 4 Argon
// tiered models, while the plain `antigravity/<ver>` shape the real IDE app
// sends is answered normally. The CLI profile is a genuinely different client
// (`antigravity/cli/<ver> (aidev_client; …)`) and some lanes expect it.
type AntigravityClientProfile string

const (
	// AntigravityProfileIDE is the Antigravity IDE fingerprint (default).
	AntigravityProfileIDE AntigravityClientProfile = "ide"
	// AntigravityProfileCLI is the standalone Antigravity CLI fingerprint.
	AntigravityProfileCLI AntigravityClientProfile = "cli"
)

// NormalizeAntigravityClientProfile coerces an arbitrary stored value into a
// known profile. Unknown or absent values fall back to "ide".
//
// The legacy synthetic names "harness"/"sdk" predate the official CLI profile
// and are still readable so previously stored connections keep working; new
// writes never produce them.
func NormalizeAntigravityClientProfile(value any) AntigravityClientProfile {
	s, ok := value.(string)
	if !ok {
		return AntigravityProfileIDE
	}
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "cli", "harness", "sdk":
		return AntigravityProfileCLI
	case "ide":
		return AntigravityProfileIDE
	default:
		return AntigravityProfileIDE
	}
}

// IsAntigravityClientProfile reports whether v is an accepted profile value
// (including legacy aliases) after normalization. Used to validate API input.
func IsAntigravityClientProfile(v any) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "ide", "cli", "harness", "sdk":
		return true
	default:
		return false
	}
}

// String returns the stored form of the profile. Callers that carry it as a
// plain string (settings JSON, quota tracker) use this rather than a cast.
func (p AntigravityClientProfile) String() string { return string(p) }

// AntigravityUserAgentFor returns the User-Agent header for the given profile.
//
//	ide → antigravity/<ide version>            (the IDE app's own shape)
//	cli → antigravity/cli/<cli version> (aidev_client; os_type=…; arch=…;
//	      cl=…; auth_method=consumer)          (the standalone `agy` CLI)
//
// The two stay different strings: the profile setting would be a no-op
// otherwise. Note that AntigravityIDEUserAgent in client_identity.go is pinned
// to the CLI shape because the legacy IDE flavor receives 404 on the Argon
// lanes; this function is the profile-aware replacement for callers that want
// the operator's choice honoured.
func AntigravityUserAgentFor(profile AntigravityClientProfile) string {
	if profile == AntigravityProfileCLI {
		return AntigravityCLIUserAgent
	}
	return "antigravity/" + AntigravityIDEVersion
}
