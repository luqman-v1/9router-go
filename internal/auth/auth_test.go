package auth

import (
	"context"
	"testing"
	"time"

	"9router/proxy/internal/handlerutil"
)

func TestGetString(t *testing.T) {
	tests := []struct {
		name string
		m    map[string]any
		key  string
		want string
	}{
		{name: "string value present", m: map[string]any{"k": "v"}, key: "k", want: "v"},
		{name: "missing key", m: map[string]any{}, key: "k", want: ""},
		{name: "nil value", m: map[string]any{"k": nil}, key: "k", want: ""},
		{name: "int value", m: map[string]any{"k": 42}, key: "k", want: ""},
		{name: "bool value", m: map[string]any{"k": true}, key: "k", want: ""},
		{name: "float64 value", m: map[string]any{"k": 3.14}, key: "k", want: ""},
		{name: "empty string value", m: map[string]any{"k": ""}, key: "k", want: ""},
		{name: "nil map (no panic)", m: nil, key: "k", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := handlerutil.GetString(tt.m, tt.key)
			if got != tt.want {
				t.Errorf("getString() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGetInt(t *testing.T) {
	tests := []struct {
		name string
		m    map[string]any
		key  string
		want int
	}{
		{name: "float64 value (JSON number)", m: map[string]any{"k": float64(42)}, key: "k", want: 42},
		{name: "int value (Go native)", m: map[string]any{"k": int(42)}, key: "k", want: 42},
		{name: "negative float64", m: map[string]any{"k": float64(-5)}, key: "k", want: -5},
		{name: "truncated float64", m: map[string]any{"k": 3.99}, key: "k", want: 3},
		{name: "zero float64", m: map[string]any{"k": float64(0)}, key: "k", want: 0},
		{name: "missing key", m: map[string]any{}, key: "k", want: 0},
		{name: "nil value", m: map[string]any{"k": nil}, key: "k", want: 0},
		{name: "string value", m: map[string]any{"k": "42"}, key: "k", want: 0},
		{name: "bool value", m: map[string]any{"k": true}, key: "k", want: 0},
		{name: "nil map (no panic)", m: nil, key: "k", want: 0},
		{name: "large float64", m: map[string]any{"k": float64(9999999999)}, key: "k", want: 9999999999},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getInt(tt.m, tt.key)
			if got != tt.want {
				t.Errorf("getInt() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestParseTokenFromConnection(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		want    *TokenInfo
		wantErr bool
	}{
		{
			name: "valid JSON with all fields",
			data: `{"accessToken":"at1","refreshToken":"rt1","expiresAt":"2026-01-01T00:00:00Z","tokenType":"bearer","scope":"read"}`,
			want: &TokenInfo{
				AccessToken:  "at1",
				RefreshToken: "rt1",
				ExpiresAt:    "2026-01-01T00:00:00Z",
				TokenType:    "bearer",
				Scope:        "read",
			},
			wantErr: false,
		},
		{
			name: "valid JSON with partial fields",
			data: `{"accessToken":"at1"}`,
			want: &TokenInfo{AccessToken: "at1"},
			wantErr: false,
		},
		{
			name:    "empty JSON object",
			data:    `{}`,
			want:    &TokenInfo{},
			wantErr: false,
		},
		{
			name:    "malformed JSON",
			data:    `not-json`,
			want:    nil,
			wantErr: true,
		},
		{
			name:    "empty string",
			data:    "",
			want:    nil,
			wantErr: true,
		},
		{
			name: "null field values",
			data: `{"accessToken":null,"refreshToken":null,"scope":null}`,
			want:    &TokenInfo{},
			wantErr: false,
		},
		{
			name: "numeric field values treated as missing",
			data: `{"accessToken":123,"refreshToken":456}`,
			want:    &TokenInfo{},
			wantErr: false,
		},
		{
			name: "extra unknown fields ignored",
			data: `{"accessToken":"at1","unknownField":"val","nested":{"a":1}}`,
			want:    &TokenInfo{AccessToken: "at1"},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseTokenFromConnection(tt.data)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseTokenFromConnection() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got.AccessToken != tt.want.AccessToken {
				t.Errorf("AccessToken = %q, want %q", got.AccessToken, tt.want.AccessToken)
			}
			if got.RefreshToken != tt.want.RefreshToken {
				t.Errorf("RefreshToken = %q, want %q", got.RefreshToken, tt.want.RefreshToken)
			}
			if got.ExpiresAt != tt.want.ExpiresAt {
				t.Errorf("ExpiresAt = %q, want %q", got.ExpiresAt, tt.want.ExpiresAt)
			}
			if got.TokenType != tt.want.TokenType {
				t.Errorf("TokenType = %q, want %q", got.TokenType, tt.want.TokenType)
			}
			if got.Scope != tt.want.Scope {
				t.Errorf("Scope = %q, want %q", got.Scope, tt.want.Scope)
			}
		})
	}
}

func TestParseProviderSpecificData(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		want    *ProviderSpecificData
		wantErr bool
	}{
		{
			name: "valid provider specific data",
			data: `{"providerSpecificData":{"clientId":"cid1","clientSecret":"cs1","tokenEndpoint":"https://example.com/token","scope":"read"}}`,
			want: &ProviderSpecificData{
				ClientID:     "cid1",
				ClientSecret: "cs1",
				TokenURL:     "https://example.com/token",
				Scope:        "read",
			},
			wantErr: false,
		},
		{
			name: "partial provider specific data",
			data: `{"providerSpecificData":{"clientId":"cid1"}}`,
			want: &ProviderSpecificData{
				ClientID: "cid1",
			},
			wantErr: false,
		},
		{
			name: "empty provider specific data object",
			data: `{"providerSpecificData":{}}`,
			want:    &ProviderSpecificData{},
			wantErr: false,
		},
		{
			name:    "missing providerSpecificData key",
			data:    `{}`,
			want:    nil,
			wantErr: true,
		},
		{
			name:    "providerSpecificData is string instead of object",
			data:    `{"providerSpecificData":"not-an-object"}`,
			want:    nil,
			wantErr: true,
		},
		{
			name:    "malformed JSON",
			data:    `not-json`,
			want:    nil,
			wantErr: true,
		},
		{
			name:    "empty string",
			data:    "",
			want:    nil,
			wantErr: true,
		},
		{
			name: "null field values in provider specific data",
			data: `{"providerSpecificData":{"clientId":null,"clientSecret":null}}`,
			want:    &ProviderSpecificData{},
			wantErr: false,
		},
		{
			name: "extra unknown fields in provider specific data",
			data: `{"providerSpecificData":{"clientId":"cid1","extra":true}}`,
			want:    &ProviderSpecificData{ClientID: "cid1"},
			wantErr: false,
		},
		{
			name: "top-level extra fields ignored",
			data: `{"providerSpecificData":{"clientId":"cid1"},"otherField":"val"}`,
			want:    &ProviderSpecificData{ClientID: "cid1"},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseProviderSpecificData(tt.data)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseProviderSpecificData() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got.ClientID != tt.want.ClientID {
				t.Errorf("ClientID = %q, want %q", got.ClientID, tt.want.ClientID)
			}
			if got.ClientSecret != tt.want.ClientSecret {
				t.Errorf("ClientSecret = %q, want %q", got.ClientSecret, tt.want.ClientSecret)
			}
			if got.TokenURL != tt.want.TokenURL {
				t.Errorf("TokenURL = %q, want %q", got.TokenURL, tt.want.TokenURL)
			}
			if got.Scope != tt.want.Scope {
				t.Errorf("Scope = %q, want %q", got.Scope, tt.want.Scope)
			}
		})
	}
}

func TestIsTokenExpired(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name  string
		token *TokenInfo
		want  bool
	}{
		{
			name:  "empty expires at",
			token: &TokenInfo{},
			want:  false,
		},
		{
			name:  "invalid date format",
			token: &TokenInfo{ExpiresAt: "not-a-date"},
			want:  true,
		},
		{
			name:  "expired one hour ago",
			token: &TokenInfo{ExpiresAt: now.Add(-1 * time.Hour).Format(time.RFC3339)},
			want:  true,
		},
		{
			name:  "expired one second ago",
			token: &TokenInfo{ExpiresAt: now.Add(-1 * time.Second).Format(time.RFC3339)},
			want:  true,
		},
		{
			name:  "within 5min buffer (1 min from now)",
			token: &TokenInfo{ExpiresAt: now.Add(1 * time.Minute).Format(time.RFC3339)},
			want:  true,
		},
		{
			name:  "beyond 5min buffer (10 min from now)",
			token: &TokenInfo{ExpiresAt: now.Add(10 * time.Minute).Format(time.RFC3339)},
			want:  false,
		},
		{
			name:  "far in the future",
			token: &TokenInfo{ExpiresAt: now.Add(24 * time.Hour).Format(time.RFC3339)},
			want:  false,
		},
		{
			name:  "exactly now (considered expired with buffer)",
			token: &TokenInfo{ExpiresAt: now.Format(time.RFC3339)},
			want:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsTokenExpired(tt.token)
			if got != tt.want {
				t.Errorf("IsTokenExpired() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRefreshToken_preHttpErrors(t *testing.T) {
	tests := []struct {
		name  string
		token *TokenInfo
		psd   *ProviderSpecificData
	}{
		{
			name:  "no refresh token available",
			token: &TokenInfo{RefreshToken: ""},
			psd:   &ProviderSpecificData{TokenURL: "http://example.com/token"},
		},
		{
			name:  "no token endpoint configured",
			token: &TokenInfo{RefreshToken: "rt1"},
			psd:   &ProviderSpecificData{TokenURL: ""},
		},
		{
			name:  "both empty",
			token: &TokenInfo{RefreshToken: ""},
			psd:   &ProviderSpecificData{TokenURL: ""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RefreshToken(context.Background(), tt.token, tt.psd)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if got != nil {
				t.Fatalf("expected nil token on error, got %+v", got)
			}
		})
	}
}

func TestRefreshToken_httpCallFails(t *testing.T) {
	token := &TokenInfo{
		AccessToken:  "old-at",
		RefreshToken: "rt1",
		ExpiresAt:    "2026-01-01T00:00:00Z",
		TokenType:    "bearer",
		Scope:        "read",
	}
	psd := &ProviderSpecificData{
		ClientID:     "cid1",
		ClientSecret: "cs1",
		TokenURL:     "http://127.0.0.1:1/refresh",
		Scope:        "read",
	}

	got, err := RefreshToken(context.Background(), token, psd)
	if err == nil {
		t.Fatal("expected error from HTTP call to unused port, got nil")
	}
	if got != nil {
		t.Fatalf("expected nil token on HTTP error, got %+v", got)
	}
}

func TestRefreshToken_invalidUrl(t *testing.T) {
	token := &TokenInfo{
		RefreshToken: "rt1",
	}
	psd := &ProviderSpecificData{
		TokenURL: "://invalid-url",
	}

	got, err := RefreshToken(context.Background(), token, psd)
	if err == nil {
		t.Fatal("expected error from invalid URL, got nil")
	}
	if got != nil {
		t.Fatalf("expected nil token on error, got %+v", got)
	}
}
