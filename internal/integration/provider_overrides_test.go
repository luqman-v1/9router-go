//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"

	"9router/proxy/internal/auth"
	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

// Issue #101 part 4: a stored per-provider header override has to reach the
// wire, and it has to win over what the registry sends. This runs through the
// real router so the assertion is about a header the fake upstream actually
// received, not about a map inside a handler.
func TestProviderHeaderOverrideReachesUpstream(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	env.AddConnection(t, "conn-deepseek", "deepseek", "DeepSeek", upstream, "sk-upstream")

	if err := env.Repo.SetProviderOverride("deepseek", &db.ProviderOverrides{
		Headers: map[string]string{"X-Tenant": "acme", "X-Trace": "abc-123"},
	}); err != nil {
		t.Fatalf("store override: %v", err)
	}

	res := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false))
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d: %s", res.Status, truncate(res.Body))
	}

	seen := upstream.Last(t)
	if got := seen.Header.Get("X-Tenant"); got != "acme" {
		t.Errorf("upstream X-Tenant = %q, want the stored override %q", got, "acme")
	}
	if got := seen.Header.Get("X-Trace"); got != "abc-123" {
		t.Errorf("upstream X-Trace = %q, want the stored override %q", got, "abc-123")
	}
	// The credential is the gateway's business, not the operator's.
	if got := seen.Header.Get("Authorization"); !strings.Contains(got, "sk-upstream") {
		t.Errorf("upstream Authorization = %q, want the connection's own key untouched", got)
	}
}

// An override wins over the registry's own static header — that is the whole
// point of the feature, and upstream's Object.assign order makes it explicit.
func TestProviderHeaderOverrideBeatsRegistryHeader(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	env.AddConnection(t, "conn-opencode", "opencode", "OpenCode", upstream, "sk-upstream")

	// opencode ships x-opencode-client: desktop from the registry.
	if providers.KnownProviders["opencode"].StaticHeaders["x-opencode-client"] != "desktop" {
		t.Fatalf("fixture changed: opencode no longer sends x-opencode-client=desktop")
	}
	if err := env.Repo.SetProviderOverride("opencode", &db.ProviderOverrides{
		Headers: map[string]string{"x-opencode-client": "cli"},
	}); err != nil {
		t.Fatalf("store override: %v", err)
	}

	res := env.Post(t, "/v1/chat/completions", ChatBody("opencode/big-pickle", false))
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d: %s", res.Status, truncate(res.Body))
	}
	if got := upstream.Last(t).Header.Get("x-opencode-client"); got != "cli" {
		t.Errorf("upstream x-opencode-client = %q, want the override to win with %q", got, "cli")
	}
}

// An override stored under a provider alias must apply to a request that
// arrives carrying that alias, and one stored for another provider must not
// leak into it.
func TestProviderHeaderOverrideIsScopedToItsProvider(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	env.AddConnection(t, "conn-deepseek", "deepseek", "DeepSeek", upstream, "sk-upstream")

	if err := env.Repo.SetProviderOverride("kimi", &db.ProviderOverrides{
		Headers: map[string]string{"X-Tenant": "other-provider"},
	}); err != nil {
		t.Fatalf("store override: %v", err)
	}
	res := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false))
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d: %s", res.Status, truncate(res.Body))
	}
	if got := upstream.Last(t).Header.Get("X-Tenant"); got != "" {
		t.Errorf("a deepseek request picked up kimi's override (%q)", got)
	}
}

// The dashboard must refuse a header that would re-point the credential, and
// the refusal has to leave the stored entry alone.
func TestProviderHeaderOverrideCannotStealCredentials(t *testing.T) {
	env := newEnv(t)

	saved := env.Put(t, "/api/providers/deepseek/overrides",
		map[string]any{"headers": map[string]string{"X-Tenant": "acme"}},
		WithHeader(auth.CLITokenHeader, auth.CLIToken()))
	if saved.Status != http.StatusOK {
		t.Fatalf("seed PUT status = %d: %s", saved.Status, truncate(saved.Body))
	}

	stolen := env.Put(t, "/api/providers/deepseek/overrides",
		map[string]any{"headers": map[string]string{"Authorization": "Bearer someone-else"}},
		WithHeader(auth.CLITokenHeader, auth.CLIToken()))
	if stolen.Status != http.StatusBadRequest {
		t.Fatalf("Authorization override status = %d, want 400", stolen.Status)
	}

	read := env.Get(t, "/api/providers/deepseek/overrides",
		WithHeader(auth.CLITokenHeader, auth.CLIToken()))
	if read.Status != http.StatusOK {
		t.Fatalf("GET status = %d", read.Status)
	}
	if !strings.Contains(string(read.Body), "acme") {
		t.Errorf("the rejected write disturbed the stored entry: %s", truncate(read.Body))
	}
}
