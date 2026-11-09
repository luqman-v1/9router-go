//go:build integration

package integration

import (
	"net/http"
	"testing"

	"github.com/samber/lo"
)

// accessObject is the `access` field the Node dashboard reads and writes.
type accessObject struct {
	Restricted bool     `json:"restricted"`
	Allow      []string `json:"allow"`
}

type allowlistWithAccess struct {
	ID     string       `json:"id"`
	Models []string     `json:"models"`
	Access accessObject `json:"access"`
}

type keyRowWithAccess struct {
	ID     string       `json:"id"`
	Access accessObject `json:"access"`
}

// newKeyForAccess mints a key and returns its id, reusing the harness helper
// that already knows the POST /api/keys contract.
func newKeyForAccess(t *testing.T, env *Env, name string) string {
	t.Helper()
	id, _ := createKey(t, env, name)
	return id
}

func accessOfKey(t *testing.T, env *Env, id string) accessObject {
	t.Helper()
	res := env.Get(t, "/api/keys")
	if res.Status != http.StatusOK {
		t.Fatalf("GET /api/keys = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	var rows []keyRowWithAccess
	res.Decode(t, &rows)
	row, ok := lo.Find(rows, func(r keyRowWithAccess) bool { return r.ID == id })
	if !ok {
		t.Fatalf("api key %s is not in GET /api/keys", id)
	}
	return row.Access
}

func modelsOfKey(t *testing.T, env *Env, id string) allowlistWithAccess {
	t.Helper()
	res := env.Get(t, "/api/keys/"+id+"/models")
	if res.Status != http.StatusOK {
		t.Fatalf("GET /api/keys/%s/models = %d, want 200 (body: %s)", id, res.Status, truncate(res.Body))
	}
	var out allowlistWithAccess
	res.Decode(t, &out)
	return out
}

func putAccess(t *testing.T, env *Env, id string, access map[string]any) {
	t.Helper()
	res := env.Put(t, "/api/keys/"+id, map[string]any{"access": access})
	if res.Status != http.StatusOK {
		t.Fatalf("PUT access = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
}

// putAccessExpect is putAccess for the cases that must be REFUSED: it returns
// the status so the caller can pin the refusal instead of failing the test.
func putAccessExpect(t *testing.T, env *Env, id string, access map[string]any) int {
	t.Helper()
	return env.Put(t, "/api/keys/"+id, map[string]any{"access": access}).Status
}

func samePatterns(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for _, w := range want {
		if !lo.Contains(got, w) {
			return false
		}
	}
	return true
}

// (a) writes the `access` form and reads the flat /models list back. Both must
// describe the same restriction — the point of the alias is that a client
// speaking either shape reaches one policy, not two.
func TestKeyAccessShapeIsTheSamePolicyAsModels(t *testing.T) {
	env := newEnv(t)
	id := newKeyForAccess(t, env, "access-a")

	want := []string{"cc/claude-opus-5", "gpt-4o"}
	putAccess(t, env, id, map[string]any{"restricted": true, "allow": want})

	got := modelsOfKey(t, env, id)
	if !samePatterns(got.Models, want) {
		t.Errorf("GET /models after writing access = %v, want %v", got.Models, want)
	}
	if !got.Access.Restricted {
		t.Errorf("access.restricted = false after writing a restricted list")
	}
	if !samePatterns(got.Access.Allow, want) {
		t.Errorf("access.allow = %v, want %v", got.Access.Allow, want)
	}
}

// (b) writes the flat `models` form and reads `access` back — the reverse
// direction, which is what the Node dashboard needs from a Go-written key.
//
// The two shapes are not the same language. The flat endpoint keeps its pattern
// semantics (#365), so a glob round-trips through `models` untouched; `access`
// carries only what upstream's shape can express, which for a glob is nothing.
// The key is still reported RESTRICTED — the gate does constrain it — but with
// an empty list rather than a pattern upstream would misread. Reporting it as
// unrestricted would be the fail-open direction the shape must never have.
func TestKeyModelsShapeIsTheSamePolicyAsAccess(t *testing.T) {
	env := newEnv(t)
	id := newKeyForAccess(t, env, "access-b")

	// Exact identities first: these round-trip through both shapes.
	exact := []string{"claude/claude-opus-5", "openai/gpt-4o"}
	res := env.Put(t, "/api/keys/"+id+"/models", map[string]any{"models": exact})
	if res.Status != http.StatusOK {
		t.Fatalf("PUT /models = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}

	got := accessOfKey(t, env, id)
	if !got.Restricted {
		t.Fatalf("GET /api/keys access.restricted = false after writing an allowlist")
	}
	if !samePatterns(got.Allow, exact) {
		t.Errorf("GET /api/keys access.allow = %v, want %v", got.Allow, exact)
	}

	// Patterns stay in the flat endpoint's own vocabulary and are reported as a
	// restriction with no Node-expressible entries — never as open.
	patterns := []string{"claude/*", "oc/*"}
	res = env.Put(t, "/api/keys/"+id+"/models", map[string]any{"models": patterns})
	if res.Status != http.StatusOK {
		t.Fatalf("PUT /models (patterns) = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}

	got = accessOfKey(t, env, id)
	if !got.Restricted {
		t.Fatalf("access.restricted = false for a pattern allowlist; a constrained key was reported open")
	}
	if len(got.Allow) != 0 {
		t.Errorf("access.allow = %v for a pattern-only allowlist, want empty", got.Allow)
	}
	// The patterns themselves are not lost: the flat field still carries them.
	if stored := modelsOfKey(t, env, id); !samePatterns(stored.Models, patterns) {
		t.Errorf("/models = %v, want the stored patterns %v", stored.Models, patterns)
	}
}

// (c) restricted with an empty list is deny-all, not "no restriction". The
// stored form is the deny-all sentinel so the chat gate cannot read the empty
// list as allow-everything.
func TestKeyAccessRestrictedEmptyDeniesEverything(t *testing.T) {
	env := newEnv(t)
	id := newKeyForAccess(t, env, "access-c")

	putAccess(t, env, id, map[string]any{"restricted": true, "allow": []string{}})

	got := accessOfKey(t, env, id)
	if !got.Restricted {
		t.Fatalf("access.restricted = false for a restricted key with an empty allow list")
	}
	if len(got.Allow) != 0 {
		t.Errorf("access.allow = %v, want empty", got.Allow)
	}

	// The stored form must be the deny-all sentinel, not an empty table: an
	// empty table reads as unrestricted to the chat gate.
	models := modelsOfKey(t, env, id)
	if !samePatterns(models.Models, []string{"!*"}) {
		t.Errorf("/models = %v, want the deny-all sentinel [!*]", models.Models)
	}

	// And the unrestricted form must clear it again, or the key can never be
	// opened from the dashboard.
	putAccess(t, env, id, map[string]any{"restricted": false, "allow": []string{}})
	if after := accessOfKey(t, env, id); after.Restricted {
		t.Errorf("access.restricted = true after writing restricted:false")
	}
	if after := modelsOfKey(t, env, id); len(after.Models) != 0 {
		t.Errorf("/models = %v after clearing, want empty", after.Models)
	}
}

// (e) The `access` door must not WIDEN a key. Go's store is a pattern language
// (#365): `["*"]` matches everything and `["-gpt-4o"]` is deny-only, meaning
// "allow everything except gpt-4o". Upstream's shape has neither, so accepting
// them here would store a policy the dashboard calls `restricted` while the
// gate serves every model.
//
// The gate is the oracle, not the response body: a rejected write must leave
// the previous restriction in force, and the request path must still refuse a
// model the seed does not name.
func TestKeyAccessDoorCannotWidenAKey(t *testing.T) {
	env := newEnv(t)
	id, secret := createKey(t, env, "access-e")

	seed := []string{"gpt-4o"}
	putAccess(t, env, id, map[string]any{"restricted": true, "allow": seed})

	widening := []struct {
		name  string
		allow []any
	}{
		{"wildcard", []any{"*"}},
		{"glob", []any{"claude/*"}},
		{"deny-only", []any{"-gpt-4o"}},
		{"bang deny-only", []any{"!gpt-4o"}},
	}
	for _, tc := range widening {
		if got := putAccessExpect(t, env, id, map[string]any{"restricted": true, "allow": tc.allow}); got != http.StatusBadRequest {
			t.Errorf("%s: PUT access = %d, want 400 — the door accepted a pattern the Node shape cannot describe", tc.name, got)
		}
	}

	// The seeded restriction is still what the gate enforces. A 403 here is the
	// proof that none of the refused writes landed, and the request has to carry
	// THIS key: the suite's default credential belongs to a different key whose
	// policy this test never touched, so an unauthenticated-as-id request would
	// answer about the wrong row.
	res := env.Post(t, "/v1/embeddings", map[string]any{"model": "denied-model", "input": "hi"}, WithAPIKey(secret))
	if res.Status != http.StatusForbidden {
		t.Errorf("POST /v1/embeddings = %d after refused widening writes, want 403 (body: %s)", res.Status, truncate(res.Body))
	}
	if got := accessOfKey(t, env, id); !got.Restricted || !samePatterns(got.Allow, seed) {
		t.Errorf("access after refused writes = %+v, want restricted with %v", got, seed)
	}
}

// (f) `access` reports the EFFECTIVE policy — the key's own rows unioned with
// its plan's. The gate unions the two and can only widen, so reporting the own
// rows alone tells an operator the key may call one model while it may in fact
// call everything the plan names. A write through this endpoint cannot narrow
// a plan-bound key at all.
func TestKeyAccessReportsThePlanUnion(t *testing.T) {
	env := newEnv(t)
	id := newKeyForAccess(t, env, "access-f")

	res := env.Post(t, "/api/plans", map[string]any{
		"name":          "access-union-plan",
		"allowedModels": []string{"cc/claude-opus-5"},
	})
	if res.Status != http.StatusOK {
		t.Fatalf("POST /api/plans = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	var plan struct {
		ID string `json:"id"`
	}
	res.Decode(t, &plan)
	if plan.ID == "" {
		t.Fatal("plan create returned no id")
	}

	res = env.Put(t, "/api/keys/"+id, map[string]any{"planId": plan.ID})
	if res.Status != http.StatusOK {
		t.Fatalf("assign plan = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	putAccess(t, env, id, map[string]any{"restricted": true, "allow": []string{"gpt-4o"}})

	want := []string{"gpt-4o", "cc/claude-opus-5"}
	got := modelsOfKey(t, env, id)
	if !got.Access.Restricted {
		t.Errorf("access.restricted = false for a restricted key on a plan")
	}
	if !samePatterns(got.Access.Allow, want) {
		t.Errorf("access.allow = %v, want the key rows unioned with the plan's %v", got.Access.Allow, want)
	}
	// The flat `models` field stays the key's OWN rows: it is the field the
	// endpoint writes, so folding the plan's rows into it would let a
	// read-modify-write copy them onto the key.
	if !samePatterns(got.Models, []string{"gpt-4o"}) {
		t.Errorf("/models = %v, want only the key's own rows", got.Models)
	}
	// The list reports the same union, so the two surfaces agree.
	if listed := accessOfKey(t, env, id); !samePatterns(listed.Allow, want) {
		t.Errorf("GET /api/keys access.allow = %v, want %v", listed.Allow, want)
	}
}
func TestKeyAccessCorruptPayloadKeepsRestriction(t *testing.T) {
	env := newEnv(t)
	id := newKeyForAccess(t, env, "access-d")

	seed := []string{"cc/claude-opus-5"}
	putAccess(t, env, id, map[string]any{"restricted": true, "allow": seed})

	corrupt := []struct {
		name string
		body map[string]any
	}{
		{"allow is not an array", map[string]any{"restricted": true, "allow": "gpt-4o"}},
		{"allow holds a non-string", map[string]any{"restricted": true, "allow": []any{"ok", 42}}},
		{"restricted flag is missing", map[string]any{"allow": []any{"gpt-4o"}}},
		{"restricted is not a boolean", map[string]any{"restricted": "yes", "allow": []any{"gpt-4o"}}},
	}
	for _, tc := range corrupt {
		res := env.Put(t, "/api/keys/"+id, map[string]any{"access": tc.body})
		if res.Status != http.StatusBadRequest {
			t.Errorf("%s: PUT access = %d, want 400 (body: %s)", tc.name, res.Status, truncate(res.Body))
		}
	}

	got := accessOfKey(t, env, id)
	if !got.Restricted {
		t.Fatalf("access.restricted = false after four refused payloads; the key was widened")
	}
	if !samePatterns(got.Allow, seed) {
		t.Errorf("access.allow = %v, want the seeded %v", got.Allow, seed)
	}
	if models := modelsOfKey(t, env, id); !samePatterns(models.Models, seed) {
		t.Errorf("/models = %v, want the seeded %v", models.Models, seed)
	}
}
