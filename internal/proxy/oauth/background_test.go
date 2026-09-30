package oauth

import (
	"context"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/models"
)

func TestSelectConnectionsNeedingRefresh(t *testing.T) {
	now := time.Now()
	mk := func(id, authType, data string) *models.ProviderConnection {
		return &models.ProviderConnection{ID: id, Provider: "kiro", AuthType: authType, Data: data}
	}

	tests := []struct {
		name string
		conn *models.ProviderConnection
		want bool
	}{
		{
			name: "expiring oauth connection is due",
			conn: mk("c1", "oauth", `{"refreshToken":"rt","expiresAt":"`+now.Add(10*time.Minute).UTC().Format(time.RFC3339)+`"}`),
			want: true,
		},
		{
			name: "fresh oauth connection is not due",
			conn: mk("c2", "oauth", `{"refreshToken":"rt","expiresAt":"`+now.Add(2*time.Hour).UTC().Format(time.RFC3339)+`"}`),
			want: false,
		},
		{
			name: "apikey connection is not due",
			conn: mk("c3", "apikey", `{"apiKey":"sk","expiresAt":"`+now.Add(time.Minute).UTC().Format(time.RFC3339)+`"}`),
			want: false,
		},
		{
			name: "missing refresh token is not due",
			conn: mk("c4", "oauth", `{"expiresAt":"`+now.Add(time.Minute).UTC().Format(time.RFC3339)+`"}`),
			want: false,
		},
		{
			name: "missing expiry is not due",
			conn: mk("c5", "oauth", `{"refreshToken":"rt"}`),
			want: false,
		},
		{
			name: "parked connection is not retried on every tick",
			conn: mk("c6", "oauth", `{"refreshToken":"rt","expiresAt":"`+now.Add(time.Minute).UTC().Format(time.RFC3339)+`","oauthLockedUntil":"`+now.Add(20*time.Minute).UTC().Format(time.RFC3339)+`"}`),
			want: false,
		},
		{
			name: "expired park lets the connection back in",
			conn: mk("c7", "oauth", `{"refreshToken":"rt","expiresAt":"`+now.Add(time.Minute).UTC().Format(time.RFC3339)+`","oauthLockedUntil":"`+now.Add(-time.Minute).UTC().Format(time.RFC3339)+`"}`),
			want: true,
		},
		{
			name: "malformed park is not due-blocking",
			conn: mk("c8", "oauth", `{"refreshToken":"rt","expiresAt":"`+now.Add(time.Minute).UTC().Format(time.RFC3339)+`","oauthLockedUntil":"soon"}`),
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SelectConnectionsNeedingRefresh([]*models.ProviderConnection{tt.conn}, now)
			if (len(got) > 0) != tt.want {
				t.Errorf("due = %v, want %v", len(got) > 0, tt.want)
			}
		})
	}
}

func TestRefreshKiroRegistered(t *testing.T) {
	if Get("kiro") == nil {
		t.Fatal("kiro refresher not registered")
	}
}

func TestRefreshKiroRejectsInvalidRegion(t *testing.T) {
	_, err := refreshKiro(context.Background(), &Params{
		RefreshToken:         "rt",
		ProviderSpecificData: map[string]string{"clientId": "cid", "clientSecret": "cs", "region": "evil;region"},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid region") {
		t.Errorf("expected invalid region error, got %v", err)
	}
}
