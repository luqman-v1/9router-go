package chat

import (
	"context"
	"database/sql"

	"net/http"
	"testing"

	"9router/proxy/internal/db"
)

// A 400 is how an upstream rejects the caller's request, and many quote it back
// ("Unknown parameter: '<x>'", "Invalid value '<x>' for model"). Reading the
// credit phrase inside such an echo as a spent balance parked a healthy shared
// connection for an hour on every user's request, until the recovery tick
// cleared it. The lock now needs the upstream's own structured code on a 400
// (providers.ClassifyErrorForProvider / OmniRoute PR #20567).
func TestComboLock400CreditsNeedStructuredCode(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantLocked bool
		wantModel  string
	}{
		{
			name:       "400 echoing a credit phrase leaves the connection alone",
			status:     http.StatusBadRequest,
			body:       `{"error":{"message":"Unknown parameter: 'insufficient_quota'","type":"invalid_request_error"}}`,
			wantLocked: false,
		},
		{
			name:       "400 carrying a structured credit code parks it",
			status:     http.StatusBadRequest,
			body:       `{"error":{"code":"insufficient_quota","message":"You exceeded your current quota"}}`,
			wantLocked: true,
			wantModel:  "claude-sonnet-4-6",
		},
		{
			name:       "402 still parks on prose alone",
			status:     http.StatusPaymentRequired,
			body:       `{"error":{"message":"Your credit balance is too low to access the API"}}`,
			wantLocked: true,
			wantModel:  "claude-sonnet-4-6",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, repo, _, cleanup := setupCreditGateCombo(t)
			defer cleanup()

			var excludeIDs []string
			h.comboLockRetryable(context.Background(), &excludeIDs, "conn-cg", "deepseek", tt.wantModel, &upstreamError{
				StatusCode: tt.status,
				Body:       []byte(tt.body),
			})

			locked, err := repo.IsConnectionModelLocked("conn-cg", tt.wantModel)
			if err != nil {
				t.Fatalf("IsConnectionModelLocked: %v", err)
			}
			if locked != tt.wantLocked {
				t.Fatalf("model locked = %v, want %v (body: %s)", locked, tt.wantLocked, tt.body)
			}
			if tt.wantLocked && len(excludeIDs) != 1 {
				t.Errorf("excludeIDs = %v, want the connection excluded after a lock", excludeIDs)
			}
			if !tt.wantLocked && len(excludeIDs) != 0 {
				t.Errorf("excludeIDs = %v, want the connection still in rotation", excludeIDs)
			}
		})
	}
}

// The account-scoped cooldown is the other half of a park: a 400 that only
// echoes request text must not set rateLimitedUntil either, or the dashboard
// shows a healthy connection as cooling down.
func TestComboLock400CreditsLeaveAccountCooldownUnset(t *testing.T) {
	h, _, database, cleanup := setupCreditGateCombo(t)
	defer cleanup()

	var excludeIDs []string
	h.comboLockRetryable(context.Background(), &excludeIDs, "conn-cg", "deepseek", "claude-sonnet-4-6", &upstreamError{
		StatusCode: http.StatusBadRequest,
		Body:       []byte(`{"error":{"message":"quota exceeded for this request"}}`),
	})

	var raw string
	if err := database.QueryRow(`SELECT data FROM providerConnections WHERE id = 'conn-cg'`).Scan(&raw); err != nil {
		t.Fatalf("read connection row: %v", err)
	}
	if _, ok := db.ConnectionCooldownUntil(raw); ok {
		t.Error("rateLimitedUntil set for a 400 that only echoed request text")
	}
}

func setupCreditGateCombo(t *testing.T) (*ChatHandler, *db.Repo, *sql.DB, func()) {
	t.Helper()
	database, cleanup := setupChatTestDB(t)
	dropSeededConn(t, database)
	if _, err := database.Exec(`DELETE FROM providerConnections WHERE id = 'conn-2'`); err != nil {
		t.Fatalf("drop seeded groq connection: %v", err)
	}
	seedConnDB(t, database, "deepseek", "conn-cg", "sk-cg", "https://mock.example.com/cg")

	repo := db.NewRepo(database)
	return NewChatHandler(repo), repo, database, cleanup
}
