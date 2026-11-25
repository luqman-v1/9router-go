package dashboard

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/db"
)

func TestValidateGuardrailConfig_AcceptsCustomPatterns(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		wantErr bool
	}{
		{
			name:    "valid custom pattern only",
			config:  `{"action":"block","customPatterns":[{"name":"proj","regex":"PROJ-[0-9]+"}]}`,
			wantErr: false,
		},
		{
			name:    "valid custom with custom detector in list",
			config:  `{"action":"mask","detectors":["custom"],"customPatterns":[{"name":"ssn","regex":"\\d{3}-\\d{2}-\\d{4}","replacement":"[SSN]"}]}`,
			wantErr: false,
		},
		{
			name:    "invalid regex in custom pattern",
			config:  `{"action":"block","customPatterns":[{"name":"broken","regex":"[invalid("}]}`,
			wantErr: true,
		},
		{
			name:    "empty regex in custom pattern",
			config:  `{"action":"block","customPatterns":[{"name":"empty","regex":"   "}]}`,
			wantErr: true,
		},
		{
			name:    "both detectors and custom patterns missing",
			config:  `{"action":"block"}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateGuardrailConfig(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateGuardrailConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestHandleCreateGuardrailPolicy_ValidatesCustomPatterns(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	h := NewDashboardHandler(repo)

	// Valid policy with customPatterns
	body := `{"name":"Project Codes","scope":"global","config":"{\"action\":\"block\",\"customPatterns\":[{\"name\":\"proj\",\"regex\":\"PROJ-[0-9]+\"}]}"}`
	req := httptest.NewRequest("POST", "/api/guardrails/policies", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()

	h.HandleCreateGuardrailPolicy(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// Invalid policy with broken regex
	brokenBody := `{"name":"Broken Regex","scope":"global","config":"{\"action\":\"block\",\"customPatterns\":[{\"name\":\"broken\",\"regex\":\"[unclosed\"}]}"}`
	req2 := httptest.NewRequest("POST", "/api/guardrails/policies", bytes.NewReader([]byte(brokenBody)))
	rec2 := httptest.NewRecorder()

	h.HandleCreateGuardrailPolicy(rec2, req2)
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec2.Code, rec2.Body.String())
	}
}

func TestHandleUpdateGuardrailPolicy(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	h := NewDashboardHandler(repo)

	// Seed a policy through the create handler so the row has the same
	// shape (tenant, timestamps) a real operator would have.
	seedBody := `{"name":"Old name","scope":"global","config":"{\"action\":\"log_only\",\"detectors\":[\"pii\"]}"}`
	seedReq := httptest.NewRequest("POST", "/api/guardrails/policies", bytes.NewReader([]byte(seedBody)))
	seedRec := httptest.NewRecorder()
	h.HandleCreateGuardrailPolicy(seedRec, seedReq)
	if seedRec.Code != http.StatusOK {
		t.Fatalf("seed create status = %d, want 200: %s", seedRec.Code, seedRec.Body.String())
	}
	var seeded db.GuardrailPolicy
	if err := json.Unmarshal(seedRec.Body.Bytes(), &seeded); err != nil {
		t.Fatalf("unmarshal seeded policy: %v", err)
	}

	t.Run("updates in place, keeps id and created_at", func(t *testing.T) {
		body := `{"name":"New name","scope":"global","config":"{\"action\":\"block\",\"detectors\":[\"pii\",\"injection\"]}"}`
		req := httptest.NewRequest("PUT", "/api/guardrails/policies/"+seeded.ID, bytes.NewReader([]byte(body)))
		req.SetPathValue("id", seeded.ID)
		rec := httptest.NewRecorder()

		h.HandleUpdateGuardrailPolicy(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}

		var updated db.GuardrailPolicy
		if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
			t.Fatalf("unmarshal updated policy: %v", err)
		}
		if updated.ID != seeded.ID {
			t.Errorf("id = %q, want unchanged %q", updated.ID, seeded.ID)
		}
		if updated.Name != "New name" {
			t.Errorf("name = %q, want %q", updated.Name, "New name")
		}
		if updated.CreatedAt != seeded.CreatedAt {
			t.Errorf("createdAt = %q, want unchanged %q", updated.CreatedAt, seeded.CreatedAt)
		}
		if updated.UpdatedAt < seeded.UpdatedAt {
			t.Errorf("updatedAt moved backwards: %q < %q", updated.UpdatedAt, seeded.UpdatedAt)
		}

		// The update must not create a second row: the scope still lists
		// exactly one policy.
		listReq := httptest.NewRequest("GET", "/api/guardrails/policies?scope=global", nil)
		listRec := httptest.NewRecorder()
		h.HandleGetGuardrailPolicies(listRec, listReq)
		var listed []db.GuardrailPolicy
		if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
			t.Fatalf("unmarshal policy list: %v", err)
		}
		if len(listed) != 1 {
			t.Fatalf("policies in scope = %d, want 1 (no duplicate row)", len(listed))
		}
	})

	t.Run("preserves enabled state from payload", func(t *testing.T) {
		body := `{"name":"New name","scope":"global","enabled":false,"config":"{\"action\":\"block\",\"detectors\":[\"pii\"]}"}`
		req := httptest.NewRequest("PUT", "/api/guardrails/policies/"+seeded.ID, bytes.NewReader([]byte(body)))
		req.SetPathValue("id", seeded.ID)
		rec := httptest.NewRecorder()

		h.HandleUpdateGuardrailPolicy(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		var updated db.GuardrailPolicy
		if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
			t.Fatalf("unmarshal updated policy: %v", err)
		}
		if updated.Enabled {
			t.Error("enabled = true, want false (payload value must survive)")
		}
	})

	t.Run("unknown id returns 404", func(t *testing.T) {
		body := `{"name":"Ghost","scope":"global","config":"{\"action\":\"allow\"}"}`
		req := httptest.NewRequest("PUT", "/api/guardrails/policies/does-not-exist", bytes.NewReader([]byte(body)))
		req.SetPathValue("id", "does-not-exist")
		rec := httptest.NewRecorder()

		h.HandleUpdateGuardrailPolicy(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("invalid payloads rejected", func(t *testing.T) {
		for name, body := range map[string]string{
			"missing name":  `{"scope":"global","config":"{\"action\":\"allow\"}"}`,
			"broken config": `{"name":"X","scope":"global","config":"{\"action\":\"block\",\"customPatterns\":[{\"name\":\"b\",\"regex\":\"[unclosed\"}]}"}`,
			"bad scope":     `{"name":"X","scope":"galaxy","config":"{\"action\":\"allow\"}"}`,
		} {
			t.Run(name, func(t *testing.T) {
				req := httptest.NewRequest("PUT", "/api/guardrails/policies/"+seeded.ID, bytes.NewReader([]byte(body)))
				req.SetPathValue("id", seeded.ID)
				rec := httptest.NewRecorder()
				h.HandleUpdateGuardrailPolicy(rec, req)
				if rec.Code != http.StatusBadRequest {
					t.Errorf("status = %d, want 400: %s", rec.Code, rec.Body.String())
				}
			})
		}
	})
}
