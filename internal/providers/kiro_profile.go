package providers

// Public default CodeWhisperer profile ARNs (us-east-1), keyed by sign-in
// method. Used when a Builder ID / social account has no profileArn of its
// own. Mirrors decolua/9router open-sse/config/kiroConstants.js
// (KIRO_DEFAULT_PROFILE_ARNS, resolveDefaultProfileArn).
const (
	KiroDefaultProfileARNBuilderID = "arn:aws:codewhisperer:us-east-1:638616132270:profile/AAAACCCCXXXX"
	KiroDefaultProfileARNSocial    = "arn:aws:codewhisperer:us-east-1:699475941385:profile/EHGA3GRVQMUK"
)

// kiroAccountBoundAuth lists auth methods whose profile is tied to one account
// or token. The shared default ARN belongs to another account for these and
// makes CodeWhisperer answer 403, so they never fall back to it.
var kiroAccountBoundAuth = map[string]bool{
	"api_key":      true,
	"idc":          true,
	"external_idp": true,
}

// KiroEffectiveProfileARN returns the profileArn to send upstream for a
// connection. An explicit per-connection profileArn always wins. Otherwise
// account-bound auth sends nothing, and OAuth/social sign-ins fall back to the
// shared default for their method.
func KiroEffectiveProfileARN(authMethod, profileARN string) string {
	if profileARN != "" {
		return profileARN
	}
	if kiroAccountBoundAuth[authMethod] {
		return ""
	}
	return KiroDefaultProfileARN(authMethod)
}

// KiroDefaultProfileARN resolves the shared default profile for an auth method.
// Google and GitHub sign-ins use the social profile; everything else, Builder
// ID included, uses the Builder ID profile.
func KiroDefaultProfileARN(authMethod string) string {
	if authMethod == "google" || authMethod == "github" {
		return KiroDefaultProfileARNSocial
	}
	return KiroDefaultProfileARNBuilderID
}
