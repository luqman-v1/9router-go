package chat

import (
	"context"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/handlers/shared"
	"9router/proxy/internal/providers"
)

// TestFetchAntigravityProjectID_outcomes pins the classification that drives the
// refresh/no-cache decisions: pid found, token rejected, project definitively
// missing vs transient rate limit.
func TestFetchAntigravityProjectID_outcomes(t *testing.T) {
	tests := []struct {
		name        string
		loadAssist  int    // status code for loadCodeAssist
		loadBody    string // body for loadCodeAssist
		onboard     int    // status code for onboardUser
		onboardBody string // body for onboardUser
		wantPID     string
		wantAuth    bool
		wantNoProj  bool
	}{
		{
			name:       "project found",
			loadAssist: 200,
			loadBody:   `{"cloudaicompanionProject":{"id":"proj-123","name":"x"}}`,
			wantPID:    "proj-123",
		},
		{
			name:       "token rejected 401",
			loadAssist: 401,
			wantAuth:   true,
		},
		{
			name:       "token rejected 403",
			loadAssist: 403,
			wantAuth:   true,
		},
		{
			name:        "empty project confirmed",
			loadAssist:  200,
			loadBody:    `{"allowedTiers":[{"id":"standard-tier","isDefault":true}]}`,
			onboard:     200,
			onboardBody: `{"done":true,"response":{"cloudaicompanionProject":{}}}`,
			wantNoProj:  true,
		},
		{
			// loadCodeAssist already said "no project for this token" (200, tiers
			// only) — an onboardUser 429 afterwards doesn't change that verdict,
			// so it's still cached as no-project.
			name:       "onboard rate-limited after clean load",
			loadAssist: 200,
			loadBody:   `{"allowedTiers":[{"id":"standard-tier","isDefault":true}]}`,
			onboard:    429,
			wantNoProj: true,
			wantAuth:   false,
		},
		{
			name:       "concurrent connection refused is transient",
			loadAssist: 503,
			wantNoProj: false,
		},
	}

	oldDelay := antigravityProbeDelay
	antigravityProbeDelay = time.Millisecond
	defer func() { antigravityProbeDelay = oldDelay }()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "loadCodeAssist") {
					w.WriteHeader(tc.loadAssist)
					w.Write([]byte(tc.loadBody))
					return
				}
				w.WriteHeader(tc.onboard)
				w.Write([]byte(tc.onboardBody))
			}))
			defer srv.Close()

			oldL, oldO := loadCodeAssistURL, onboardUserURL
			loadCodeAssistURL, onboardUserURL = srv.URL+"/loadCodeAssist", srv.URL+"/onboardUser"
			defer func() { loadCodeAssistURL, onboardUserURL = oldL, oldO }()

			pid, auth, noProj := fetchAntigravityProjectID(context.Background(), srv.Client(), "test-token", nil)
			if pid != tc.wantPID {
				t.Errorf("pid = %q, want %q", pid, tc.wantPID)
			}
			if auth != tc.wantAuth {
				t.Errorf("authFailed = %v, want %v", auth, tc.wantAuth)
			}
			if noProj != tc.wantNoProj {
				t.Errorf("noProject = %v, want %v", noProj, tc.wantNoProj)
			}
		})
	}
}

func TestProjectNoCache(t *testing.T) {
	projectNoCache.Delete("test-conn")
	if projectProbeCached("test-conn") {
		t.Fatal("fresh cache should not report cached")
	}
	cacheProjectMissing("test-conn")
	if !projectProbeCached("test-conn") {
		t.Fatal("should be cached after cacheProjectMissing")
	}
	projectNoCache.Store("test-conn", int64(time.Now().Add(-time.Second).Unix()))
	if projectProbeCached("test-conn") {
		t.Fatal("expired cache entry should not report cached")
	}
}

