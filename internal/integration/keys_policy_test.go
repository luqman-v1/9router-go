//go:build integration

package integration

import (
	"net/http"
	"testing"

	"github.com/samber/lo"
)

// policyRow is the governance slice of one GET /api/keys row: the columns the
// key policy modal edits and reads back.
type policyRow struct {
	ID            string `json:"id"`
	RateLimitRpm  int    `json:"rateLimitRpm"`
	RateLimitTpm  int    `json:"rateLimitTpm"`
	RateLimitConc int    `json:"rateLimitConcurrency"`
	ExpiresAt     string `json:"expiresAt"`
	Metadata      string `json:"metadata"`
}

// allowlist is the GET/PUT /api/keys/{id}/models payload.
type allowlist struct {
	ID     string   `json:"id"`
	Models []string `json:"models"`
}

func getAllowlist(t *testing.T, env *Env, id string) allowlist {
	t.Helper()
	res := env.Get(t, "/api/keys/"+id+"/models")
	if res.Status != http.StatusOK {
		t.Fatalf("GET allowlist = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	var out allowlist
	res.Decode(t, &out)
	return out
}

func policyOf(t *testing.T, env *Env, id string) policyRow {
	t.Helper()
	res := env.Get(t, "/api/keys")
	if res.Status != http.StatusOK {
		t.Fatalf("GET /api/keys = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	var rows []policyRow
	res.Decode(t, &rows)
	row, ok := lo.Find(rows, func(r policyRow) bool { return r.ID == id })
	if !ok {
		t.Fatalf("api key %s is not in the list %+v", id, rows)
	}
	return row
}

// TestKeyPolicyRoundTrip pins every field the key policy modal owns, read back
// through the production router after a fresh GET — the exact sequence the
// dashboard performs when the operator reopens the dialog.
//
// Issue #216 reported the modal returning 200 while the saved values were not
// there on reopen, so each field is asserted on a separate re-read rather than
// on the write response, which always reported success.
func TestKeyPolicyRoundTrip(t *testing.T) {
	env := newEnv(t)

	create := env.Post(t, "/api/keys", map[string]any{"name": "policy-roundtrip", "key": "sk-policy-216"})
	if create.Status != http.StatusOK {
		t.Fatalf("create key = %d, want 200 (body: %s)", create.Status, truncate(create.Body))
	}
	var made struct {
		ID string `json:"id"`
	}
	create.Decode(t, &made)

	metadata := `{"customerId":"acme","priceCents":5000}`
	put := env.Put(t, "/api/keys/"+made.ID, map[string]any{
		"rateLimitRpm":         30,
		"rateLimitTpm":         1500,
		"rateLimitConcurrency": 4,
		"expiresAt":            "2030-01-02T03:04:05Z",
		"metadata":             metadata,
	})
	if put.Status != http.StatusOK {
		t.Fatalf("PUT policy = %d, want 200 (body: %s)", put.Status, truncate(put.Body))
	}

	got := policyOf(t, env, made.ID)
	switch {
	case got.RateLimitRpm != 30:
		t.Errorf("rateLimitRpm = %d, want 30", got.RateLimitRpm)
	case got.RateLimitTpm != 1500:
		t.Errorf("rateLimitTpm = %d, want 1500", got.RateLimitTpm)
	case got.RateLimitConc != 4:
		t.Errorf("rateLimitConcurrency = %d, want 4", got.RateLimitConc)
	case got.ExpiresAt != "2030-01-02T03:04:05Z":
		t.Errorf("expiresAt = %q, want the stored instant", got.ExpiresAt)
	case got.Metadata != metadata:
		t.Errorf("metadata = %q, want %q", got.Metadata, metadata)
	}
}

// TestKeyModelAllowlistRoundTrip pins the per-key allowlist across a re-read,
// which is what the modal shows when it is reopened.
//
// An empty result means "allow every model", so a write that silently failed
// and a read that returned nothing are indistinguishable in the UI — the
// operator sees an unrestricted key either way. Asserting the exact set closes
// that gap.
func TestKeyModelAllowlistRoundTrip(t *testing.T) {
	env := newEnv(t)

	create := env.Post(t, "/api/keys", map[string]any{"name": "allowlist", "key": "sk-allowlist-216"})
	var made struct {
		ID string `json:"id"`
	}
	create.Decode(t, &made)

	set := env.Put(t, "/api/keys/"+made.ID+"/models", map[string]any{
		"models": []string{"e2e/allowed-*", "gpt-4o"},
	})
	if set.Status != http.StatusOK {
		t.Fatalf("PUT allowlist = %d, want 200 (body: %s)", set.Status, truncate(set.Body))
	}

	got := getAllowlist(t, env, made.ID)
	want := []string{"e2e/allowed-*", "gpt-4o"}
	if len(got.Models) != len(want) {
		t.Fatalf("allowlist = %v, want %v", got.Models, want)
	}
	for _, pattern := range want {
		if !lo.Contains(got.Models, pattern) {
			t.Errorf("allowlist %v is missing %q", got.Models, pattern)
		}
	}
}

// TestKeyPolicySavePreservesAllowlist pins the contract the modal's single
// "Save Policy" button relies on: the allowlist is keyed separately from the
// policy columns, so a save that rewrites one without touching the other must
// leave the untouched list intact. Re-seeding the list and re-reading it after
// a policy-only write is exactly what a reopen does.
func TestKeyPolicySavePreservesAllowlist(t *testing.T) {
	env := newEnv(t)

	create := env.Post(t, "/api/keys", map[string]any{"name": "preserve", "key": "sk-preserve-216"})
	var made struct {
		ID string `json:"id"`
	}
	create.Decode(t, &made)

	seed := env.Put(t, "/api/keys/"+made.ID+"/models", map[string]any{"models": []string{"claude/*"}})
	if seed.Status != http.StatusOK {
		t.Fatalf("seed allowlist = %d, want 200 (body: %s)", seed.Status, truncate(seed.Body))
	}

	// A policy-only write: the modal's other fields, with no models payload.
	if put := env.Put(t, "/api/keys/"+made.ID, map[string]any{
		"rateLimitRpm": 60,
		"metadata":     `{"customerId":"acme"}`,
	}); put.Status != http.StatusOK {
		t.Fatalf("PUT policy = %d, want 200 (body: %s)", put.Status, truncate(put.Body))
	}

	got := getAllowlist(t, env, made.ID)
	if !lo.Contains(got.Models, "claude/*") {
		t.Errorf("allowlist = %v, want it to still hold claude/* after a policy-only write", got.Models)
	}
	if rpm := policyOf(t, env, made.ID).RateLimitRpm; rpm != 60 {
		t.Errorf("rateLimitRpm = %d, want 60", rpm)
	}
}
