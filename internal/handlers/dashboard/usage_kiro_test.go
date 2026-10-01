package dashboard

import "testing"

// A Kiro connection stores its credential in apiKey, not accessToken. The quota
// path used to read only accessToken, so it sent an empty bearer and reported
// "quota API rejected the current token" while chat worked with the very
// credential the quota read never looked at (issue #78 item 4).
func TestKiroUsageToken(t *testing.T) {
	tests := []struct {
		name        string
		accessToken string
		apiKey      string
		psd         map[string]any
		want        string
	}{
		{
			name:        "api_key auth sends the api key",
			apiKey:      "ak-123",
			psd:         map[string]any{"authMethod": "api_key"},
			want:        "ak-123",
		},
		{
			name:        "api_key auth with both fields still sends the api key",
			accessToken: "oauth-token",
			apiKey:      "ak-123",
			psd:         map[string]any{"authMethod": "api_key"},
			want:        "ak-123",
		},
		{
			name:        "oauth sends the access token",
			accessToken: "oauth-token",
			apiKey:      "ak-123",
			psd:         map[string]any{"authMethod": "builder-id"},
			want:        "oauth-token",
		},
		{
			name:   "a connection with only an api key is usable",
			apiKey: "ak-123",
			psd:    map[string]any{},
			want:   "ak-123",
		},
		{
			name:        "a connection with no credential yields nothing to send",
			accessToken: "",
			apiKey:      "",
			psd:         map[string]any{},
			want:        "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := kiroUsageToken(tt.accessToken, tt.apiKey, tt.psd); got != tt.want {
				t.Errorf("kiroUsageToken(%q, %q, %v) = %q, want %q",
					tt.accessToken, tt.apiKey, tt.psd, got, tt.want)
			}
		})
	}
}

// An empty bearer always comes back as an auth rejection, which reads as "your
// token expired" and sends the user chasing a token problem that does not exist.
func TestFetchKiroUsage_NoCredentialIsNotAnAuthRejection(t *testing.T) {
	for _, token := range []string{"", "   "} {
		got := fetchKiroUsage(t.Context(), token, map[string]any{"authMethod": "builder-id"})
		if got.quotas == nil {
			t.Errorf("expected an empty quota map for token %q, got nil", token)
		}
		if got.message == "Kiro quota API rejected the current token. Chat may still work." {
			t.Errorf("token %q reported a rejected token, but nothing was sent", token)
		}
	}
}