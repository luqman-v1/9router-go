package dashboard

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

// TestComboModelIDCoercion pins the two legacy object shapes the TUI sends and
// the strings it sends itself, plus every shape that must be refused. The
// object entries are the point of the change: persisted verbatim they land in a
// column the reader only unmarshals as []string, which breaks the Combos view.
func TestComboModelIDCoercion(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  []string
	}{
		{
			name:  "fullModel wins over provider and model",
			input: []any{map[string]any{"fullModel": "ag/gemini-3.8-flash", "provider": "ag", "model": "other"}},
			want:  []string{"ag/gemini-3.8-flash"},
		},
		{
			name:  "provider and model are joined",
			input: []any{map[string]any{"provider": "cx", "model": "gpt-6-sol"}},
			want:  []string{"cx/gpt-6-sol"},
		},
		{
			name:  "strings and objects mix in one list",
			input: []any{map[string]any{"fullModel": "ag/gemini-3.8-flash"}, "ag/claude-sonnet-4-6"},
			want:  []string{"ag/gemini-3.8-flash", "ag/claude-sonnet-4-6"},
		},
		{
			name:  "typed string slice",
			input: []string{"openai/gpt-4o-mini"},
			want:  []string{"openai/gpt-4o-mini"},
		},
		{
			name:  "json array string, the form the client type allows",
			input: `["openai/gpt-4o-mini","cx/gpt-6-sol"]`,
			want:  []string{"openai/gpt-4o-mini", "cx/gpt-6-sol"},
		},
		{
			name:  "empty list",
			input: []any{},
			want:  []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := normalizeComboModelIDs(tc.input)
			if !ok {
				t.Fatalf("normalizeComboModelIDs(%#v) refused a valid list", tc.input)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("normalizeComboModelIDs(%#v) = %#v, want %#v", tc.input, got, tc.want)
			}
		})
	}
}

// TestComboModelIDRejection covers every entry that names no model: those are
// refused at the boundary rather than persisted, because a stored object row is
// unreadable by comboModels and takes the whole Combos view with it.
func TestComboModelIDRejection(t *testing.T) {
	tests := []struct {
		name  string
		input any
	}{
		{"object with no id", []any{map[string]any{"name": "unroutable"}}},
		{"blank string among valid ids", []any{"", "cx/gpt-6-sol"}},
		{"blank object id", []any{map[string]any{"fullModel": "   "}}},
		{"provider without model", []any{map[string]any{"provider": "cx"}}},
		{"model without provider", []any{map[string]any{"model": "gpt-6-sol"}}},
		{"non-string id fields", []any{map[string]any{"provider": 1, "model": 2}}},
		{"numeric entry", []any{42}},
		{"null entry", []any{nil}},
		{"nested array entry", []any{[]any{"cx/gpt-6-sol"}}},
		{"not a list", map[string]any{"provider": "cx"}},
		{"empty string", ""},
		{"unparseable json string", "["},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := normalizeComboModelIDs(tc.input)
			if ok {
				t.Errorf("normalizeComboModelIDs(%#v) = %#v, want refusal", tc.input, got)
			}
		})
	}
}

