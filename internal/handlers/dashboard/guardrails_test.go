package dashboard

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
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
