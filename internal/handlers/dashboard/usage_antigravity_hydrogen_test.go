package dashboard

import (
	"context"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/providers"
)

const antigravityHydrogenModelsBody = `{
	"models": {
		"gemini-4-hydrogen-xhigh": {"quotaInfo": {"remainingFraction": 0.9}},
		"gemini-4-hydrogen-high": {"quotaInfo": {"remainingFraction": 0.8}},
		"gemini-4-hydrogen-medium": {"quotaInfo": {"remainingFraction": 0.7}},
		"gemini-4-hydrogen-low": {"quotaInfo": {"remainingFraction": 0.6}},
		"gemini-4-carbon-high": {"quotaInfo": {"remainingFraction": 0.5}}
	}
}`

func antigravityPaidQuotaServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1internal:loadCodeAssist", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"cloudaicompanionProject":"proj","currentTier":{"name":"Pro"},"paidTier":{"id":"pro"}}`))
	})
	mux.HandleFunc("/v1internal:fetchAvailableModels", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(antigravityHydrogenModelsBody))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func useAntigravityQuotaServer(t *testing.T, srv *httptest.Server) {
	t.Helper()
	prevProd, prevDaily := antigravityDashboardBaseURL, antigravityDailyBaseURL
	antigravityDashboardBaseURL, antigravityDailyBaseURL = srv.URL, srv.URL
	t.Cleanup(func() {
		antigravityDashboardBaseURL, antigravityDailyBaseURL = prevProd, prevDaily
	})
}

func assertHydrogenQuotas(t *testing.T, quotas map[string]any) {
	t.Helper()
	for _, model := range []string{
		"gemini-4-hydrogen-xhigh",
		"gemini-4-hydrogen-high",
		"gemini-4-hydrogen-medium",
		"gemini-4-hydrogen-low",
	} {
		if _, ok := quotas[model]; !ok {
			t.Errorf("quota tracker is missing %s; got %v", model, quotas)
		}
	}
}

func TestFetchAntigravityDashboardUsage_HydrogenTiers(t *testing.T) {
	useAntigravityQuotaServer(t, antigravityPaidQuotaServer(t))

	res := fetchAntigravityDashboardUsage(context.Background(), "tok", "proj")
	if len(res.quotas) == 0 {
		t.Fatalf("paid-tier quotas empty: %+v", res)
	}
	assertHydrogenQuotas(t, res.quotas)
}

func TestHandleGetConnectionUsage_AGYACPShowsHydrogen(t *testing.T) {
	fastQuotaGate(t)
	useAntigravityQuotaServer(t, antigravityPaidQuotaServer(t))

	repo, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	if err := repo.CreateProviderConnectionFull(context.Background(),
		"agcpy-hydrogen", "agy-acp", "oauth", "ACP", nil,
		`{"accessToken":"tok","refreshToken":"refresh","projectId":"proj"}`,
	); err != nil {
		t.Fatalf("seed agy-acp connection: %v", err)
	}

	router := setupTestRouter(repo)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/usage/agcpy-hydrogen", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Quotas map[string]any `json:"quotas"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode usage response: %v", err)
	}
	assertHydrogenQuotas(t, out.Quotas)
}

func TestAntigravityUsageToken_AGYACPSourceReference(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	if err := repo.CreateProviderConnectionFull(context.Background(),
		"ag-source", "antigravity", "oauth", "AG", nil,
		`{"accessToken":"source-tok","refreshToken":"refresh","projectId":"source-proj"}`,
	); err != nil {
		t.Fatalf("seed source connection: %v", err)
	}

	h := &DashboardHandler{Repo: repo}
	token, project := h.antigravityUsageToken(context.Background(), "agy-acp", map[string]any{
		"providerSpecificData": map[string]any{"sourceConnection": "ag-source"},
	})
	if token != "source-tok" || project != "source-proj" {
		t.Errorf("agy-acp source credentials = (%q, %q), want (source-tok, source-proj)", token, project)
	}
}

// TestAntigravityDashboardHeaders_ClientVersionMatchesUA pins the identity pair
// the usage RPCs present. The UA is the CLI fingerprint, so X-Client-Version
// must carry the CLI version; sending the IDE version beside a CLI UA is the
// one shape no real client produces.
func TestAntigravityDashboardHeaders_ClientVersionMatchesUA(t *testing.T) {
	headers := antigravityDashboardHeaders("tok")
	if got := headers["User-Agent"]; got != providers.AntigravityCLIUserAgent {
		t.Errorf("User-Agent = %q, want %q", got, providers.AntigravityCLIUserAgent)
	}
	if got := headers["X-Client-Version"]; got != providers.AntigravityCLIVersion {
		t.Errorf("X-Client-Version = %q, want %q", got, providers.AntigravityCLIVersion)
	}
}
