package oauth

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/sync/singleflight"
)

func init() {
	Register("minimax-code", refreshMiniMaxCode)
	Register("minimax-code-global", refreshMiniMaxCode)
}

// MiniMax Code refresh tokens are SINGLE-USE: sending the same one twice
// answers invalid_grant (HTTP 400) and signs the account out. Two paths reach
// refresh — the pre-dispatch expiry check and the executor's on-401 retry —
// and they may hold different snapshots of one connection, so the flight is
// keyed on the spent token value rather than on the connection id: a token
// already in the air resolves to the shared answer instead of a second send.
// Port of upstream open-sse/services/tokenRefresh/dedup.js (dedupRefresh),
// which upstream calls with the same (provider, oldToken) key.
var minimaxCodeFlight singleflight.Group

// miniMaxCodeSites maps each mcode provider id to its site's token endpoint.
// Sign-ins are per site — an account on account.minimax.cn says nothing about
// account.minimax.io — so the two are never cross-falled (AGENTS.md §3.A).
var miniMaxCodeSites = map[string]string{
	"minimax-code":        "https://account.minimax.cn/oauth2/token",
	"minimax-code-global": "https://account.minimax.io/oauth2/token",
}

const (
	miniMaxCodeClientID = "mcode-public"
	miniMaxCodeScope    = "agent.default"
	miniMaxCodeAudience = "agent-backend"
	// miniMaxCodeDefaultExpiresIn covers an answer that omits expires_in. mcode
	// access tokens live about an hour; storing zero instead would mark the
	// connection expired and refresh it again on every single request.
	miniMaxCodeDefaultExpiresIn = 3600
)

// refreshMiniMaxCode refreshes a MiniMax Code OAuth token.
//
// invalid_grant is the only answer that means the sign-in is gone; anything
// else (5xx, transport error) returns an error the caller's grant-dead check
// does not recognise, so the current access token stays usable and the
// connection is not parked on a blip.
func refreshMiniMaxCode(ctx context.Context, p *Params) (*TokenResult, error) {
	tokenURL, known := miniMaxCodeSites[p.Provider]
	if !known || p.RefreshToken == "" {
		return nil, fmt.Errorf("%s: unsupported provider or missing refresh_token", p.Provider)
	}

	vals := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {p.RefreshToken},
		"client_id":     {miniMaxCodeClientID},
		"scope":         {miniMaxCodeScope},
		"audience":      {miniMaxCodeAudience},
	}

	res, err, _ := minimaxCodeFlight.Do(p.Provider+":"+p.RefreshToken, func() (any, error) {
		result, err := doFormRefresh(ctx, minimaxCodeClient(p.Client), tokenURL, vals)
		if err != nil {
			// A spent token is reported as 400 invalid_grant, which the shared
			// OAuthRefreshError already carries; anything else (5xx, transport)
			// surfaces as-is and is not grant-dead.
			return nil, fmt.Errorf("%s: %w", p.Provider, err)
		}
		// MiniMax rotates the refresh token on every refresh. The old one is
		// kept only when the answer omits it, so a rotation is never lost to a
		// partial body and the next refresh does not resend a spent token.
		if result.RefreshToken == "" {
			result.RefreshToken = p.RefreshToken
		}
		if result.ExpiresIn <= 0 {
			result.ExpiresIn = miniMaxCodeDefaultExpiresIn
		}
		return result, nil
	})
	if err != nil {
		return nil, err
	}
	result, ok := res.(*TokenResult)
	if !ok || result == nil {
		return nil, fmt.Errorf("%s: refresh returned no token", p.Provider)
	}
	return result, nil
}

// minimaxCodeClient bounds the exchange. The caller's client is the shared
// proxy-aware one whenever it carries a timeout; otherwise a copy (or a fresh
// client) gets one, so no mcode refresh can hang indefinitely.
func minimaxCodeClient(client *http.Client) *http.Client {
	switch {
	case client == nil:
		return &http.Client{Timeout: 30 * time.Second}
	case client.Timeout > 0:
		return client
	default:
		clone := *client
		clone.Timeout = 30 * time.Second
		return &clone
	}
}
