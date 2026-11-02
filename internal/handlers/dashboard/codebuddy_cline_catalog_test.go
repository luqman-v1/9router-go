package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
)

// CodeBuddy, WorkBuddy and the Cline pair publish no OpenAI-style /models route,
// so the dashboard's fetch button used to answer "does not support models
// listing". These cases pin that each family now returns its own catalogue and
// that the namespaces stay disjoint.
func TestWriteConnectionModels_NoModelsRouteProviders(t *testing.T) {
	tests := []struct {
		name       string
		provider   string
		wantID     string
		wantAbsent string
	}{
		{
			name:     "codebuddy-cn serves its own catalogue",
			provider: "codebuddy-cn",
			wantID:   "glm-5.2",
		},
		{
			name:     "codebuddy-intl serves its own catalogue",
			provider: "codebuddy-intl",
			wantID:   "minimax-m3",
		},
		{
			name:     "cbai alias resolves to the intl catalogue",
			provider: "cbai",
			wantID:   "minimax-m3",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, cleanup := setupNodeTestDB(t)
			defer cleanup()
			router := setupTestRouter(repo)
			connID := tt.provider + "-conn"
			if err := repo.CreateProviderConnection(connID, tt.provider, "apiKey", tt.provider+" conn", "k"); err != nil {
				t.Fatalf("seed connection: %v", err)
			}
			req := httptest.NewRequest(http.MethodGet, "/api/providers/"+connID+"/models", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			if !strings.Contains(body, `"warning"`) {
				t.Error("response must carry the static-catalogue warning so the operator knows why the list is not live")
			}
			if tt.wantID != "" && !strings.Contains(body, `"`+tt.wantID+`"`) {
				t.Errorf("catalogue missing %q: %s", tt.wantID, body)
			}
			if tt.wantAbsent != "" && strings.Contains(body, `"`+tt.wantAbsent+`"`) {
				t.Errorf("catalogue must not offer %q for %s: %s", tt.wantAbsent, tt.provider, body)
			}
		})
	}
}

// The non-chat surfaces are rejected by name, on segment boundaries only, so a
// legitimate id that merely contains the letters survives.
func TestIsDiscoverableCodebuddyModel(t *testing.T) {
	tests := []struct {
		id   string
		want bool
	}{
		{"glm-5.2", true},
		{"minimax-m3", true},
		{"deepseek-v4.1-flash", true},
		{"", false},
		{"glm-image", false},
		{"image", false},
		{"veo-3", false},
		{"kokoro-tts", false},
		{"text-embedding-3", false},
		{"rerank-v3", false},
		{"bge-reranker", true}, // the token is reranker, not rerank; the segment rule must not overreach
		{"bge-rerank-v3", false},
	}
	for _, tt := range tests {
		if got := isDiscoverableCodebuddyModel(tt.id); got != tt.want {
			t.Errorf("isDiscoverableCodebuddyModel(%q) = %v, want %v", tt.id, got, tt.want)
		}
	}
}

// The two Cline namespaces are disjoint: `cline` excludes the cline-pass/*
// subscription rows and non-text-output rows, `clinepass` keeps only cline-pass/*.
// Advertising a subscription model on the free connection gets a 403
// ENTITLEMENT_ERROR, so this split is the whole point of the branch.
func TestParseClineCatalogNamespaces(t *testing.T) {
	full := []byte(`{"data":[
		{"id":"cline-free/deepseek-v4.1-flash","name":"Free DeepSeek"},
		{"id":"z-ai/glm-5.3-flash","name":"GLM"},
		{"id":"cline-pass/opus","name":"ClinePass Opus"},
		{"id":"acme/embed-only","name":"Embed","architecture":{"output_modalities":["embedding"]}}
	]}`)
	got := parseClineCatalog(full, false)
	if hasID(got, "cline-pass/opus") {
		t.Error("cline must not offer cline-pass/* rows; the free connection answers 403")
	}
	if hasID(got, "acme/embed-only") {
		t.Error("cline must not offer non-text-output rows; they cannot serve a chat completion")
	}
	if !hasID(got, "cline-free/deepseek-v4.1-flash") || !hasID(got, "z-ai/glm-5.3-flash") {
		t.Errorf("cline dropped usable rows: %v", got)
	}

	recommended := []byte(`{"clinePass":[{"id":"cline-pass/opus","name":"Opus"}],"free":[{"id":"cline-free/x","name":"X"}]}`)
	pass := parseClineCatalog(recommended, true)
	if !hasID(pass, "cline-pass/opus") {
		t.Errorf("clinepass must keep cline-pass/* rows: %v", pass)
	}
	if hasID(pass, "cline-free/x") {
		t.Error("clinepass must not offer the free namespace")
	}
}

