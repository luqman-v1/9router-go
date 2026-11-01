package dashboard

import (
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandleCreateApiKeyNameIsTrimmedAndCapped pins the create path to the same
// name rules the update path enforces. The name rides along on every
// /api/keys listing and is rendered in the dashboard list, so an unbounded or
// whitespace-only one used to be stored verbatim: a pasted megabyte was echoed
// on every call, and a name the operator typed as spaces rendered as a blank
// row.
func TestHandleCreateApiKeyNameIsTrimmedAndCapped(t *testing.T) {
	tests := []struct {
		name string
		body map[string]any
		// wantName is what GET /api/keys must report afterwards; empty means
		// the request is refused and no row is created.
		wantName string
		wantCode int
	}{
		{
			name:     "a plain name is stored",
			body:     map[string]any{"name": "Production Key"},
			wantName: "Production Key",
			wantCode: http.StatusOK,
		},
		{
			// The dashboard trims before it sends, but a scripted caller does
			// not, and the stored value is what the table renders.
			name:     "surrounding whitespace is trimmed",
			body:     map[string]any{"name": "  Staging Key  "},
			wantName: "Staging Key",
			wantCode: http.StatusOK,
		},
		{
			// A whitespace-only name is an accident, not a label: it used to
			// create a row the list rendered as blank.
			name:     "whitespace-only name is stored empty",
			body:     map[string]any{"name": "   "},
			wantName: "",
			wantCode: http.StatusOK,
		},
		{
			name:     "omitted name is accepted",
			body:     map[string]any{},
			wantName: "",
			wantCode: http.StatusOK,
		},
		{
			name:     "at the length limit",
			body:     map[string]any{"name": strings.Repeat("a", maxAPIKeyNameLen)},
			wantName: strings.Repeat("a", maxAPIKeyNameLen),
			wantCode: http.StatusOK,
		},
		{
			name:     "one past the limit is refused",
			body:     map[string]any{"name": strings.Repeat("a", maxAPIKeyNameLen+1)},
			wantCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, cleanup := setupTestDB(t)
			defer cleanup()
			router := setupTestRouter(repo)

			raw, err := json.Marshal(tt.body)
			if err != nil {
				t.Fatalf("marshal body: %v", err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/keys", bytes.NewReader(raw))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != tt.wantCode {
				t.Fatalf("POST /api/keys = %d, want %d (body: %s)", rec.Code, tt.wantCode, rec.Body.String())
			}
			if tt.wantCode != http.StatusOK {
				if !strings.Contains(rec.Body.String(), "name is too long") {
					t.Errorf("refusal body = %q, want it to name the reason", rec.Body.String())
				}
				// A refused create must leave no row behind: the handler
				// validates before the first write.
				if keys, err := repo.GetApiKeys(); err != nil {
					t.Fatalf("GetApiKeys: %v", err)
				} else if len(keys) != 0 {
					t.Errorf("refused create left %d row(s) behind", len(keys))
				}
				return
			}

			row := listKeys(t, router)
			if got, _ := row["name"].(string); got != tt.wantName {
				t.Errorf("stored name = %q, want %q", got, tt.wantName)
			}
		})
	}
}
