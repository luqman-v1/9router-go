// Package oauth provides per-provider OAuth token refresh.
// Register custom refresh functions for providers that need non-standard OAuth flows.
package oauth

import (
	"context"
	"fmt"
	"net/http"
	"sync"
)

// TokenResult holds the result of a token refresh.
type TokenResult struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int // seconds
	Scope        string
	ProjectID    string // provider-specific extra field
}

// Params holds all inputs for a refresh call.
type Params struct {
	Client       *http.Client
	Provider     string
	RefreshToken string
	AccessToken  string // current (possibly expired) token
	// ProviderSpecificData carries per-account OAuth material stored at login
	// time (e.g. Kiro clientId/clientSecret/region for AWS SSO OIDC refresh).
	ProviderSpecificData map[string]string
}

// Refresher refreshes an OAuth token for a specific provider.
type Refresher func(ctx context.Context, p *Params) (*TokenResult, error)

var (
	registryMu sync.RWMutex
	registry   = map[string]Refresher{}
)

// Register adds a refresher for the given provider.
func Register(provider string, fn Refresher) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[provider] = fn
}

// Get returns the refresher for the given provider, or nil.
func Get(provider string) Refresher {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return registry[provider]
}

// Unregister removes the refresher for a provider, restoring the entry to
// "none registered". Tests that register a stub under a real provider id need
// this to put the process-wide registry back the way they found it — a leaked
// stub silently changes what production code under test does.
func Unregister(provider string) {
	registryMu.Lock()
	defer registryMu.Unlock()
	delete(registry, provider)
}

// Refresh calls the provider's refresher, or falls back to standard OAuth2.
func Refresh(ctx context.Context, p *Params) (*TokenResult, error) {
	if fn := Get(p.Provider); fn != nil {
		return fn(ctx, p)
	}
	return nil, fmt.Errorf("no OAuth refresher for: %s", p.Provider)
}

// StringMap flattens a connection providerSpecificData blob to the string
// fields refreshers need (clientId/clientSecret/region). Non-string values
// are dropped.
func StringMap(m map[string]any) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if s, ok := v.(string); ok && s != "" {
			out[k] = s
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