func TestAntigravityProbe_HeadersAndMetadata(t *testing.T) {
	var gotUA, gotGoogClient, gotClientMeta string
	var gotBodyMeta map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotGoogClient = r.Header.Get("X-Goog-Api-Client")
		gotClientMeta = r.Header.Get("Client-Metadata")

		var body struct {
			Metadata map[string]any `json:"metadata"`
		}
		_ = json.Unmarshal(mustReadAll(r.Body), &body)
		gotBodyMeta = body.Metadata

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"cloudaicompanionProject":{"id":"test-proj"}}`))
	}))
	defer srv.Close()

	oldL, oldO := loadCodeAssistURL, onboardUserURL
	loadCodeAssistURL, onboardUserURL = srv.URL+"/loadCodeAssist", srv.URL+"/onboardUser"
	defer func() { loadCodeAssistURL, onboardUserURL = oldL, oldO }()

	pid, auth, noProj := fetchAntigravityProjectID(context.Background(), srv.Client(), "test-token", nil)
	if pid != "test-proj" || auth || noProj {
		t.Fatalf("unexpected result: pid=%q, auth=%v, noProj=%v", pid, auth, noProj)
	}

	// A nil connData is the media-lane shape: only a token, no connection. It must
	// fall back to the CLI identity, which is the one that keeps working for the
	// Gemini 4 Argon tiers.
	if want := providers.AntigravityUserAgentFor(providers.AntigravityProfileCLI); gotUA != want {
		t.Errorf("User-Agent = %q, want the CLI shape %q", gotUA, want)
	}
	if gotGoogClient != "" {
		t.Errorf("X-Goog-Api-Client = %q, want empty", gotGoogClient)
	}
	if gotClientMeta != "" {
		t.Errorf("Client-Metadata = %q, want empty", gotClientMeta)
	}
	if gotBodyMeta["ideType"] != float64(9) {
		t.Errorf("body metadata ideType = %v, want 9", gotBodyMeta["ideType"])
	}
	if gotBodyMeta["platform"] != float64(2) {
		t.Errorf("body metadata platform = %v, want 2", gotBodyMeta["platform"])
	}
	if gotBodyMeta["pluginType"] != float64(2) {
		t.Errorf("body metadata pluginType = %v, want 2", gotBodyMeta["pluginType"])
	}
}

func TestExtractProjectID(t *testing.T) {
	cases := []struct {
		input any
		want  string
	}{
		{nil, ""},
		{"", ""},
		{"  ", ""},
		{"proj-direct", "proj-direct"},
		{"  proj-trimmed  ", "proj-trimmed"},
		{map[string]any{"id": "proj-from-id"}, "proj-from-id"},
		{map[string]any{"projectId": "proj-from-projectId"}, "proj-from-projectId"},
		{map[string]any{"project_id": "proj-from-project_id"}, "proj-from-project_id"},
		{map[string]any{"id": "", "projectId": "fallback-pid"}, "fallback-pid"},
		{map[string]any{}, ""},
		{123, ""},
	}

	for _, tc := range cases {
		got := extractProjectID(tc.input)
		if got != tc.want {
			t.Errorf("extractProjectID(%v) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func mustReadAll(r io.Reader) []byte {
	b, _ := io.ReadAll(r)
	return b
}

// TestAntigravityProjectUserAgent pins the identity the onboarding RPCs are sent
// as. The value is load-bearing: Google answers the generic
// `google-api-nodejs-client` identity with a 200 that omits
// `cloudaicompanionProject` even for a provisioned account, so a caller sending
// it concludes "no project" and every chat then 502s for a healthy login.
//
// The profile comes from the connection, so an account signed in as the CLI is
// discovered as the CLI rather than as the IDE — and a caller with no
// connection data still gets the CLI shape rather than nothing.
func TestAntigravityProjectUserAgent(t *testing.T) {
	ideUA := providers.AntigravityUserAgentFor(providers.AntigravityProfileIDE)
	cliUA := providers.AntigravityUserAgentFor(providers.AntigravityProfileCLI)
	if ideUA == cliUA {
		t.Fatal("IDE and CLI user agents are identical; the profile setting would be a no-op")
	}

	tests := []struct {
		name string
		data *shared.ConnectionData
		want string
	}{
		{
			name: "nil conn data falls back to CLI",
			data: nil,
			want: cliUA,
		},
		{
			name: "no providerSpecificData falls back to CLI",
			data: &shared.ConnectionData{},
			want: cliUA,
		},
		{
			name: "cli profile",
			data: &shared.ConnectionData{ProviderSpecificData: map[string]any{"clientProfile": "cli"}},
			want: cliUA,
		},
		{
			name: "ide profile",
			data: &shared.ConnectionData{ProviderSpecificData: map[string]any{"clientProfile": "ide"}},
			want: ideUA,
		},
		{
			// Legacy spellings predate the official CLI profile and must keep
			// resolving so previously stored connections are not silently
			// downgraded to the IDE.
			name: "legacy harness resolves to CLI",
			data: &shared.ConnectionData{ProviderSpecificData: map[string]any{"clientProfile": "harness"}},
			want: cliUA,
		},
		{
			name: "legacy sdk resolves to CLI",
			data: &shared.ConnectionData{ProviderSpecificData: map[string]any{"clientProfile": "sdk"}},
			want: cliUA,
		},
		{
			name: "unknown value falls back to CLI",
			data: &shared.ConnectionData{ProviderSpecificData: map[string]any{"clientProfile": "nonsense"}},
			want: cliUA,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := antigravityProjectUserAgent(tt.data); got != tt.want {
				t.Errorf("antigravityProjectUserAgent() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestAntigravityProjectDiscovery_UsesConnectionProfile proves the profile is
// read from the connection and actually reaches the wire, not just through the
// unit-level helper: the request Google receives must carry the identity the
// connection signed in as.
func TestAntigravityProjectDiscovery_UsesConnectionProfile(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"cloudaicompanionProject":{"id":"proj-from-ide"}}`))
	}))
	defer srv.Close()
	oldL, oldO := loadCodeAssistURL, onboardUserURL
	loadCodeAssistURL, onboardUserURL = srv.URL+"/loadCodeAssist", srv.URL+"/onboardUser"
	defer func() { loadCodeAssistURL, onboardUserURL = oldL, oldO }()

	connData := &shared.ConnectionData{
		ProviderSpecificData: map[string]any{"clientProfile": "ide"},
	}
	pid, auth, noProj := fetchAntigravityProjectID(context.Background(), srv.Client(), "test-token", connData)
	if pid != "proj-from-ide" || auth || noProj {
		t.Fatalf("unexpected result: pid=%q, auth=%v, noProj=%v", pid, auth, noProj)
	}
	if want := providers.AntigravityUserAgentFor(providers.AntigravityProfileIDE); gotUA != want {
		t.Errorf("User-Agent on the wire = %q, want the IDE shape %q", gotUA, want)
	}
}
