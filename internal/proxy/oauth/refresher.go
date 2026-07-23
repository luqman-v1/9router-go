// Package oauth provides per-provider OAuth token refresh.
// Register custom refresh functions for providers that need non-standard OAuth flows.
package oauth

import (
	"context"
	"fmt"
	"net/http"
)

// TokenResult holds the result of a token refresh.
type TokenResult struct {
	AccessToken string
	ExpiresIn   int // seconds
	Scope       string
	ProjectID   string // provider-specific extra field
}

// Params holds all inputs for a refresh call.
type Params struct {
	Client       *http.Client
	Provider     string
	RefreshToken string
	AccessToken  string // current (possibly expired) token
}

// Refresher refreshes an OAuth token for a specific provider.
type Refresher func(ctx context.Context, p *Params) (*TokenResult, error)

var registry = map[string]Refresher{}

// Register adds a refresher for the given provider.
func Register(provider string, fn Refresher) {
	registry[provider] = fn
}

// Get returns the refresher for the given provider, or nil.
func Get(provider string) Refresher {
	return registry[provider]
}

// Refresh calls the provider's refresher, or falls back to standard OAuth2.
func Refresh(ctx context.Context, p *Params) (*TokenResult, error) {
	if fn := Get(p.Provider); fn != nil {
		return fn(ctx, p)
	}
	return nil, fmt.Errorf("no OAuth refresher for: %s", p.Provider)
}
