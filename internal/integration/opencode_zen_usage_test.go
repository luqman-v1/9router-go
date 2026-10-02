//go:build integration

package integration

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An opencode-zen connection must show up in the dashboard quota tracker, not
// only on the engine routes. Before the port it was filtered out entirely
// (registry features.usage / features.usageApikey), so the provider page had
// no account to refresh.
func TestOpenCodeZenIsListedForQuotaTracking(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, chatCompletionResponder())
	addZenConnection(t, env, up)

	res := env.Get(t, "/api/providers/client", WithAPIKey(testAPIKey))
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, body %s", res.Status, truncate(res.Body))
	}
	var page struct {
		Connections []struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
		} `json:"connections"`
		ProviderOptions []string `json:"providerOptions"`
	}
	res.Decode(t, &page)
	if len(page.Connections) != 1 {
		t.Fatalf("want exactly the zen connection, got %+v", page.Connections)
	}
	if page.Connections[0].Provider != "opencode-zen" {
		t.Errorf("provider = %q", page.Connections[0].Provider)
	}
	if len(page.ProviderOptions) != 1 || page.ProviderOptions[0] != "opencode-zen" {
		t.Errorf("providerOptions = %v, want [opencode-zen]", page.ProviderOptions)
	}
}

// A usage host the test owns is the only offline-safe way to assert the
// shape; the registry default would dial the real provider.
func TestOpenCodeZenQuotaParsesProviderShape(t *testing.T) {
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/zen/v1/usage" {
			t.Errorf("usage path = %q, want /zen/v1/usage", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-zen" {
			t.Errorf("usage auth = %q, want Bearer sk-zen", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"usage":{"rolling":{"percent":18.25,"resetsAt":"2026-10-05T00:00:00Z"},"weekly":{"percent":41}}}`))
	}))
	t.Cleanup(usage.Close)

	env := newEnv(t)
	addZenConnectionWithData(t, env, usage.URL+"/zen/v1/chat/completions")

	res := env.Get(t, "/api/usage/conn-ocz", WithAPIKey(testAPIKey))
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, body %s", res.Status, truncate(res.Body))
	}
	for _, want := range []string{`"plan":"OpenCode Zen"`, `"used":18.25`, `"remaining":81.75`, `"used":41`, `"resetAt":"2026-10-05T00:00:00Z"`} {
		if !strings.Contains(string(res.Body), want) {
			t.Errorf("quota payload missing %s: %s", want, truncate(res.Body))
		}
	}
}

