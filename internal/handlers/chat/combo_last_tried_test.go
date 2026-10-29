package chat

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"9router/proxy/internal/db"
)

// TestComboTimeoutSparesLastTriedCandidate pins that the combo response timeout
// only ever ADVANCES a combo, never makes one fail sooner (#776).
//
// The loop skips an entry that does not resolve and a provider whose model is
// locked before forwarding anything, so `i == len(models)-1` is not the last
// candidate actually tried. Keying the first-token timer to that index left the
// timer armed on the last member that could still answer, and the client got a
// 504 where the same combo with the timeout off answered 200.
//
// Fixture: three members, the third model-locked, the second slow but healthy.
//
//	deepseek/deepseek-chat  500 at once   -> failover
//	groq/qwen3-32b          answers after slowDelay  <- the last one actually tried
//	xai/grok-2              model-locked, skipped by the loop
func TestComboTimeoutSparesLastTriedCandidate(t *testing.T) {
	const slowDelay = 300 * time.Millisecond

	tests := []struct {
		name      string
		timeoutMs int
		wantCode  int
		wantBody  string
	}{
		{
			name:      "timeout armed spares the last tried candidate",
			timeoutMs: 100,
			wantCode:  http.StatusOK,
			wantBody:  "slow but fine",
		},
		{
			name:      "timeout disabled answers the same",
			timeoutMs: 0,
			wantCode:  http.StatusOK,
			wantBody:  "slow but fine",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var slowCalls atomic.Int32
			fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":{"message":"boom","type":"server_error"}}`))
			}))
			defer fast.Close()
			slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				slowCalls.Add(1)
				time.Sleep(slowDelay)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"slow but fine"}}]}`))
			}))
			defer slow.Close()

			repo := seedComboLastEntryLocked(t, "combo-last-tried-"+fmt.Sprint(tt.timeoutMs), fast.URL, slow.URL)
			if tt.timeoutMs > 0 {
				name := "combo-last-tried-" + fmt.Sprint(tt.timeoutMs)
				if err := repo.UpdateSettingsRaw(map[string]any{
					"comboStrategies": map[string]any{
						name: map[string]any{"firstTokenTimeoutMs": tt.timeoutMs},
					},
				}); err != nil {
					t.Fatalf("set firstTokenTimeoutMs: %v", err)
				}
			}

			h := NewChatHandler(repo)
			req := httptest.NewRequest(http.MethodPost, "/chat/completions",
				strings.NewReader(`{"model":"combo-last-tried-`+fmt.Sprint(tt.timeoutMs)+`","messages":[{"role":"user","content":"hi"}],"stream":false}`))
			rec := httptest.NewRecorder()
			h.HandleChatCompletions(rec, req)

			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.wantCode, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("body = %s, want it to contain %q", rec.Body.String(), tt.wantBody)
			}
			if got := slowCalls.Load(); got != 1 {
				t.Errorf("last tried candidate received %d requests, want 1", got)
			}
		})
	}
}

// seedComboLastEntryLocked builds the #776 fixture: a three-member combo whose
// final entry belongs to a provider whose only connection is model-locked, so
// the loop skips that entry entirely and the second member is the last one it
// actually tries.
func seedComboLastEntryLocked(t *testing.T, comboName, fastURL, slowURL string) *db.Repo {
	t.Helper()
	database, cleanup := setupChatTestDB(t)
	t.Cleanup(cleanup)

	if _, err := database.Exec(`DELETE FROM providerConnections WHERE id IN ('conn-1', 'conn-2')`); err != nil {
		t.Fatalf("clear seeded connections: %v", err)
	}
	insert := func(id, provider, name, baseURL string) {
		t.Helper()
		data, _ := json.Marshal(map[string]any{"apiKey": "sk-mock", "baseUrl": baseURL})
		if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
			(?, ?, 'apikey', ?, 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
			id, provider, name, string(data)); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	insert("mock-fast", "deepseek", "Mock Fast", fastURL)
	insert("mock-slow", "groq", "Mock Slow", slowURL)
	insert("mock-locked", "xai", "Mock Locked", "http://127.0.0.1:1")

	repo := db.NewRepo(database)
	// The final member's only connection is locked for its model, so the loop
	// skips the entry before forwarding anything.
	if err := repo.LockConnectionModel("mock-locked", "grok-2", 600, 1); err != nil {
		t.Fatalf("lock final member: %v", err)
	}

	models, _ := json.Marshal([]string{"deepseek/deepseek-chat", "groq/qwen3-32b", "xai/grok-2"})
	if _, err := database.Exec(`INSERT INTO combos (id, name, models, createdAt, updatedAt) VALUES
		(?, ?, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		"combo-"+comboName, comboName, string(models)); err != nil {
		t.Fatalf("insert combo: %v", err)
	}
	return repo
}

