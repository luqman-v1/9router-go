package dashboard

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/db"
)

// The three behaviours this file pins are the review's blocking findings on
// #520: a door that could WIDEN a key, a read failure that read as "no
// restriction", and an `access` field that understated a plan-bound key.

// TestKeyAccessWriteRefusesPatternSyntax closes the widening.
//
// Go's store is a pattern language (#365): `["*"]` matches everything and
// `["-gpt-4o"]` is deny-only, which means "allow everything except gpt-4o".
// Upstream's `{restricted, allow}` shape has no wildcard and no deny mode, so
// storing either through this door would answer "restricted" for a key the
// gate treats as open. Both must be a 400, and the key must keep the policy it
// already had.
func TestKeyAccessWriteRefusesPatternSyntax(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	if err := repo.UpdateSettingsRaw(map[string]any{"requireLogin": false}); err != nil {
		t.Fatalf("disable requireLogin: %v", err)
	}
	router := setupTestRouter(repo)

	id, _ := createTestKey(t, router, "pattern-syntax")

	seed := []string{"gpt-4o"}

	putKeyAccess(t, router, id, map[string]any{"restricted": true, "allow": seed})

	for _, tc := range []struct {
		name  string
		allow []any
	}{
		{"wildcard", []any{"*"}},
		{"glob", []any{"claude/*"}},
		{"infix glob", []any{"*claude*"}},
		{"deny entry", []any{"-gpt-4o"}},
		{"bang deny entry", []any{"!gpt-4o"}},
		{"exact id mixed with a wildcard", []any{"gpt-4o", "*"}},
	} {
		rec := putKeyAccess(t, router, id, map[string]any{"restricted": true, "allow": tc.allow})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: PUT access = %d, want 400 (body: %s)", tc.name, rec.Code, rec.Body.String())
		}
	}

	// The seed survives every refusal — a rejected body must not have widened
	// the key, which is the failure mode the whole check exists to prevent.
	if got := keyAccessOf(t, router, id); !got.Restricted || !sameSet(got.Allow, seed) {
		t.Errorf("access after refused writes = %+v, want restricted with %v", got, seed)
	}
}

// TestKeyAccessReadFailureIsNotUnrestricted closes the fail-open read.
//
// The gate fails closed when the allowlist cannot be read (see
// allowedModelPatterns in internal/handlers/chat/access.go). The listing has to
// answer the same way: reporting the key as unrestricted would have the
// dashboard advertise a policy the request path is not enforcing. The stored
// rows cannot be read here, so the only safe report is "restricted, allows
// nothing" — the same shape upstream uses and the direction its own comment
// promises ("it can never turn a restricted key into an unrestricted one").
func TestKeyAccessReadFailureIsNotUnrestricted(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	if err := repo.UpdateSettingsRaw(map[string]any{"requireLogin": false}); err != nil {
		t.Fatalf("disable requireLogin: %v", err)
	}
	router := setupTestRouter(repo)

	id, _ := createTestKey(t, router, "read-failure")
	putKeyAccess(t, router, id, map[string]any{"restricted": true, "allow": []any{"gpt-4o"}})

	// Dropping the table is the reachable form of "the read fails": it is the
	// same error the gate fails closed on, and it leaves the key row intact so
	// the listing still has something to report.
	if _, err := repo.RawDB().Exec(`DROP TABLE api_key_model_access`); err != nil {
		t.Fatalf("drop the access table: %v", err)
	}

	row := listKeys(t, router)
	access, ok := row["access"].(map[string]any)
	if !ok {
		t.Fatalf("access missing or not an object: %v", row["access"])
	}
	if restricted, _ := access["restricted"].(bool); !restricted {
		t.Errorf("access.restricted = false after a failed allowlist read; the dashboard is advertising an open key")
	}
	allow, _ := access["allow"].([]any)
	if len(allow) != 0 {
		t.Errorf("access.allow = %v after a failed read, want empty", allow)
	}
}

// TestKeyAccessReportsThePlanUnion closes the understatement.
//
// The gate unions a key's own rows with its plan's (withPlanPatterns), so a
// write through this endpoint cannot narrow a plan-bound key. Reporting only
// the own rows would tell an operator the key may call `gpt-4o` while the gate
// still allows everything the plan names.
func TestKeyAccessReportsThePlanUnion(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	if err := repo.UpdateSettingsRaw(map[string]any{"requireLogin": false}); err != nil {
		t.Fatalf("disable requireLogin: %v", err)
	}
	router := setupTestRouter(repo)

	plan := doJSON(t, router, http.MethodPost, "/api/plans", planBody(map[string]any{
		"name":          "union-plan",
		"allowedModels": []string{"cc/claude-opus-5"},
	}))
	if plan.Code != http.StatusOK {
		t.Fatalf("create plan = %d: %s", plan.Code, plan.Body.String())
	}
	planID, _ := decodePlan(t, plan)["id"].(string)
	if planID == "" {
		t.Fatal("create plan returned no id")
	}

	id, _ := createTestKey(t, router, "plan-union")
	if rec := doJSON(t, router, http.MethodPut, "/api/keys/"+id, planBody(map[string]any{"planId": planID})); rec.Code != http.StatusOK {
		t.Fatalf("assign plan = %d: %s", rec.Code, rec.Body.String())
	}
	putKeyAccess(t, router, id, map[string]any{"restricted": true, "allow": []any{"gpt-4o"}})

	// The per-key endpoint reports the union.
	models := getKeyModels(t, router, id)
	if !models.Access.Restricted {
		t.Errorf("GET /api/keys/{id}/models access.restricted = false")
	}
	if !sameSet(models.Access.Allow, []string{"gpt-4o", "cc/claude-opus-5"}) {
		t.Errorf("access.allow = %v, want the key rows unioned with the plan's", models.Access.Allow)
	}
	// The flat `models` field stays the key's OWN rows: it is the field this
	// endpoint writes, so promoting the plan's rows into it would let a
	// read-modify-write copy them onto the key.
	if !sameSet(models.Models, []string{"gpt-4o"}) {
		t.Errorf("/models = %v, want only the key's own rows", models.Models)
	}

	// And the list reports the same union, so the two surfaces agree.
	row := listKeys(t, router)
	access, ok := row["access"].(map[string]any)
	if !ok {
		t.Fatalf("list access missing or not an object: %v", row["access"])
	}
	allow, _ := access["allow"].([]any)
	got := make([]string, 0, len(allow))
	for _, v := range allow {
		if s, ok := v.(string); ok {
			got = append(got, s)
		}
	}
	if !sameSet(got, []string{"gpt-4o", "cc/claude-opus-5"}) {
		t.Errorf("list access.allow = %v, want the key rows unioned with the plan's", got)
	}
}

