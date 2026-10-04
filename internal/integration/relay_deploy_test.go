//go:build integration

package integration

import (
	"net/http"
	"testing"
	"time"

	"9router/proxy/internal/auth"
)

// TestRelayDeployRoutesAcceptDashboardSession pins that the three relay-deploy
// endpoints are reachable the way the dashboard SPA actually calls them.
//
// They were mounted under RequireApiKey at /proxy-pools/{platform}-deploy while
// the SPA (web/src/api/client.ts deployVercelRelay/deployDenoRelay/
// deployCloudflareRelay) authenticated with the session cookie and no engine
// key, so every Deploy button answered 401 "Authentication required."
// (issue #140). The fix moves them to /api/proxy-pools/*-deploy inside the
// dashboard group.
//
// Each case asserts on the handler's own validation message, not on a 401 vs
// 200 split: a 401 proves the guard rejected the caller, but only reaching the
// handler proves the cookie was accepted, and a body that merely is not the
// guard's text would pass even if some other middleware 403'd. The platform
// calls themselves are never made — the handlers bail out before any network
// access on an empty token.
func TestRelayDeployRoutesAcceptDashboardSession(t *testing.T) {
	env := newEnv(t)
	if err := env.Repo.UpdateSettingsRaw(map[string]any{"requireLogin": true}); err != nil {
		t.Fatalf("set requireLogin: %v", err)
	}
	session, err := auth.Sign(auth.Secret(), time.Now())
	if err != nil {
		t.Fatalf("sign session token: %v", err)
	}
	sessionOnly := []RequestOpt{WithAPIKey(""), WithHeader("Cookie", auth.CookieName+"="+session)}

	tests := []struct {
		name string
		path string
		// wantMissingField is the handler's own message for an empty payload,
		// proving the request got past the auth guard and into the handler.
		wantMissingField string
	}{
		{
			name: "deno",
			path: "/api/proxy-pools/deno-deploy",
			// Deno's handler checks the org domain before the token, same
			// order as upstream deno-deploy/route.js:54-60.
			wantMissingField: "Organization domain is required",
		},
		{
			name:             "vercel",
			path:             "/api/proxy-pools/vercel-deploy",
			wantMissingField: "Vercel API token is required",
		},
		{
			name:             "cloudflare",
			path:             "/api/proxy-pools/cloudflare-deploy",
			wantMissingField: "Cloudflare Account ID and API Token are required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name+" with a dashboard session cookie", func(t *testing.T) {
			res := env.Post(t, tt.path, map[string]any{}, sessionOnly...)
			if res.Status != http.StatusBadRequest {
				t.Fatalf("POST %s with a session cookie = %d, want 400 from the handler's own validation (body: %s)",
					tt.path, res.Status, truncate(res.Body))
			}
			if got := res.ErrorMessage(t); got != tt.wantMissingField {
				t.Fatalf("POST %s message = %q, want %q", tt.path, got, tt.wantMissingField)
			}
		})

		t.Run(tt.name+" rejects an anonymous caller", func(t *testing.T) {
			res := env.Post(t, tt.path, map[string]any{}, WithoutAPIKey())
			if res.Status != http.StatusUnauthorized {
				t.Fatalf("POST %s without credentials = %d, want 401 (body: %s)",
					tt.path, res.Status, truncate(res.Body))
			}
		})
	}
}

// TestRelayDeployRoutesRejectEngineKeyOnEmptyToken is the other half of the
// dashboard guard: RequireDashboardAuth still admits a client API key for CLI
// compatibility, so moving the routes must not have left them open to any
// bearer key. Both credential carriers land on the same 400.
func TestRelayDeployRoutesRejectEngineKeyOnEmptyToken(t *testing.T) {
	env := newEnv(t)
	if err := env.Repo.UpdateSettingsRaw(map[string]any{"requireLogin": true}); err != nil {
		t.Fatalf("set requireLogin: %v", err)
	}

	res := env.Post(t, "/api/proxy-pools/deno-deploy", map[string]any{})
	if res.Status != http.StatusBadRequest {
		t.Fatalf("POST /api/proxy-pools/deno-deploy with the client api key = %d, want 400 (body: %s)",
			res.Status, truncate(res.Body))
	}
	if got := res.ErrorMessage(t); got != "Organization domain is required" {
		t.Fatalf("message = %q, want the handler's own validation", got)
	}
}