// TestComboCreateNormalizesLegacyModelObjects drives the real route: the stored
// column must hold plain id strings, and the reader must be able to decode it.
func TestComboCreateNormalizesLegacyModelObjects(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	rec := postJSON(t, router, "/api/combos", map[string]any{
		"id":   "legacy-combo",
		"name": "legacy-models",
		"models": []any{
			map[string]any{"fullModel": "ag/gemini-3.8-flash", "provider": "ag", "model": "other"},
			map[string]any{"provider": "cx", "model": "gpt-6-sol"},
			"ag/claude-sonnet-4-6",
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	stored, err := repo.GetComboById("legacy-combo")
	if err != nil || stored == nil {
		t.Fatalf("GetComboById: %v (%+v)", err, stored)
	}
	ids, err := comboModels(stored.Models)
	if err != nil {
		t.Fatalf("stored models unreadable by the reader: %v (%s)", err, stored.Models)
	}
	want := []string{"ag/gemini-3.8-flash", "cx/gpt-6-sol", "ag/claude-sonnet-4-6"}
	if !slices.Equal(ids, want) {
		t.Errorf("stored models = %#v (%s), want %#v", ids, stored.Models, want)
	}
}

// TestComboWriteRejectsMalformedModels checks both write paths answer 400 in
// the package's standard error envelope and write nothing to the column.
func TestComboWriteRejectsMalformedModels(t *testing.T) {
	const malformed = `[{"name":"unroutable"}]`

	tests := []struct {
		name   string
		method string
		path   string
		body   map[string]any
	}{
		{
			name:   "create with an object entry that names no model",
			method: http.MethodPost,
			path:   "/api/combos",
			body:   map[string]any{"id": "bad-create", "name": "bad-create", "models": []any{map[string]any{"name": "unroutable"}}},
		},
		{
			name:   "create with a blank id",
			method: http.MethodPost,
			path:   "/api/combos",
			body:   map[string]any{"id": "bad-create2", "name": "bad-create2", "models": []any{"", "cx/gpt-6-sol"}},
		},
		{
			name:   "create with a non-list models field",
			method: http.MethodPost,
			path:   "/api/combos",
			body:   map[string]any{"id": "bad-create3", "name": "bad-create3", "models": map[string]any{"provider": "cx"}},
		},
		{
			name:   "update with an object entry that names no model",
			method: http.MethodPut,
			path:   "/api/combos/legacy-combo",
			body:   map[string]any{"models": []any{map[string]any{"name": "unroutable"}}},
		},
		{
			name:   "update with a raw malformed column value",
			method: http.MethodPut,
			path:   "/api/combos/legacy-combo",
			body:   map[string]any{"models": malformed},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo, cleanup := setupTestDB(t)
			defer cleanup()
			router := setupTestRouter(repo)
			// Seed the combo the update cases write to.
			if err := repo.CreateCombo("legacy-combo", "legacy", "", `["ag/claude-sonnet-4-6"]`, "fallback"); err != nil {
				t.Fatalf("seed combo: %v", err)
			}

			var rec *httptest.ResponseRecorder
			if tc.method == http.MethodPost {
				rec = postJSON(t, router, tc.path, tc.body)
			} else {
				rec = putJSON(t, router, tc.path, tc.body)
			}
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
			}
			var payload map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("unmarshal error body: %v", err)
			}
			if msg := errorMessage(t, payload); msg != "models must contain valid model IDs" {
				t.Errorf("error message = %q", msg)
			}

			// A refused write must leave nothing behind.
			id, _ := tc.body["id"].(string)
			if id != "" {
				if stored, _ := repo.GetComboById(id); stored != nil {
					t.Errorf("refused create persisted combo %s", id)
				}
				return
			}
			stored, err := repo.GetComboById("legacy-combo")
			if err != nil || stored == nil {
				t.Fatalf("GetComboById: %v (%+v)", err, stored)
			}
			if stored.Models != `["ag/claude-sonnet-4-6"]` {
				t.Errorf("refused update still wrote models: %s", stored.Models)
			}
		})
	}
}

// TestComboUpdateOmitsModelsKeepsStoredSet: an update that leaves models out is
// not a write to the field, so the normalizer must not touch it.
func TestComboUpdateOmitsModelsKeepsStoredSet(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)
	if err := repo.CreateCombo("keep-models", "keep", "", `["ag/claude-sonnet-4-6"]`, "fallback"); err != nil {
		t.Fatalf("seed combo: %v", err)
	}

	rec := putJSON(t, router, "/api/combos/keep-models", map[string]any{"strategy": "round-robin"})
	if rec.Code != http.StatusOK {
		t.Fatalf("strategy-only update expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	stored, err := repo.GetComboById("keep-models")
	if err != nil || stored == nil {
		t.Fatalf("GetComboById: %v (%+v)", err, stored)
	}
	if stored.Models != `["ag/claude-sonnet-4-6"]` {
		t.Errorf("models = %s, want the stored set untouched", stored.Models)
	}
}
