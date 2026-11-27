package chat

import (
	"context"
	json "encoding/json/v2"
	"net/http"
	"testing"

	"9router/proxy/internal/db"
)

// recordModelCooldown is the one place a model-level cooldown badge is written,
// so it is the only place that has to know a refusal the gateway produced
// itself never exercised the account. Without the Local guard a 504 from the
// end-to-end deadline parked a healthy connection (#1527 review).
func TestRecordModelCooldownSkipsLocalRefusals(t *testing.T) {
	tests := []struct {
		name string
		ue   *upstreamError
		want bool // whether the connection should be locked
	}{
		{
			name: "gateway fault locks",
			ue:   &upstreamError{StatusCode: http.StatusGatewayTimeout, Body: []byte(`{}`)},
			want: true,
		},
		{
			name: "local refusal does not lock",
			ue:   &upstreamError{StatusCode: http.StatusGatewayTimeout, Body: []byte(`{}`), Local: true},
			want: false,
		},
		{
			name: "local queue refusal does not lock",
			ue:   &upstreamError{StatusCode: http.StatusServiceUnavailable, Body: []byte(`{}`), Local: true},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database, cleanup := setupChatTestDB(t)
			defer cleanup()

			if _, err := database.Exec(`DELETE FROM providerConnections WHERE id IN ('conn-1', 'conn-2')`); err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(map[string]any{"apiKey": "sk-test"})
			if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
				('conn-1', 'deepseek', 'apikey', 'Subject', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
				string(data)); err != nil {
				t.Fatalf("insert providerConnections: %v", err)
			}

			repo := db.NewRepo(database)
			h := NewChatHandler(repo)
			h.recordModelCooldown(context.Background(), "deepseek", "deepseek-chat", "conn-1", tt.ue)

			var raw string
			if err := database.QueryRow(`SELECT data FROM providerConnections WHERE id = 'conn-1'`).Scan(&raw); err != nil {
				t.Fatalf("read connection: %v", err)
			}
			if got := db.ConnectionModelLockedData(raw, "deepseek-chat"); got != tt.want {
				t.Fatalf("locked = %v, want %v (data: %s)", got, tt.want, raw)
			}
		})
	}
}
