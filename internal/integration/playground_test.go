//go:build integration

package integration

import (
	"net/http"
	"testing"
	"time"

	"9router/proxy/internal/auth"
)

// The playground is a dashboard surface that dispatches through the engine's
// own chat lane, so its contract has two halves: the dashboard gate admits a
// session (and a client key, like every dashboard route), and the turn itself
// behaves exactly like a public-lane call — the upstream sees the bare model,
// the connection's credential, and the reply comes back as Chat Completions.
func TestPlaygroundChatForwardsThroughEngine(t *testing.T) {
	env, upstream := newProviderEnv(t)

	res := env.Post(t, "/api/playground/chat", map[string]any{
		"model": "deepseek/deepseek-chat",
		"messages": []map[string]any{
			{"role": "user", "content": "Summarize the Go memory model."},
		},
		"stream": false,
	})
	if res.Status != http.StatusOK {
		t.Fatalf("POST /api/playground/chat = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}

	var completion struct {
		Object  string `json:"object"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	res.Decode(t, &completion)
	if len(completion.Choices) != 1 || completion.Choices[0].Message.Content != "upstream reply" {
		t.Fatalf("playground reply = %+v, want the upstream content (body: %s)", completion, truncate(res.Body))
	}

	sent := upstream.Last(t)
	if got := sent.Model(t); got != "deepseek-chat" {
		t.Errorf("upstream model = %q, want \"deepseek-chat\"", got)
	}
	if got := sent.Header.Get("Authorization"); got != "Bearer sk-upstream" {
		t.Errorf("upstream Authorization = %q, want the connection credential", got)
	}
}

// A keyId scopes the turn: the synthetic request carries the key on its
// context, so the usage ledger names it as the client — the same attribution
// the public lane records for a keyed caller.
func TestPlaygroundChatAttributesUsageToKey(t *testing.T) {
	env, upstream := newProviderEnv(t)

	res := env.Post(t, "/api/playground/chat", map[string]any{
		"model":  "deepseek/deepseek-chat",
		"stream": false,
		"messages": []map[string]any{
			{"role": "user", "content": "Summarize the Go memory model."},
		},
		"keyId": "integration-key",
	})
	if res.Status != http.StatusOK {
		t.Fatalf("POST /api/playground/chat with keyId = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	if upstream.Count() != 1 {
		t.Fatalf("upstream received %d requests, want 1", upstream.Count())
	}

	var clientKey string
	if err := env.Repo.RawDB().QueryRow(
		`SELECT COALESCE(clientKey, '') FROM usageHistory WHERE id = (SELECT MAX(id) FROM usageHistory)`,
	).Scan(&clientKey); err != nil {
		t.Fatalf("read latest usage row clientKey: %v", err)
	}
	if clientKey == "" {
		t.Errorf("usage row clientKey is empty, want the picked key's mask")
	}
}

// A key's model allowlist is enforced by the same gate the public lane uses:
// a playground turn may not reach a model the key itself is denied.
func TestPlaygroundChatEnforcesKeyAllowlist(t *testing.T) {
	env, _ := newProviderEnv(t)

	body := map[string]any{"models": []string{"openai/gpt-5-mini"}}
	if res := env.Put(t, "/api/keys/integration-key/models", body); res.Status != http.StatusOK {
		t.Fatalf("restrict key models = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}

	res := env.Post(t, "/api/playground/chat", map[string]any{
		"model":  "deepseek/deepseek-chat",
		"stream": false,
		"messages": []map[string]any{
			{"role": "user", "content": "Summarize the Go memory model."},
		},
		"keyId": "integration-key",
	})
	if res.Status != http.StatusForbidden {
		t.Fatalf("POST /api/playground/chat with denied model = %d, want 403 (body: %s)", res.Status, truncate(res.Body))
	}
}

// A keyId that names no live row is a caller mistake and must read as one —
// not as a silent downgrade to the global scope, which would let a typo'd id
// spend a turn without any key's policy applying.
func TestPlaygroundChatRejectsUnknownKey(t *testing.T) {
	env, _ := newProviderEnv(t)

	res := env.Post(t, "/api/playground/chat", map[string]any{
		"model":  "deepseek/deepseek-chat",
		"stream": false,
		"messages": []map[string]any{
			{"role": "user", "content": "Summarize the Go memory model."},
		},
		"keyId": "no-such-key",
	})
	if res.Status != http.StatusBadRequest {
		t.Fatalf("POST /api/playground/chat with unknown key = %d, want 400 (body: %s)", res.Status, truncate(res.Body))
	}
}

// The dashboard gate still applies: a call with neither session, CLI token nor
// client key is refused, like every other dashboard route.
func TestPlaygroundChatRequiresDashboardAuth(t *testing.T) {
	env, _ := newProviderEnv(t)
	if err := env.Repo.UpdateSettingsRaw(map[string]any{"requireLogin": true}); err != nil {
		t.Fatalf("set requireLogin: %v", err)
	}

	res := env.Post(t, "/api/playground/chat", map[string]any{
		"model":  "deepseek/deepseek-chat",
		"stream": false,
		"messages": []map[string]any{
			{"role": "user", "content": "Summarize the Go memory model."},
		},
	}, WithoutAPIKey())
	if res.Status != http.StatusUnauthorized {
		t.Fatalf("anonymous POST /api/playground/chat = %d, want 401 (body: %s)", res.Status, truncate(res.Body))
	}

	session, err := auth.Sign(auth.Secret(), time.Now())
	if err != nil {
		t.Fatalf("sign session: %v", err)
	}
	res = env.Post(t, "/api/playground/chat", map[string]any{
		"model":  "deepseek/deepseek-chat",
		"stream": false,
		"messages": []map[string]any{
			{"role": "user", "content": "Summarize the Go memory model."},
		},
	}, WithAPIKey(""), WithHeader("Cookie", auth.CookieName+"="+session))
	if res.Status != http.StatusOK {
		t.Fatalf("session POST /api/playground/chat = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
}
