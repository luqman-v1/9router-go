package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// zedToken builds an unsigned JWT whose payload carries the given exp claim.
// The gateway never verifies the signature, so the payload is all that matters
// for the expiry logic under test.
func zedToken(t *testing.T, exp int64) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"exp": exp})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"HS256"}`)) + "." + enc(payload) + ".sig"
}

func TestZedAuthHeader(t *testing.T) {
	creds := ZedCredentials{
		AccessToken: `{"version":2,"id":"client_token_abc","token":"secret-token"}`,
		UserID:      "493663",
	}
	// Zed authenticates the account layer as "{user_id} {access_token}", never
	// as a bearer token. The access token is the credential blob verbatim:
	// Zed validates the whole envelope, so extracting its inner "token" field
	// is rejected with 401 even though the secret itself is unchanged.
	want := `493663 {"version":2,"id":"client_token_abc","token":"secret-token"}`
	if got := ZedAuthHeader(creds); got != want {
		t.Errorf("ZedAuthHeader() = %q, want %q", got, want)
	}
}

func TestZedWireProvider(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{"claude-haiku-5-5", "anthropic"},
		{"claude-sonnet-5-5", "anthropic"},
		{"CLAUDE-SONNET-4-5", "anthropic"},
		{"gpt-5.6-luna", "open_ai"},
		{"gpt-6.1-sol", "open_ai"},
		{"o3-mini", "open_ai"},
		{"unknown-model", ""},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			if got := ZedWireProvider(tt.model); got != tt.want {
				t.Errorf("ZedWireProvider(%q) = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}

func TestZedTokenExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)

	// A readable exp claim is honoured, minus the safety skew.
	exp := now.Add(10 * time.Minute).Unix()
	got := zedTokenExpiry(zedToken(t, exp), now)
	want := time.Unix(exp, 0)
	if !got.Equal(want) {
		t.Errorf("expiry = %v, want %v", got, want)
	}

	// An unreadable payload falls back to the conservative window rather than
	// being cached forever.
	got = zedTokenExpiry("not-a-jwt", now)
	if want := now.Add(zedLLMTokenTTL); !got.Equal(want) {
		t.Errorf("fallback expiry = %v, want %v", got, want)
	}

	// A token that is already expired is retired almost immediately, so the
	// next caller mints a fresh one instead of reusing a dead token.
	got = zedTokenExpiry(zedToken(t, now.Add(-time.Hour).Unix()), now)
	if !got.After(now) || got.After(now.Add(time.Minute)) {
		t.Errorf("expired token expiry = %v, want ~now", got)
	}
}

func TestZedLLMTokenExchange(t *testing.T) {
	var gotAuth, gotSystemID, gotPath, gotMethod string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		gotSystemID = r.Header.Get(ZedSystemIDHeader)
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"` + zedToken(t, time.Now().Add(time.Hour).Unix()) + `"}`))
	}))
	defer srv.Close()

	// Point the client at the test server.
	origBase := zedCloudBaseURL
	restore := func() { zedCloudBaseURL = origBase }
	defer restore()
	zedCloudBaseURL = srv.URL

	creds := ZedCredentials{
		AccessToken:    `{"version":2,"id":"client_token_abc","token":"secret-token"}`,
		UserID:         "493663",
		OrganizationID: "org_01km17g87057pgmpyaftmtcd3w",
		SystemID:       "3dee12f7-2647-471c-b09b-276a96e9896b",
	}
	token, err := ZedLLMToken(context.Background(), srv.Client(), creds)
	if err != nil {
		t.Fatalf("ZedLLMToken: %v", err)
	}
	if token == "" {
		t.Fatal("expected a token")
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != ZedLLMTokensPath {
		t.Errorf("path = %q, want %q", gotPath, ZedLLMTokensPath)
	}
	if want := `493663 {"version":2,"id":"client_token_abc","token":"secret-token"}`; gotAuth != want {
		t.Errorf("Authorization = %q, want %q", gotAuth, want)
	}
	if gotSystemID != creds.SystemID {
		t.Errorf("system id header = %q, want %q", gotSystemID, creds.SystemID)
	}
	if gotBody["organization_id"] != creds.OrganizationID {
		t.Errorf("organization_id = %v, want %q", gotBody["organization_id"], creds.OrganizationID)
	}
}

func TestZedFetchModels(t *testing.T) {
	origBase := zedCloudBaseURL
	restore := func() { zedCloudBaseURL = origBase }
	defer restore()

	var modelsAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case ZedLLMTokensPath:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"` + zedToken(t, time.Now().Add(time.Hour).Unix()) + `"}`))
		case ZedModelsPath:
			modelsAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"models":[
				{"provider":"anthropic","id":"claude-haiku-5-5","display_name":"Claude Haiku 5.5",
				 "max_token_count":1000000,"max_output_tokens":128000,"supports_tools":true,
				 "supports_images":true,"supports_thinking":true,"is_latest":false,"is_disabled":false,
				 "supported_effort_levels":[{"name":"Low","value":"low"},{"name":"High","value":"high"}]},
				{"provider":"open_ai","id":"gpt-5.6-luna","max_token_count":400000,
				 "max_output_tokens":64000,"is_disabled":true}
			]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	zedCloudBaseURL = srv.URL

	creds := ZedCredentials{AccessToken: "raw-token", UserID: "493663", OrganizationID: "org_1"}
	catalog, err := ZedFetchModels(context.Background(), srv.Client(), creds)
	if err != nil {
		t.Fatalf("ZedFetchModels: %v", err)
	}
	if len(catalog) != 2 {
		t.Fatalf("got %d models, want 2", len(catalog))
	}
	// The model list is behind the LLM token, not the account token.
	if !strings.HasPrefix(modelsAuth, "Bearer ") {
		t.Errorf("models Authorization = %q, want a bearer token", modelsAuth)
	}
	m := catalog[0]
	if m.ID != "claude-haiku-5-5" || m.Provider != "anthropic" {
		t.Errorf("first model = %+v", m)
	}
	if m.MaxTokenCount != 1000000 || m.MaxOutputTokens != 128000 {
		t.Errorf("limits = %d/%d, want 1000000/128000", m.MaxTokenCount, m.MaxOutputTokens)
	}
	if !m.SupportsThinking || !m.SupportsTools || !m.SupportsImages {
		t.Errorf("capabilities = %+v", m)
	}
	if len(m.EffortLevels) != 2 || m.EffortLevels[0] != "low" {
		t.Errorf("effort levels = %v", m.EffortLevels)
	}
}
