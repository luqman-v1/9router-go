package oauth

import (
	"time"

	"9router/proxy/internal/providers"
)

// RegisterAll registers all built-in OAuth refreshers.
// Standard OAuth2 providers (with client_id + token_url) are registered from KnownOAuthConfigs.
func RegisterAll() {
	for name, cfg := range providers.KnownOAuthConfigs {
		if cfg.ClientID == "" || cfg.TokenURL == "" {
			continue
		}
		// Skip providers that have their own custom refresher
		if Get(name) != nil {
			continue
		}
		Register(name, NewStandardRefresher(cfg.TokenURL, cfg.ClientID, cfg.ClientSecret))
	}
}

// BuildConnectionUpdate builds a DB update map from a refresh result.
func BuildConnectionUpdate(result *TokenResult) map[string]interface{} {
	return map[string]interface{}{
		"accessToken": result.AccessToken,
		"expiresAt":   time.Now().Add(time.Duration(result.ExpiresIn) * time.Second).Format(time.RFC3339),
	}
}