// A row with no architecture block is admitted, so an upstream schema change
// degrades to a longer list rather than silently emptying the catalogue.
func TestParseClineCatalogMissingArchitectureIsAdmitted(t *testing.T) {
	got := parseClineCatalog([]byte(`[{"id":"acme/unknown-shape","name":"Unknown"}]`), false)
	if !hasID(got, "acme/unknown-shape") {
		t.Errorf("row without architecture must be admitted (fail-open): %v", got)
	}
}

// The legacy "text+image->text" modality shape is read on its output side only.
func TestParseClineCatalogLegacyModality(t *testing.T) {
	got := parseClineCatalog([]byte(`[{"id":"acme/legacy","architecture":{"modality":"text+image->text"}},
		{"id":"acme/legacy-img","architecture":{"modality":"text->image"}}]`), false)
	if !hasID(got, "acme/legacy") {
		t.Error("a ->text output must be admitted")
	}
	if hasID(got, "acme/legacy-img") {
		t.Error("a ->image output must be rejected")
	}
}

// The known-but-unlisted ids are appended only for `cline`, never for `clinepass`.
func TestMergeClineExtraCatalogRows(t *testing.T) {
	base := []clineModelItem{{ID: "z-ai/glm-5.3-flash", Name: "GLM"}}
	merged := mergeClineExtraCatalogRows(append([]clineModelItem{}, base...), false)
	if !hasID(merged, "cline-free/deepseek-v4.1-flash") {
		t.Errorf("unlisted but served id must be appended: %v", merged)
	}
	// A row already present is not duplicated: the one input id is kept once and
	// only the three absent extras are appended.
	withDup := mergeClineExtraCatalogRows([]clineModelItem{{ID: "cline-free/deepseek-v4.1-flash", Name: "x"}}, false)
	if len(withDup) != 1+len(clineExtraCatalogRows)-1 {
		t.Errorf("present row must not be duplicated: %v", withDup)
	}
	count := 0
	for _, m := range withDup {
		if m.ID == "cline-free/deepseek-v4.1-flash" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("id appeared %d times, want exactly 1", count)
	}
	if got := mergeClineExtraCatalogRows(append([]clineModelItem{}, base...), true); len(got) != len(base) {
		t.Errorf("clinepass must not receive the extra rows: %v", got)
	}
}

// The workos: prefix is applied to a WorkOS JWT only; a ClinePass key rides plain.
func TestClineAuthHeader(t *testing.T) {
	jwt := "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiIxIn0.sig"
	if got := clineAuthHeader(jwt); got != "workos:"+jwt {
		t.Errorf("WorkOS JWT must take the prefix, got %q", got)
	}
	if got := clineAuthHeader("workos:" + jwt); got != "workos:"+jwt {
		t.Errorf("an already-prefixed token must be left alone, got %q", got)
	}
	if got := clineAuthHeader("clp_live_abc123"); got != "clp_live_abc123" {
		t.Errorf("a ClinePass key must ride plain Bearer, got %q", got)
	}
}

func hasID(items []clineModelItem, id string) bool {
	for _, m := range items {
		if m.ID == id {
			return true
		}
	}
	return false
}

// The registry must publish the CodeBuddy catalogues the branch serves, or the
// branch returns an empty list and Import looks broken for a different reason.
func TestProviderRegistryHasCodebuddyCatalogues(t *testing.T) {
	for _, id := range []string{"codebuddy-cn", "codebuddy-intl"} {
		if len(providers.GetProviderModels(id)) == 0 {
			t.Errorf("%s has no registered models; the dashboard branch would offer nothing", id)
		}
	}
}
