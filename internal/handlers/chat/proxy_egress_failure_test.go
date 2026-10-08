package chat

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

// A pool that is assigned but cannot serve traffic fails the request before a
// byte leaves the process. That refusal is invisible on the Usage & Analytics
// dashboard unless it is recorded, and upstream records it: the same condition
// throws out of proxyFetch ("Proxy required but failed") from inside
// executor.execute, so chatCore catches it and writes both the requestDetails
// error row and the isError pending-tracking update. Without a row the client
// sees a 502 nobody can diagnose from the dashboard.
//
// The pool id is user-supplied and lands inside the error body, so the body must
// be marshalled: a quote in it used to produce a payload that no longer parsed,
// and every reader then reported the useless "request failed" instead of why the
// egress refused.
func TestProxyFailClosed_RecordsDiagnosticWithoutUsage(t *testing.T) {
	tests := []struct {
		name     string
		poolID   string
		poolJSON string
		isActive int
		wantMsg  string
	}{
		{
			name:     "inactive pool",
			poolID:   "pool-inactive",
			poolJSON: `{"name":"inactive pool","proxyUrl":"http://p1:8080"}`,
			isActive: 0,
			wantMsg:  "pool-inactive",
		},
		{
			name:     "quote in the pool id still yields a readable reason",
			poolID:   `pool-"quoted"`,
			poolJSON: `{"name":"weird pool","proxyUrl":"http://p1:8080"}`,
			isActive: 0,
			wantMsg:  "quoted",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database, cleanup := setupChatTestDB(t)
			defer cleanup()

			if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS proxyPools (
				id TEXT PRIMARY KEY,
				isActive INTEGER DEFAULT 1,
				testStatus TEXT,
				data TEXT NOT NULL,
				createdAt TEXT NOT NULL,
				updatedAt TEXT NOT NULL
			)`); err != nil {
				t.Fatalf("create proxyPools: %v", err)
			}
			if _, err := database.Exec(`INSERT INTO proxyPools (id, isActive, testStatus, data, createdAt, updatedAt)
				VALUES (?, ?, 'unknown', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
				tt.poolID, tt.isActive, tt.poolJSON); err != nil {
				t.Fatalf("insert pool: %v", err)
			}
			seedConnDB(t, database, "deepseek", "conn-pool", "sk-x", "https://api.deepseek.com")
			if _, err := database.Exec(`UPDATE providerConnections
				SET data = json_set(data, '$.proxyPoolId', ?) WHERE id = 'conn-pool'`, tt.poolID); err != nil {
				t.Fatalf("bind pool to connection: %v", err)
			}

			handler := NewChatHandler(db.NewRepo(database))
			rec := httptest.NewRecorder()
			err := handler.handleAccountFallback(context.Background(), rec, "deepseek", "deepseek-chat", "conn-pool",
				[]byte(`{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}`), false, false, "/v1/chat/completions")
			if err == nil {
				t.Fatal("expected the unusable pool to fail the request closed")
			}

			// The error body the client receives must be valid JSON that names
			// the pool. Decoding it is the assertion: a body that only looks
			// right in a log line is exactly the bug.
			var ue *upstreamError
			if !errors.As(err, &ue) {
				t.Fatalf("error = %T (%v), want an upstreamError carrying the body", err, err)
			}
			var wire struct {
				Error struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if jsonErr := json.Unmarshal(ue.Body, &wire); jsonErr != nil {
				t.Fatalf("client-facing body is not valid JSON (%v): %s", jsonErr, ue.Body)
			}
			if wire.Error.Type != "proxy_error" {
				t.Errorf("error type = %q, want proxy_error", wire.Error.Type)
			}
			if !strings.Contains(wire.Error.Message, tt.wantMsg) {
				t.Errorf("message %q does not mention %q", wire.Error.Message, tt.wantMsg)
			}

			var usageRows, detailRows int
			if err := database.QueryRow(`SELECT COUNT(*) FROM usageHistory`).Scan(&usageRows); err != nil {
				t.Fatalf("count usageHistory: %v", err)
			}
			if err := database.QueryRow(`SELECT COUNT(*) FROM requestDetails WHERE status = 'error'`).Scan(&detailRows); err != nil {
				t.Fatalf("count requestDetails: %v", err)
			}
			if detailRows != 1 {
				t.Fatalf("requestDetails error rows = %d, want 1: a refusal the dashboard cannot show is a silent failure", detailRows)
			}
			if usageRows != 0 {
				t.Errorf("usageHistory rows = %d, want 0: a refusal serves no tokens", usageRows)
			}

			var raw string
			if err := database.QueryRow(`SELECT data FROM requestDetails WHERE status = 'error'`).Scan(&raw); err != nil {
				t.Fatalf("read detail row: %v", err)
			}
			var detail struct {
				Response struct {
					Error  string `json:"error"`
					Status int    `json:"status"`
				} `json:"response"`
			}
			if jsonErr := json.Unmarshal([]byte(raw), &detail); jsonErr != nil {
				t.Fatalf("stored detail is not valid JSON (%v): %s", jsonErr, raw)
			}
			// The stored diagnostic must carry the reason, not the generic
			// "request failed" an unparseable body degrades to.
			if !strings.Contains(detail.Response.Error, tt.wantMsg) {
				t.Errorf("detail row lost the reason: %q", detail.Response.Error)
			}
			if detail.Response.Status != http.StatusBadGateway {
				t.Errorf("detail status = %d, want 502", detail.Response.Status)
			}
		})
	}
}