// TestComboTimeoutTracksMembersThePassPutsOutOfReach covers the two residual
// shapes the precomputed index could not see. Both leave the timer armed on the
// one member that really sends the last request, so the client got a 504 where
// the same combo with the timeout off answered 200 (#776).
//
// A. A later member on a provider whose only connection the pass just locked:
//
//	the deepseek 429 puts its connection in excludeIDs, so the third entry is
//	unreachable by the time the loop reaches it and the second is last.
//
// B. A later member whose provider has no connection at all: the loop's
//
//	connection lookup fails on it, so it is not the last one tried.
func TestComboTimeoutTracksMembersThePassPutsOutOfReach(t *testing.T) {
	const slowDelay = 300 * time.Millisecond

	tests := []struct {
		name   string
		models []string
		seed   func(t *testing.T, database *sql.DB, slowURL string)
	}{
		{
			name:   "A: later member on a provider the pass locked",
			models: []string{"deepseek/deepseek-chat", "groq/qwen3-32b", "deepseek/deepseek-reasoner"},
		},
		{
			name:   "B: later member whose provider has no connection",
			models: []string{"deepseek/deepseek-chat", "groq/qwen3-32b", "openai/gpt-4o"},
		},
	}

	for _, tt := range tests {
		for _, timeoutMs := range []int{100, 0} {
			t.Run(tt.name+"/timeout="+fmt.Sprint(timeoutMs), func(t *testing.T) {
				var slowCalls atomic.Int32
				limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = w.Write([]byte(`{"error":{"message":"quota","type":"rate_limit_error"}}`))
				}))
				defer limited.Close()
				slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					slowCalls.Add(1)
					time.Sleep(slowDelay)
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"slow but fine"}}]}`))
				}))
				defer slow.Close()

				comboName := "combo-reach-" + fmt.Sprint(len(tt.models)) + "-" + fmt.Sprint(timeoutMs) + "-" + strings.ReplaceAll(tt.name[:1], " ", "")
				repo := seedComboReach(t, comboName, tt.models, limited.URL, slow.URL)
				if timeoutMs > 0 {
					if err := repo.UpdateSettingsRaw(map[string]any{
						"comboStrategies": map[string]any{
							comboName: map[string]any{"firstTokenTimeoutMs": timeoutMs, "respectStoredOrder": true},
						},
					}); err != nil {
						t.Fatalf("set firstTokenTimeoutMs: %v", err)
					}
				}

				h := NewChatHandler(repo)
				req := httptest.NewRequest(http.MethodPost, "/chat/completions",
					strings.NewReader(`{"model":"`+comboName+`","messages":[{"role":"user","content":"hi"}],"stream":false}`))
				rec := httptest.NewRecorder()
				h.HandleChatCompletions(rec, req)

				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
				}
				if !strings.Contains(rec.Body.String(), "slow but fine") {
					t.Errorf("body = %s, want the slow healthy member's answer", rec.Body.String())
				}
				if got := slowCalls.Load(); got != 1 {
					t.Errorf("the last member that could answer received %d requests, want 1", got)
				}
			})
		}
	}
}

// seedComboReach builds a combo over the given models with one deepseek
// connection that answers 429 and one groq connection that answers slowly. A
// provider named in the combo with no seeded connection exercises the
// no-connection shape.
func seedComboReach(t *testing.T, comboName string, models []string, limitedURL, slowURL string) *db.Repo {
	t.Helper()
	database, cleanup := setupChatTestDB(t)
	t.Cleanup(cleanup)

	if _, err := database.Exec(`DELETE FROM providerConnections WHERE id IN ('conn-1', 'conn-2')`); err != nil {
		t.Fatalf("clear seeded connections: %v", err)
	}
	insert := func(id, provider, name, baseURL string) {
		t.Helper()
		data, _ := json.Marshal(map[string]any{"apiKey": "sk-mock", "baseUrl": baseURL})
		if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
			(?, ?, 'apikey', ?, 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
			id, provider, name, string(data)); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	insert("mock-limited", "deepseek", "Mock Limited", limitedURL)
	insert("mock-slow", "groq", "Mock Slow", slowURL)

	encoded, _ := json.Marshal(models)
	if _, err := database.Exec(`INSERT INTO combos (id, name, models, createdAt, updatedAt) VALUES
		(?, ?, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		"combo-"+comboName, comboName, string(encoded)); err != nil {
		t.Fatalf("insert combo: %v", err)
	}
	return db.NewRepo(database)
}