// TestKeyAccessPatternFromThePlanIsNotEmpty closes the last place the field
// could understate: a PLAN carrying a pattern. The union then holds the key's
// plain entries beside the plan's glob, and publishing only the plain ones
// would say "may call gpt-4o" while the gate allows everything the glob names.
// The whole set is then outside the Node shape, so the answer is restricted
// with nothing — never restricted with a subset.
func TestKeyAccessPatternFromThePlanIsNotEmpty(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	if err := repo.UpdateSettingsRaw(map[string]any{"requireLogin": false}); err != nil {
		t.Fatalf("disable requireLogin: %v", err)
	}
	router := setupTestRouter(repo)

	plan := doJSON(t, router, http.MethodPost, "/api/plans", planBody(map[string]any{
		"name": "glob-plan",
		// The plan endpoint keeps its pattern semantics, so a glob is accepted
		// there — and it is exactly the shape that makes the union wider than
		// its plain entries suggest.
		"allowedModels": []string{"*"},
	}))
	if plan.Code != http.StatusOK {
		t.Fatalf("create plan = %d: %s", plan.Code, plan.Body.String())
	}
	planID, _ := decodePlan(t, plan)["id"].(string)

	id, _ := createTestKey(t, router, "plan-glob")
	if rec := doJSON(t, router, http.MethodPut, "/api/keys/"+id, planBody(map[string]any{"planId": planID})); rec.Code != http.StatusOK {
		t.Fatalf("assign plan = %d: %s", rec.Code, rec.Body.String())
	}
	putKeyAccess(t, router, id, map[string]any{"restricted": true, "allow": []any{"gpt-4o"}})

	for _, surface := range []string{"per-key", "list"} {
		var got keyAccessShape
		if surface == "per-key" {
			got = keyAccessOf(t, router, id)
		} else {
			access, ok := listKeys(t, router)["access"].(map[string]any)
			if !ok {
				t.Fatalf("list access missing or not an object")
			}
			allow, _ := access["allow"].([]any)
			got.Restricted, _ = access["restricted"].(bool)
			for _, v := range allow {
				if s, ok := v.(string); ok {
					got.Allow = append(got.Allow, s)
				}
			}
		}
		if !got.Restricted {
			t.Errorf("%s: access.restricted = false for a key on a glob plan", surface)
		}
		if len(got.Allow) != 0 {
			t.Errorf("%s: access.allow = %v, want empty — the plan's glob makes the set wider than its plain entries", surface, got.Allow)
		}
	}
}

// putKeyAccess is this file's own request shape: it must not depend on the
// handler's struct, or a rename there would keep these tests green.
func putKeyAccess(t *testing.T, router http.Handler, id string, access map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	return doJSON(t, router, http.MethodPut, "/api/keys/"+id, planBody(map[string]any{"access": access}))
}

type keyAccessShape struct {
	Restricted bool     `json:"restricted"`
	Allow      []string `json:"allow"`
}

type keyModelsShape struct {
	Models []string
	Access keyAccessShape
}

func keyAccessOf(t *testing.T, router http.Handler, id string) keyAccessShape {
	t.Helper()
	return getKeyModels(t, router, id).Access
}

func getKeyModels(t *testing.T, router http.Handler, id string) keyModelsShape {
	t.Helper()
	rec := doJSON(t, router, http.MethodGet, "/api/keys/"+id+"/models", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/keys/%s/models = %d: %s", id, rec.Code, rec.Body.String())
	}
	var raw struct {
		Models []string       `json:"models"`
		Access keyAccessShape `json:"access"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("unmarshal models response: %v", err)
	}
	return keyModelsShape{Models: raw.Models, Access: raw.Access}
}

// sameSet is order-insensitive: the union's order is stable but it is not part
// of the contract these tests pin, and asserting it would make a reordering
// look like a policy change.
func sameSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := make(map[string]bool, len(got))
	for _, v := range got {
		seen[v] = true
	}
	for _, w := range want {
		if !seen[w] {
			return false
		}
	}
	return true
}

var _ = db.SentinelDenyAll // the sentinel is part of the shape under test
