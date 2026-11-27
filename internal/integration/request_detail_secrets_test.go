//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"
)

// TestRequestDetailsMaskPastedCredentials pins the storage hygiene of the
// Recent Requests tab. Its rows are written to disk, so a credential a client
// pasted into a prompt — a provider key, a forwarded Authorization header — is
// otherwise retained verbatim forever. The same request through the usage row
// is already covered; this is the payload row.
func TestRequestDetailsMaskPastedCredentials(t *testing.T) {
	env, _ := newProviderEnv(t)

	// Assembled from two literals so the file never carries a whole credential
	// shape: push protection reads a test fixture exactly as a live key.
	key := "sk-" + "proj1234567890abcdefghijklmnop"
	body := map[string]any{
		"model":  "deepseek/deepseek-chat",
		"stream": false,
		"messages": []map[string]any{
			{"role": "user", "content": "Deploy with this key: " + key + " and nothing else."},
		},
	}
	if res := env.Post(t, "/v1/chat/completions", body); res.Status != 200 {
		t.Fatalf("POST /v1/chat/completions = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}

	var data string
	err := env.Repo.RawDB().QueryRowContext(context.Background(),
		`SELECT data FROM requestDetails ORDER BY rowid DESC LIMIT 1`).Scan(&data)
	if err != nil {
		t.Fatalf("read the stored request detail: %v", err)
	}

	if strings.Contains(data, key) {
		t.Errorf("requestDetails stored the pasted credential in the clear: %s", truncate([]byte(data)))
	}
	if !strings.Contains(data, "[REDACTED]") {
		t.Errorf("requestDetails carries no mask where the credential was: %s", truncate([]byte(data)))
	}
	// The row is still a diagnostics row: everything that is not a credential
	// survives the mask.
	for _, want := range []string{"deepseek-chat", "Deploy with this key:", "conn-deepseek", "DeepSeek Integration"} {
		if !strings.Contains(data, want) {
			t.Errorf("requestDetails lost %q: %s", want, truncate([]byte(data)))
		}
	}
}

// TestFailedRequestDetailsMaskPastedCredentials covers the failure row, which
// is the one an operator re-reads most often.
func TestFailedRequestDetailsMaskPastedCredentials(t *testing.T) {
	env := newEnv(t)
	failing := env.NewUpstream(t, JSONResponder(500, `{"error":{"message":"upstream exploded","type":"server_error"}}`))
	env.AddConnection(t, "conn-fail", "deepseek", "DeepSeek Account", failing, "sk-upstream")

	key := "sk-" + "ant-api03-1234567890abcdefghijklmnop"
	body := map[string]any{
		"model":  "deepseek/deepseek-chat",
		"stream": false,
		"messages": []map[string]any{
			{"role": "user", "content": "Authorization: Bearer " + key},
		},
	}
	if res := env.Post(t, "/v1/chat/completions", body); res.Status != 500 {
		t.Fatalf("POST /v1/chat/completions = %d, want 500 (body: %s)", res.Status, truncate(res.Body))
	}

	var data string
	err := env.Repo.RawDB().QueryRowContext(context.Background(),
		`SELECT data FROM requestDetails ORDER BY rowid DESC LIMIT 1`).Scan(&data)
	if err != nil {
		t.Fatalf("read the stored failure detail: %v", err)
	}
	if strings.Contains(data, key) {
		t.Errorf("failure requestDetails stored the pasted credential in the clear: %s", truncate([]byte(data)))
	}
}
