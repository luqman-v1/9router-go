package dashboard

import (
	"net/http"
	"strings"
	"testing"

	json "encoding/json/v2"
)

func TestSaveCustomModelExtraBodyValidation(t *testing.T) {
	const key = "openrouter|vendor/m:free|llm"
	wrap := func(extra string) string {
		return `{"key":"` + key + `","value":{"providerAlias":"openrouter","id":"vendor/m:free","type":"llm","extraBody":` + extra + `}}`
	}
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantExtra  string // stored extraBody; "" means absent
	}{
		{"valid object", wrap(`{"temperature":0.7,"reasoning":{"effort":"high"}}`), http.StatusOK, `{"temperature":0.7,"reasoning":{"effort":"high"}}`},
		{"string value form", `{"key":"` + key + `","value":"{\"id\":\"vendor/m:free\",\"extraBody\":{\"top_k\":3}}"}`, http.StatusOK, `{"top_k":3}`},
		{"null clears", wrap(`null`), http.StatusOK, ""},
		{"empty string clears", wrap(`""`), http.StatusOK, ""},
		{"array refused", wrap(`[1,2]`), http.StatusBadRequest, ""},
		{"string refused", wrap(`"x"`), http.StatusBadRequest, ""},
		{"oversize refused", wrap(`{"x":"` + strings.Repeat("a", 9000) + `"}`), http.StatusBadRequest, ""},
		{"large integer kept exact", wrap(`{"seed":12345678901234567}`), http.StatusOK, `{"seed":12345678901234567}`},
		{"large integer kept exact in string value", `{"key":"` + key + `","value":"{\"id\":\"vendor/m:free\",\"extraBody\":{\"seed\":12345678901234567}}"}`, http.StatusOK, `{"seed":12345678901234567}`},
		{"or alias accepted", `{"key":"or|vendor/m:free|llm","value":{"providerAlias":"or","id":"vendor/m:free","extraBody":{"top_k":1}}}`, http.StatusOK, `{"top_k":1}`},
		{"non-openrouter refused", `{"key":"openai|vendor/m:free|llm","value":{"providerAlias":"openai","id":"vendor/m:free","extraBody":{"top_k":1}}}`, http.StatusBadRequest, ""},
		{"non-openrouter key-only refused", `{"key":"opencode|vendor/m:free|llm","value":{"id":"vendor/m:free","extraBody":{"top_k":1}}}`, http.StatusBadRequest, ""},
		{"non-openrouter clear accepted", `{"key":"openai|vendor/m:free|llm","value":{"providerAlias":"openai","id":"vendor/m:free","extraBody":null}}`, http.StatusOK, ""},
	}
	for _, denied := range []string{"provider", "plugins", "model", "messages", "stream", "tools",
		"max_tokens", "Temperature", "STREAM"} {
		tests = append(tests, struct {
			name       string
			body       string
			wantStatus int
			wantExtra  string
		}{"unknown key " + denied, wrap(`{"` + denied + `":1}`), http.StatusBadRequest, ""})
	}
	for name, extra := range map[string]string{
		"wrong type":         `{"temperature":"hot"}`,
		"bad reasoning key":  `{"reasoning":{"foo":1}}`,
		"max_tokens+effort":  `{"reasoning":{"max_tokens":5,"effort":"low"}}`,
		"max_tokens+top eff": `{"reasoning":{"max_tokens":5},"reasoning_effort":"low"}`,
	} {
		tests = append(tests, struct {
			name       string
			body       string
			wantStatus int
			wantExtra  string
		}{"invalid " + name, wrap(extra), http.StatusBadRequest, ""})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, cleanup := setupTestDB(t)
			defer cleanup()
			rec := postCustomModels(t, setupTestRouter(repo), tt.body)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantStatus != http.StatusOK {
				var resp struct {
					Code string `json:"code"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.Code != "INVALID_EXTRA_BODY" {
					t.Fatalf("error body = %s, want code INVALID_EXTRA_BODY", rec.Body.String())
				}
				if all, _ := repo.GetKVScope("customModels"); len(all) != 0 {
					t.Fatalf("refused save stored %v", all)
				}
				return
			}
			customs, err := repo.GetCustomModels()
			if err != nil || len(customs) != 1 {
				t.Fatalf("GetCustomModels = %v, %v", customs, err)
			}
			if got := string(customs[0].ExtraBody); got != tt.wantExtra {
				t.Errorf("stored extraBody = %q, want %q", got, tt.wantExtra)
			}
		})
	}
}
