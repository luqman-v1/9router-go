package dashboard

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
)

// Duplicate connection detection.
//
// WHY THIS EXISTS
// Re-running an OAuth flow for an account that is already connected used to
// insert a second row. That is not cosmetic: the account fallback logic walks
// connections in order and locks the failing one per model, so a duplicate
// makes the router "fall back" to the same account's quota and lock it twice.
// A production database carried exactly this for codebuddy-intl — two rows six
// days apart, sharing one JWT `sub`.
//
// Identity is provider-specific and read from the credential, not from the
// label: a renamed account is still recognised as the same account.
//
// Providers without a stable identity (kiro, cloudflare-ai,
// openai-compatible-*) are deliberately NOT deduplicated — many connections
// there are legitimate, and collapsing them would destroy working accounts.

// ConnectionIdentity is the stable identity a provider can be matched on.
// Empty means "this provider has no stable identity; do not deduplicate".
type ConnectionIdentity struct {
	// Kind names the extraction rule that produced the value, so a mismatch
	// between two rows is visible in logs and tests.
	Kind string
	// Value is the comparable identity string.
	Value string
}

// IdentityOf extracts the stable identity of a connection from its stored
// credential. Returns an empty identity for providers that have none.
func IdentityOf(provider string, data map[string]any) ConnectionIdentity {
	if data == nil {
		return ConnectionIdentity{}
	}
	canonical := providers.ResolveAlias(provider)

	switch canonical {
	case "github":
		// GitHub's numeric user id is immutable; the login and email can both
		// change. Prefer the id, fall back to the login, then the email.
		if psd, ok := data["providerSpecificData"].(map[string]any); ok {
			if id, ok := psd["githubUserId"].(string); ok && strings.TrimSpace(id) != "" {
				return ConnectionIdentity{Kind: "githubUserId", Value: strings.TrimSpace(id)}
			}
			if login, ok := psd["githubLogin"].(string); ok && strings.TrimSpace(login) != "" {
				return ConnectionIdentity{Kind: "githubLogin", Value: strings.TrimSpace(login)}
			}
			if email, ok := psd["githubEmail"].(string); ok && strings.TrimSpace(email) != "" {
				return ConnectionIdentity{Kind: "githubEmail", Value: strings.TrimSpace(email)}
			}
		}

	case "antigravity":
		// Antigravity keys off the Google account email.
		if email, ok := data["email"].(string); ok && strings.TrimSpace(email) != "" {
			return ConnectionIdentity{Kind: "googleEmail", Value: strings.TrimSpace(email)}
		}
		if psd, ok := data["providerSpecificData"].(map[string]any); ok {
			if email, ok := psd["email"].(string); ok && strings.TrimSpace(email) != "" {
				return ConnectionIdentity{Kind: "googleEmail", Value: strings.TrimSpace(email)}
			}
		}

	case "codebuddy-cn", "codebuddy-intl", "codebuddy":
		// CodeBuddy and Antigravity-style JWTs key off the `sub` claim.
		if sub := jwtSubject(accessTokenOf(data)); sub != "" {
			return ConnectionIdentity{Kind: "jwtSubject", Value: sub}
		}
		if email, ok := data["email"].(string); ok && strings.TrimSpace(email) != "" {
			return ConnectionIdentity{Kind: "email", Value: strings.TrimSpace(email)}
		}
	}

	// A JWT access token with a `sub` claim is a usable identity for any
	// provider that issues one, which covers the OAuth flows that were
	// actually producing duplicates.
	if sub := jwtSubject(accessTokenOf(data)); sub != "" {
		return ConnectionIdentity{Kind: "jwtSubject", Value: sub}
	}
	return ConnectionIdentity{}
}

// accessTokenOf returns the stored access token, or the API key when the
// provider stores its credential there.
func accessTokenOf(data map[string]any) string {
	if t, ok := data["accessToken"].(string); ok && strings.TrimSpace(t) != "" {
		return t
	}
	if t, ok := data["apiKey"].(string); ok && strings.TrimSpace(t) != "" {
		return t
	}
	if t, ok := data["authToken"].(string); ok && strings.TrimSpace(t) != "" {
		return t
	}
	return ""
}

// jwtSubject decodes the `sub` claim of a JWT without verifying the signature.
// The signature is the provider's business; this is a local identity check
// against a token we already stored and already send upstream.
func jwtSubject(token string) string {
	if !strings.HasPrefix(token, "eyJ") {
		return ""
	}
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// Some encoders pad; try the standard alphabet before giving up.
		payload, err = base64.StdEncoding.DecodeString(parts[1])
		if err != nil {
			return ""
		}
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return strings.TrimSpace(claims.Sub)
}

// SameIdentity reports whether two connections belong to the same account.
// Both must have a non-empty identity of the same kind and value.
func SameIdentity(a, b ConnectionIdentity) bool {
	if a.Kind == "" || b.Kind == "" {
		return false
	}
	return a.Kind == b.Kind && a.Value == b.Value
}

// findDuplicateConnection returns the existing connection that holds the same
// account identity as the one being created, or nil when there is none.
// The second return value is that connection's parsed data blob, so the caller
// can report which identity matched without re-reading the row.
func (h *DashboardHandler) findDuplicateConnection(provider string, dataMap map[string]any) (*models.ProviderConnection, map[string]any) {
	incoming := IdentityOf(provider, dataMap)
	if incoming.Kind == "" {
		return nil, nil
	}
	conns, err := h.Repo.GetProviderConnections("", false)
	if err != nil {
		return nil, nil
	}
	for _, c := range conns {
		if c == nil || c.ID == "" {
			continue
		}
		// Only compare within the same provider: two providers may legitimately
		// hold the same Google account, and they are not duplicates of each
		// other.
		if providers.ResolveAlias(c.Provider) != providers.ResolveAlias(provider) {
			continue
		}
		var existing map[string]any
		if strings.TrimSpace(c.Data) == "" {
			continue
		}
		if err := json.Unmarshal([]byte(c.Data), &existing); err != nil {
			continue
		}
		if SameIdentity(incoming, IdentityOf(c.Provider, existing)) {
			return c, existing
		}
	}
	return nil, nil
}

// connectionDisplayName is the label shown to the operator for a connection.
func connectionDisplayName(c *models.ProviderConnection) string {
	if c == nil {
		return ""
	}
	if c.Name != nil && strings.TrimSpace(*c.Name) != "" {
		return *c.Name
	}
	if c.Email != nil && strings.TrimSpace(*c.Email) != "" {
		return *c.Email
	}
	return c.Provider
}
