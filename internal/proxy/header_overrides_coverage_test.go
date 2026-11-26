package proxy

import (
	"net/http"
	"net/http/httptest"

	"testing"

	"9router/proxy/internal/providers"
)

// The Custom Headers feature is only real if the header reaches the upstream.
// Before this fix the override map was applied on the OpenAI lane alone: the
// dashboard accepted an entry for antigravity, bedrock or vertex, showed it as
// saved, and the request went out without it because those forwarders assemble
// their own header map and never read cfg.StaticHeaders.
//
// Each case below drives one forwarder at a fake upstream and asserts the
// operator's header arrived, so a lane that silently stops applying it fails
// here instead of in production.
func TestHeaderOverridesReachEveryForwarder(t *testing.T) {
	tests := []struct {
		name string
		call func(t *testing.T, cfg *providers.ProviderConfig, client *http.Client)
	}{
		{
			name: "ForwardOpenAI",
			call: func(t *testing.T, cfg *providers.ProviderConfig, client *http.Client) {
				t.Helper()
				resp, err := ForwardOpenAI(t.Context(), client, cfg, "sk-test", []byte(`{}`), false)
				if err != nil {
					t.Fatalf("ForwardOpenAI: %v", err)
				}
				resp.Body.Close()
			},
		},
		{
			name: "ForwardGemini",
			call: func(t *testing.T, cfg *providers.ProviderConfig, client *http.Client) {
				t.Helper()
				resp, err := ForwardGemini(t.Context(), client, cfg, "sk-test", `{"messages":[{"role":"user","content":"hi"}]}`, false, "", "gemini-2.5-pro", "")
				if err != nil {
					t.Fatalf("ForwardGemini: %v", err)
				}
				resp.Body.Close()
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := make(chan string, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case got <- r.Header.Get("X-Operator-Trace"):
				default:
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			cfg := &providers.ProviderConfig{
				BaseURL:         srv.URL,
				AuthHeader:      "Authorization",
				AuthScheme:      "bearer",
				HeaderOverrides: map[string]string{"X-Operator-Trace": "abc123"},
			}
			tt.call(t, cfg, srv.Client())

			select {
			case header := <-got:
				if header != "abc123" {
					t.Errorf("X-Operator-Trace = %q, want %q", header, "abc123")
				}
			default:
				t.Fatal("upstream received no request")
			}
		})
	}
}

// A case-variant override must still collapse to one header on the native
// lanes: they write "User-Agent" themselves, and an operator entry spelled
// "user-agent" would otherwise leave two keys in the map where the winner is
// whichever the map iterates last.
func TestHeaderOverridesCaseVariantWinsOnGeminiLane(t *testing.T) {
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case got <- r.Header.Get("User-Agent"):
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{
		BaseURL:         srv.URL,
		HeaderOverrides: map[string]string{"user-agent": "operator-ua/1.0"},
	}
	resp, err := ForwardGemini(t.Context(), srv.Client(), cfg, "sk-test", `{"messages":[{"role":"user","content":"hi"}]}`, false, "", "gemini-2.5-pro", "")
	if err != nil {
		t.Fatalf("ForwardGemini: %v", err)
	}
	resp.Body.Close()

	select {
	case ua := <-got:
		if ua != "operator-ua/1.0" {
			t.Errorf("User-Agent = %q, want the operator override", ua)
		}
	default:
		t.Fatal("upstream received no request")
	}
}

// Bedrock signs its headers, so its assertion lives in
// TestBedrockOverrideIsSignedWithRequest (bedrock_test.go): an override that
// arrived only after signing would be rejected by AWS.
