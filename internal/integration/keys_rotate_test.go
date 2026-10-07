//go:build integration

package integration

import (
	"strings"
	"testing"
)

// createKey mints a client key and returns its id and the plaintext shown once.
func createKey(t *testing.T, env *Env, name string) (id, plaintext string) {
	t.Helper()
	res := env.Post(t, "/api/keys", map[string]any{"name": name})
	if res.Status != 200 {
		t.Fatalf("create key: status %d: %s", res.Status, truncate(res.Body))
	}
	var out struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	res.Decode(t, &out)
	if out.Key == "" {
		t.Fatal("create returned no plaintext key")
	}
	return out.ID, out.Key
}

// TestRotateApiKeyReplacesSecret is the path an operator has after F-6 removed
// "reveal". A leaked key must be replaceable without deleting the row, because
// deleting it also takes the key's policy, usage history, and allowlist with it.
func TestRotateApiKeyReplacesSecret(t *testing.T) {
	env := newEnv(t)
	id, oldKey := createKey(t, env, "rotate me")

	res := env.Post(t, "/api/keys/"+id+"/rotate", nil)
	if res.Status != 200 {
		t.Fatalf("rotate: status %d: %s", res.Status, truncate(res.Body))
	}
	var out struct {
		Key string `json:"key"`
	}
	res.Decode(t, &out)
	if out.Key == "" {
		t.Fatal("rotate returned no plaintext key")
	}
	if out.Key == oldKey {
		t.Fatal("rotate returned the same secret")
	}

	// The new key works.
	if got := env.Get(t, "/v1/models", WithAPIKey(out.Key)); got.Status != 200 {
		t.Errorf("new key status = %d, want 200", got.Status)
	}
	// The old one does not, immediately — not after the cache TTL. An operator
	// rotating a leaked key is counting on it dying now.
	if got := env.Get(t, "/v1/models", WithAPIKey(oldKey)); got.Status == 200 {
		t.Errorf("rotated-out key still authenticates (status %d)", got.Status)
	}
}

// TestRotateApiKeyKeepsPolicy pins what rotation must NOT destroy: the row's
// governance settings. Rotating a secret is not reissuing a key.
func TestRotateApiKeyKeepsPolicy(t *testing.T) {
	env := newEnv(t)
	id, _ := createKey(t, env, "policy survives rotation")

	if put := env.Put(t, "/api/keys/"+id, map[string]any{"rateLimitRpm": 7}); put.Status != 200 {
		t.Fatalf("set policy: status %d: %s", put.Status, truncate(put.Body))
	}

	if res := env.Post(t, "/api/keys/"+id+"/rotate", nil); res.Status != 200 {
		t.Fatalf("rotate: status %d: %s", res.Status, truncate(res.Body))
	}

	res := env.Get(t, "/api/keys")
	if res.Status != 200 {
		t.Fatalf("list keys: %d", res.Status)
	}
	if !strings.Contains(string(res.Body), `"rateLimitRpm":7`) {
		t.Errorf("rotation cleared the key's policy: %s", truncate(res.Body))
	}
}

// TestRotateUnknownKeyIsNotFound keeps the handler from minting a secret for a
// row that does not exist.
func TestRotateUnknownKeyIsNotFound(t *testing.T) {
	env := newEnv(t)

	res := env.Post(t, "/api/keys/does-not-exist/rotate", nil)
	if res.Status != 404 {
		t.Errorf("status = %d, want 404: %s", res.Status, truncate(res.Body))
	}
}
