package dashboard

import (
	"bytes"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// A bare model string resolves against a model alias first and a combo name
// second, so those two spaces share one namespace and a name written into both
// makes one of them unreachable. That pair is what the guard refuses.
//
// A custom model id is not in that namespace: it is reached as
// "<node-prefix>/<id>" and published in the same prefixed form, so an id that
// matches an alias or a combo is a second, distinguishable address rather than a
// shadowed name. Refusing those writes broke "Import from /models" — a /models
// list carrying one already-aliased id failed the whole import — so custom model
// writes are no longer cross-checked at all.
//
// The guard is write-side only, so a setup that already collides keeps serving
// traffic; only new writes are refused.
func TestGuardNameCollision(t *testing.T) {
	const (
		nodeID     = "openai-compatible-chat-xai"
		comboName  = "combo-wombo"
		otherCombo = "free-tier"
		customID   = "agy"
		bareAlias  = "shared-name"
	)

	// seed builds a repo holding a combo "combo-wombo", a combo "free-tier",
	// a model alias "shared-name", and a node "xai" carrying a custom model id
	// "agy".
	seed := func(t *testing.T) (*DashboardHandler, func()) {
		t.Helper()
		repo, cleanup := setupTestDB(t)

		if err := repo.CreateCombo("c1", comboName, "", `["oc/space-bunny-free"]`, "round-robin"); err != nil {
			cleanup()
			t.Fatalf("seed combo %s: %v", comboName, err)
		}
		if err := repo.CreateCombo("c2", otherCombo, "", `["clinepass/deepseek/deepseek-v4-flash"]`, "fallback"); err != nil {
			cleanup()
			t.Fatalf("seed combo %s: %v", otherCombo, err)
		}
		if err := repo.SetKV("modelAliases", bareAlias, `"openai/gpt-4o"`); err != nil {
			cleanup()
			t.Fatalf("seed alias: %v", err)
		}
		if _, err := repo.CreateProviderNode(nodeID, "openai-compatible", "xai",
			`{"prefix":"xai","apiType":"chat","baseUrl":"http://xai.local/v1"}`); err != nil {
			cleanup()
			t.Fatalf("seed node: %v", err)
		}
		if err := repo.SetKV("customModels", nodeID+"|"+customID+"|llm",
			`{"id":"`+customID+`","providerAlias":"`+nodeID+`","type":"llm"}`); err != nil {
			cleanup()
			t.Fatalf("seed custom model: %v", err)
		}

		return NewDashboardHandler(repo), cleanup
	}

	do := func(h *DashboardHandler, method, target string, body string) *httptest.ResponseRecorder {
		r := chi.NewRouter()
		r.Route("/api", func(r chi.Router) {
			r.Post("/models/custom", h.HandleSaveCustomModel)
			r.Put("/models/alias", h.HandleSetModelAlias)
			r.Post("/combos", h.HandleCreateCombo)
			r.Put("/combos/{id}", h.HandleUpdateCombo)
		})
		req := httptest.NewRequest(method, target, bytes.NewReader([]byte(body)))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	// assertRefused checks the typed 409 the connection-name guard established.
	assertRefused := func(t *testing.T, rec *httptest.ResponseRecorder, wantCode string) {
		t.Helper()
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409 (body %s)", rec.Code, rec.Body)
		}
		var payload map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if payload["code"] != wantCode {
			t.Errorf("code = %v, want %s", payload["code"], wantCode)
		}
	}

	assertAccepted := func(t *testing.T, rec *httptest.ResponseRecorder, wantKey string) {
		t.Helper()
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
		}
		if wantKey == "" {
			return
		}
		var payload map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if payload["key"] != wantKey {
			t.Errorf("key = %v, want %s", payload["key"], wantKey)
		}
	}

	// assertCustomModel checks the row landed in the customModels kv scope.
	assertCustomModel := func(t *testing.T, h *DashboardHandler, key string) {
		t.Helper()
		kv, err := h.Repo.GetKVScope("customModels")
		if err != nil {
			t.Fatalf("read customModels: %v", err)
		}
		if _, ok := kv[key]; !ok {
			t.Fatalf("custom model %q was not written (have %v)", key, kv)
		}
	}

	t.Run("a custom model id matching a combo is accepted", func(t *testing.T) {
		h, cleanup := seed(t)
		defer cleanup()

		key := nodeID + "|" + comboName + "|llm"
		rec := do(h, http.MethodPost, "/api/models/custom",
			`{"providerAlias":"`+nodeID+`","id":"`+comboName+`","type":"llm"}`)
		assertAccepted(t, rec, key)
		assertCustomModel(t, h, key)
	})

	t.Run("a custom model id matching a model alias is accepted", func(t *testing.T) {
		h, cleanup := seed(t)
		defer cleanup()

		// The reported failure: /models listed an id a bare-name alias already
		// owned, and the import was refused.
		key := nodeID + "|" + bareAlias + "|llm"
		rec := do(h, http.MethodPost, "/api/models/custom",
			`{"providerAlias":"`+nodeID+`","id":"`+bareAlias+`","type":"llm"}`)
		assertAccepted(t, rec, key)

		assertCustomModel(t, h, key)
		if target, err := h.Repo.GetModelAlias(bareAlias); err != nil || target == "" {
			t.Fatalf("the alias must survive the write: %q (err %v)", target, err)
		}
	})

	t.Run("a combo name matching a custom model id is accepted", func(t *testing.T) {
		h, cleanup := seed(t)
		defer cleanup()

		// The other order of the same pair: the custom id is prefixed, so the
		// combo keeps the bare name and neither entry is shadowed. Refusing
		// this would make the guard order-dependent.
		rec := do(h, http.MethodPost, "/api/combos",
			`{"id":"c3","name":"`+customID+`","models":["oc/space-bunny-free"]}`)
		assertAccepted(t, rec, "")

		if combo, err := h.Repo.GetComboByName(customID); err != nil || combo == nil {
			t.Fatalf("combo was not created: %+v (err %v)", combo, err)
		}
	})

	t.Run("a model alias matching a custom model id is accepted", func(t *testing.T) {
		h, cleanup := seed(t)
		defer cleanup()

		rec := do(h, http.MethodPut, "/api/models/alias",
			`{"model":"openai/gpt-4o","alias":"`+customID+`"}`)
		assertAccepted(t, rec, "")

		if target, err := h.Repo.GetModelAlias(customID); err != nil || target != "openai/gpt-4o" {
			t.Fatalf("alias was not written: %q (err %v)", target, err)
		}
	})

	t.Run("a model alias matching a combo is refused", func(t *testing.T) {
		h, cleanup := seed(t)
		defer cleanup()

		// An alias outranks a combo in resolveModel, so this one silently
		// makes the combo unreachable by name.
		rec := do(h, http.MethodPut, "/api/models/alias",
			`{"model":"openai/gpt-4o","alias":"`+comboName+`"}`)
		assertRefused(t, rec, "COMBO_NAME_CONFLICT")

		if target, err := h.Repo.GetModelAlias(comboName); err != nil || target != "" {
			t.Fatalf("refused alias was still written: %q (err %v)", target, err)
		}
	})

	t.Run("a combo name matching a model alias is refused", func(t *testing.T) {
		h, cleanup := seed(t)
		defer cleanup()

		// The mirror of the case above: a bare request for the name is the
		// alias, so the new combo could never be reached.
		rec := do(h, http.MethodPost, "/api/combos",
			`{"id":"c3","name":"`+bareAlias+`","models":["oc/space-bunny-free"]}`)
		assertRefused(t, rec, "MODEL_ALIAS_CONFLICT")

		if combo, err := h.Repo.GetComboByName(bareAlias); err != nil || combo != nil {
			t.Fatalf("refused combo was still created: %+v (err %v)", combo, err)
		}
	})

	t.Run("renaming a combo into an alias name is refused and leaves the row alone", func(t *testing.T) {
		h, cleanup := seed(t)
		defer cleanup()

		rec := do(h, http.MethodPut, "/api/combos/c1",
			`{"name":"`+bareAlias+`","models":["oc/space-bunny-free"]}`)
		assertRefused(t, rec, "MODEL_ALIAS_CONFLICT")

		combo, err := h.Repo.GetComboById("c1")
		if err != nil || combo == nil {
			t.Fatalf("read combo: %v", err)
		}
		if combo.Name != comboName {
			t.Errorf("name = %q, want the original %q — a refused rename must not apply", combo.Name, comboName)
		}
	})

	t.Run("free names are unaffected", func(t *testing.T) {
		h, cleanup := seed(t)
		defer cleanup()

		tests := []struct {
			name   string
			method string
			target string
			body   string
		}{
			{"custom model with an unused id", http.MethodPost, "/api/models/custom",
				`{"providerAlias":"` + nodeID + `","id":"glm-5.3","type":"llm"}`},
			{"alias with an unused name", http.MethodPut, "/api/models/alias",
				`{"model":"openai/gpt-4o","alias":"fast-model"}`},
			{"combo with an unused name", http.MethodPost, "/api/combos",
				`{"id":"c4","name":"smart-combo","models":["clinepass/deepseek/deepseek-v4-flash"]}`},
			// A combo keeps its own name across an update: the guard must not
			// make every save of a combo collide with itself.
			{"combo update that keeps its name", http.MethodPut, "/api/combos/c1",
				`{"name":"` + comboName + `","models":["oc/space-bunny-free"]}`},
			// Resolution is case-sensitive (combos resolve on `WHERE name = ?`,
			// aliases on an exact kv key), so a differently-cased name is a
			// genuinely different address and must not be refused.
			{"combo differing only in case", http.MethodPost, "/api/combos",
				`{"id":"c5","name":"` + comboName + `-V2","models":["oc/space-bunny-free"]}`},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				rec := do(h, tt.method, tt.target, tt.body)
				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
				}
			})
		}
	})
}
