//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// TestV1AddServesTheSPA pins the one thing a package test cannot see: the real
// binary's route table. A /v1/add registered only in dashboard.RegisterRoutes
// (the test table) answers 404 here while every unit test stays green.
func TestV1AddServesTheSPA(t *testing.T) {
	env := newEnv(t)

	res := env.Get(t, "/v1/add", WithoutAPIKey())
	if res.Status != http.StatusOK {
		t.Fatalf("GET /v1/add = %d, want 200: %s", res.Status, truncate(res.Body))
	}
	if !strings.Contains(string(res.Body), "<html") && !strings.Contains(string(res.Body), "<!doctype") {
		t.Fatalf("GET /v1/add did not serve the SPA document: %.120s", res.Body)
	}
}

// TestAddServesTheSPAAlias pins the bare /add form. RequestLogger
// (internal/middleware/logging.go:70-77) strips a leading /v1/ from
// req.URL.Path before the router runs, so "/add" is the pattern that actually
// matches and /v1/add reaches it through the strip. Registering "/v1/add" as
// the pattern leaves both spellings 404 — that was the first attempt, and this
// test is what stops it coming back.
func TestAddServesTheSPAAlias(t *testing.T) {
	env := newEnv(t)

	res := env.Get(t, "/add", WithoutAPIKey())
	if res.Status != http.StatusOK {
		t.Fatalf("GET /add = %d, want 200: %s", res.Status, truncate(res.Body))
	}
	if !strings.Contains(string(res.Body), "<html") && !strings.Contains(string(res.Body), "<!doctype") {
		t.Fatalf("GET /add did not serve the SPA document: %.120s", res.Body)
	}
}

// TestV1AddFragmentNeverReachesTheServer is the contract the whole share-link
// design rests on: the credential rides in the fragment, which an HTTP client
// strips exactly as a browser does. A regression that moves it into the query
// string would put a live key into every access log and Referer.
func TestV1AddFragmentNeverReachesTheServer(t *testing.T) {
	env := newEnv(t)

	// The fragment is not part of the request: net/url drops it when the URL is
	// parsed, so this documents the transport rather than testing the parser.
	res := env.Get(t, "/v1/add", WithoutAPIKey())
	if res.Status != http.StatusOK {
		t.Fatalf("GET /v1/add = %d, want 200", res.Status)
	}
	if strings.Contains(string(res.Body), "sk-integration") {
		t.Fatal("server-rendered body leaked a credential")
	}
}

// TestProviderShareLinkBulkBlockWritesEveryKey walks the bulk form of the same
// flow. A keys= link with one entry must still go down the bulk path: api= and
// keys= are mutually exclusive, so a one-key block that took the single path
// would store an empty credential.
func TestProviderShareLinkBulkBlockWritesEveryKey(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"m-1"}]}`))
			return
		}
		chatCompletionResponder()(w, r)
	})

	const prov = "openai-compatible-chat-bulkme"
	nodeRes := env.Post(t, "/api/provider-nodes", map[string]any{
		"name": "Bulk Me", "prefix": "bulkme", "apiType": "chat",
		"baseUrl": up.URL + "/v1", "type": "openai-compatible", "urlSuffix": "bulkme",
	})
	if nodeRes.Status != http.StatusCreated {
		t.Fatalf("create node: %d %s", nodeRes.Status, string(nodeRes.Body))
	}

	// Three keys, in the order the link lists them.
	for i, key := range []string{"sk-a", "sk-b", "sk-c"} {
		res := env.Post(t, "/api/connections", map[string]any{
			"provider": prov, "authType": "compatible", "apiKey": key,
			"name": fmt.Sprintf("Bulk Key %d", i+1), "priority": i + 1,
		})
		if res.Status != http.StatusOK && res.Status != http.StatusCreated {
			t.Fatalf("create connection %d: %d %s", i+1, res.Status, string(res.Body))
		}
	}

	// All three rows exist under the node, and each carries its own key.
	conns, err := env.Repo.GetProviderConnections(prov, false)
	if err != nil {
		t.Fatalf("read connections: %v", err)
	}
	if len(conns) != 3 {
		t.Fatalf("connection count = %d, want 3", len(conns))
	}
	seen := map[string]bool{}
	for _, c := range conns {
		var data struct {
			APIKey string `json:"apiKey"`
		}
		if c.Data != "" {
			if err := json.Unmarshal([]byte(c.Data), &data); err != nil {
				t.Fatalf("decode connection data: %v", err)
			}
		}
		seen[data.APIKey] = true
	}
	for _, want := range []string{"sk-a", "sk-b", "sk-c"} {
		if !seen[want] {
			t.Fatalf("no connection carries %q; got %v", want, seen)
		}
	}
}

// TestProviderShareLinkAddsNodeConnectionAndModels walks the whole add flow the
// share modal builds a link for: create the node from the link's fields, store
// the key, fetch the upstream catalogue through the connection, and send a chat
// request through the new alias. Every step is a real dashboard REST call on the
// production router, so a change to any of them fails here.
func TestProviderShareLinkAddsNodeConnectionAndModels(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"m-1"},{"id":"m-2"}]}`))
			return
		}
		chatCompletionResponder()(w, r)
	})

	const prov = "openai-compatible-chat-shareme"

	// 1. The node, exactly as the receiver modal creates it from the link.
	nodeRes := env.Post(t, "/api/provider-nodes", map[string]any{
		"name": "Share Me", "prefix": "shareme", "apiType": "chat",
		"baseUrl": up.URL + "/v1", "type": "openai-compatible", "urlSuffix": "shareme",
	})
	if nodeRes.Status != http.StatusCreated {
		t.Fatalf("create node: %d %s", nodeRes.Status, string(nodeRes.Body))
	}
	var nodeBody struct {
		Node struct {
			ID string `json:"id"`
		} `json:"node"`
	}
	nodeRes.Decode(t, &nodeBody)
	if nodeBody.Node.ID != prov {
		t.Fatalf("node id = %q, want %q", nodeBody.Node.ID, prov)
	}

	// 2. The key, with the authType a compatible node stores.
	connRes := env.Post(t, "/api/connections", map[string]any{
		"provider": prov, "authType": "compatible", "apiKey": "sk-shared", "name": "Share Me Key",
	})
	if connRes.Status != http.StatusOK && connRes.Status != http.StatusCreated {
		t.Fatalf("create connection: %d %s", connRes.Status, string(connRes.Body))
	}
	var connBody struct {
		ID string `json:"id"`
	}
	connRes.Decode(t, &connBody)
	if connBody.ID == "" {
		t.Fatalf("create connection returned no id: %s", string(connRes.Body))
	}

	// 3. The catalogue, fetched through the connection id.
	modelsRes := env.Get(t, "/api/providers/"+connBody.ID+"/models")
	if modelsRes.Status != http.StatusOK {
		t.Fatalf("list models: %d %s", modelsRes.Status, string(modelsRes.Body))
	}

	// 4. A chat request actually reaches the shared upstream through the alias.
	chat := env.Post(t, "/v1/chat/completions", ChatBody(prov+"/m-1", false))
	if chat.Status != http.StatusOK {
		t.Fatalf("chat through the shared node: %d %s", chat.Status, string(chat.Body))
	}
	if got := up.Last(t).Path; got != "/v1/chat/completions" {
		t.Fatalf("upstream path = %q, want /v1/chat/completions", got)
	}
}
