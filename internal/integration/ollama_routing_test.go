//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"
)

// Ollama Cloud must be advertised as its own provider, because the whole of
// #192 was that the id "ollama" resolved to the self-hosted daemon. A catalog
// listing is checked rather than a proxied request because AddConnection
// installs a baseUrl override: a request would pass whether or not the registry
// default were right, and so would prove nothing about the default.
func TestOllamaCloudResolvesToCloudEndpoint(t *testing.T) {
	env := newEnv(t)

	res := env.Get(t, "/v1/models")
	if res.Status != http.StatusOK {
		t.Fatalf("GET /v1/models = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	var catalog struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	res.Decode(t, &catalog)

	var ollamaListed bool
	for _, m := range catalog.Data {
		if strings.HasPrefix(m.ID, "ollama/") {
			ollamaListed = true
			break
		}
	}
	if !ollamaListed {
		t.Fatalf("no ollama/ models in /v1/models (body: %s)", truncate(res.Body))
	}
}

// An Ollama Cloud connection pointed at a fake upstream must reach that
// upstream, and the model must arrive with the provider prefix stripped. This is
// the observable half of #192: the request that used to be refused is now
// proxied.
func TestOllamaCloudConnectionProxiesChat(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	env.AddConnection(t, "conn-ollama", "ollama", "Ollama Cloud Integration", upstream, "oll-test-key")

	res := env.Post(t, "/v1/chat/completions", ChatBody("ollama/gpt-oss:120b", false))
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}

	sent := upstream.Last(t)
	if sent.Path != "/chat/completions" {
		t.Errorf("upstream path = %q, want /chat/completions", sent.Path)
	}
	if got := sent.Model(t); got != "gpt-oss:120b" {
		t.Errorf("upstream model = %q, want \"gpt-oss:120b\" (the prefix must be stripped)", got)
	}
	if got := sent.Header.Get("Authorization"); got != "Bearer oll-test-key" {
		t.Errorf("upstream Authorization = %q, want the connection credential", got)
	}
}
