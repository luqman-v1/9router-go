package chat

import (
	"strings"
	"testing"
)

func TestGetProviderConfigCloudflareAccountID(t *testing.T) {
	tests := []struct {
		name        string
		accountID   string
		envOverride string
		wantURL     string
		wantErr     bool
		errContains string
	}{
		{
			name:      "resolves accountId from providerSpecificData",
			accountID: "abc123def456",
			wantURL:   "https://api.cloudflare.com/client/v4/accounts/abc123def456/ai/v1/chat/completions",
			wantErr:   false,
		},
		{
			name:        "falls back to CLOUDFLARE_ACCOUNT_ID env var",
			accountID:   "",
			envOverride: "envfallback789",
			wantURL:     "https://api.cloudflare.com/client/v4/accounts/envfallback789/ai/v1/chat/completions",
			wantErr:     false,
		},
		{
			name:        "errors when both accountId and env are absent",
			accountID:   "",
			envOverride: "",
			wantErr:     true,
			errContains: "requires accountId",
		},
		{
			name:        "empty string accountId falls back to env",
			accountID:   "  ",
			envOverride: "trimmed789",
			wantURL:     "https://api.cloudflare.com/client/v4/accounts/trimmed789/ai/v1/chat/completions",
			wantErr:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CLOUDFLARE_ACCOUNT_ID", tt.envOverride)

			connData := &ConnectionData{}
			if tt.accountID != "" {
				connData.ProviderSpecificData = map[string]any{"accountId": strings.TrimSpace(tt.accountID)}
			}

			handler := &ChatHandler{}
			cfg, err := handler.getProviderConfig("cloudflare-ai", connData)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("wanted error containing %q, got nil", tt.errContains)
				}
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error %q does not contain %q", err.Error(), tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg == nil {
				t.Fatal("cfg is nil")
			}
			if cfg.BaseURL != tt.wantURL {
				t.Errorf("BaseURL = %q, want %q", cfg.BaseURL, tt.wantURL)
			}
		})
	}
}
