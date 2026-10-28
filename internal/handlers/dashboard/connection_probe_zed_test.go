package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
)

// zedProbeServer stands in for cloud.zed.dev and records the auth headers it
// was shown, so the test can assert the credential travelled verbatim.
type zedProbeServer struct {
	srv       *httptest.Server
	seenAuth  []string
	meStatus  int
	llmStatus int
	models    string
}

func newZedProbeServer(t *testing.T) *zedProbeServer {
	t.Helper()
	z := &zedProbeServer{meStatus: http.StatusOK, llmStatus: http.StatusOK,
		models: `{"models":[{"id":"claude-sonnet-5","provider":"anthropic","max_token_count":1000000,"max_output_tokens":128000}]}`}
	z.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		z.seenAuth = append(z.seenAuth, r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/client/users/me":
			w.WriteHeader(z.meStatus)
			if z.meStatus == http.StatusOK {
				_, _ = w.Write([]byte(`{"user":{"id":493663,"github_login":"azrinaldi"}}`))
			}
		case "/client/llm_tokens":
			w.WriteHeader(z.llmStatus)
			if z.llmStatus == http.StatusOK {
				_, _ = w.Write([]byte(`{"token":"header.payload.sig"}`))
			}
		case "/models":
			_, _ = w.Write([]byte(z.models))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(z.srv.Close)
	return z
}

// withZedBase points the client at the stub for the duration of the test.
func withZedBase(t *testing.T, url string) {
	t.Helper()
	previous := providers.ZedCloudBaseURL()
	providers.SetZedCloudBaseURL(url)
	t.Cleanup(func() { providers.SetZedCloudBaseURL(previous) })
}

func zedTestData() connectionProbeData {
	return connectionProbeData{
		AccessToken: `{"version":2,"id":"client_token_1","token":"secret"}`,
		ProviderSpecificData: map[string]any{
			"userId":         "493663",
			"organizationId": "org_1",
			"systemId":       "sys_1",
		},
	}
}

func TestProbeZedWalksBothAuthLayers(t *testing.T) {
	z := newZedProbeServer(t)
	withZedBase(t, z.srv.URL)

	out := (&DashboardHandler{}).probeZed(context.Background(), zedTestData(), z.srv.Client())
	if !out.valid {
		t.Fatalf("probe failed: %s", out.message)
	}
	// The account call must carry the blob verbatim — extracting the inner
	// "token" field is what Zed rejects.
	if len(z.seenAuth) == 0 {
		t.Fatal("no upstream call was made")
	}
	want := `493663 {"version":2,"id":"client_token_1","token":"secret"}`
	if z.seenAuth[0] != want {
		t.Errorf("account auth header = %q, want %q", z.seenAuth[0], want)
	}
}

func TestProbeZedRejectsRevokedToken(t *testing.T) {
	z := newZedProbeServer(t)
	z.meStatus = http.StatusUnauthorized
	withZedBase(t, z.srv.URL)

	out := (&DashboardHandler{}).probeZed(context.Background(), zedTestData(), z.srv.Client())
	if out.valid {
		t.Fatal("a revoked token was reported as valid")
	}
	if !strings.Contains(out.message, "revoked") {
		t.Errorf("message = %q, want it to say the token was revoked", out.message)
	}
}

func TestProbeZedReportsFailedTokenExchange(t *testing.T) {
	z := newZedProbeServer(t)
	z.llmStatus = http.StatusForbidden
	withZedBase(t, z.srv.URL)

	out := (&DashboardHandler{}).probeZed(context.Background(), zedTestData(), z.srv.Client())
	if out.valid {
		t.Fatal("a failed token exchange was reported as valid")
	}
	if !strings.Contains(out.message, "token exchange") {
		t.Errorf("message = %q, want it to mention the token exchange", out.message)
	}
}

func TestProbeZedRejectsEmptyCatalogue(t *testing.T) {
	z := newZedProbeServer(t)
	z.models = `{"models":[]}`
	withZedBase(t, z.srv.URL)

	out := (&DashboardHandler{}).probeZed(context.Background(), zedTestData(), z.srv.Client())
	if out.valid {
		t.Fatal("an empty catalogue was reported as valid")
	}
	if !strings.Contains(out.message, "subscription") {
		t.Errorf("message = %q, want it to point at the subscription", out.message)
	}
}

func TestProbeZedRequiresUserID(t *testing.T) {
	data := zedTestData()
	data.ProviderSpecificData = map[string]any{}
	out := (&DashboardHandler{}).probeZed(context.Background(), data, nil)
	if out.valid {
		t.Fatal("a credential without a user id was reported as valid")
	}
	if !strings.Contains(out.message, "user id") {
		t.Errorf("message = %q, want it to name the missing user id", out.message)
	}
}
