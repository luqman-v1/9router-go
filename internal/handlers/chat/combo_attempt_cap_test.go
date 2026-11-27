package chat

import (
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"9router/proxy/internal/db"
)

// The cap exists so one member's exhausted pool cannot grind every account
// while the client waits. The assertion that matters is the upstream call
// count: with a cap of 2 and four failing connections, the provider must be
// dialled twice and the next member must answer.
func TestComboMemberAttemptCapStopsGrindingThePool(t *testing.T) {
	tests := []struct {
		name        string
		cap         int
		wantCalls   int32
		description string
	}{
		{
			name:        "cap set",
			cap:         2,
			wantCalls:   2,
			description: "the member gives up after two failed accounts",
		},
		{
			name:        "cap off",
			cap:         0,
			wantCalls:   4,
			description: "the exhaustive walk still tries every account",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stalledCalls atomic.Int32
			stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				stalledCalls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				fmt.Fprint(w, `{"error":{"message":"rate limited","type":"rate_limit_error"}}`)
			}))
			defer stalled.Close()

			healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"second member answered"}}]}`))
			}))
			defer healthy.Close()

			database, cleanup := setupChatTestDB(t)
			defer cleanup()

			if _, err := database.Exec(`DELETE FROM providerConnections WHERE id IN ('conn-ds', 'conn-gq')`); err != nil {
				t.Fatal(err)
			}

			stalledData, _ := json.Marshal(map[string]any{"apiKey": "sk-test", "baseUrl": stalled.URL})
			healthyData, _ := json.Marshal(map[string]any{"apiKey": "sk-test", "baseUrl": healthy.URL})

			// Four accounts on the first member, one on the second: the pool
			// is what the cap is meant to stop from being walked to the end.
			if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
				('conn-ds-1', 'deepseek', 'apikey', 'Stalled 1', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z'),
				('conn-ds-2', 'deepseek', 'apikey', 'Stalled 2', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z'),
				('conn-ds-3', 'deepseek', 'apikey', 'Stalled 3', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z'),
				('conn-ds-4', 'deepseek', 'apikey', 'Stalled 4', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z'),
				('conn-gq', 'groq', 'apikey', 'Healthy', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
				string(stalledData), string(stalledData), string(stalledData), string(stalledData), string(healthyData)); err != nil {
				t.Fatalf("insert providerConnections: %v", err)
			}

			comboModels, _ := json.Marshal([]string{"deepseek/deepseek-chat", "groq/qwen3-32b"})
			if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES
				('c-cap', 'cap-combo', 'llm', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
				string(comboModels)); err != nil {
				t.Fatalf("seed combo: %v", err)
			}

			repo := db.NewRepo(database)
			if tt.cap > 0 {
				if err := repo.UpdateSettingsRaw(map[string]any{"comboMemberAttemptCap": tt.cap}); err != nil {
					t.Fatal(err)
				}
			}

			h := NewChatHandler(repo)
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
				strings.NewReader(`{"model":"cap-combo","messages":[{"role":"user","content":"hello"}]}`))
			h.HandleChatCompletions(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "second member answered") {
				t.Fatalf("body = %s, want the second member's answer", w.Body.String())
			}
			if got := stalledCalls.Load(); got != tt.wantCalls {
				t.Errorf("%s: stalled provider calls = %d, want %d", tt.description, got, tt.wantCalls)
			}
		})
	}
}

// A per-provider override must beat the global setting on the path the loops
// actually take, not only in the resolver's own unit test.
func TestComboMemberAttemptCapProviderOverrideWins(t *testing.T) {
	var stalledCalls atomic.Int32
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stalledCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"rate limited","type":"rate_limit_error"}}`)
	}))
	defer stalled.Close()

	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"second member answered"}}]}`))
	}))
	defer healthy.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	if _, err := database.Exec(`DELETE FROM providerConnections WHERE id IN ('conn-ds', 'conn-gq')`); err != nil {
		t.Fatal(err)
	}
	stalledData, _ := json.Marshal(map[string]any{"apiKey": "sk-test", "baseUrl": stalled.URL})
	healthyData, _ := json.Marshal(map[string]any{"apiKey": "sk-test", "baseUrl": healthy.URL})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-ds-1', 'deepseek', 'apikey', 'Stalled 1', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z'),
		('conn-ds-2', 'deepseek', 'apikey', 'Stalled 2', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z'),
		('conn-ds-3', 'deepseek', 'apikey', 'Stalled 3', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z'),
		('conn-gq', 'groq', 'apikey', 'Healthy', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(stalledData), string(stalledData), string(stalledData), string(healthyData)); err != nil {
		t.Fatalf("insert providerConnections: %v", err)
	}
	comboModels, _ := json.Marshal([]string{"deepseek/deepseek-chat", "groq/qwen3-32b"})
	if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES
		('c-ov', 'ov-combo', 'llm', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(comboModels)); err != nil {
		t.Fatalf("seed combo: %v", err)
	}

	repo := db.NewRepo(database)
	// Global cap of 3 would allow three dials; the provider's own cap of 1
	// must win.
	if err := repo.UpdateSettingsRaw(map[string]any{
		"comboMemberAttemptCap": 3,
		"providerOverrides":     map[string]any{"deepseek": map[string]any{"comboMemberAttemptCap": 1}},
	}); err != nil {
		t.Fatal(err)
	}

	h := NewChatHandler(repo)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"ov-combo","messages":[{"role":"user","content":"hello"}]}`))
	h.HandleChatCompletions(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	if got := stalledCalls.Load(); got != 1 {
		t.Errorf("stalled provider calls = %d, want 1 (the provider override)", got)
	}
}

// The cap is applied in two loops, and the review of #1525 was right that only
// the OpenAI one was tested: mutating the break in combo.go's Messages loop left
// this suite green. This subtest drives /v1/messages, which is the only way to
// reach handleMessagesComboFallbackWithVisited, and asserts the same call count.
//
// The two loops are separate copies of the same walk (one over an OpenAI body,
// one over a translated Claude body), so a change to one is not a change to the
// other and both need their own proof.
func TestComboMemberAttemptCapStopsGrindingThePoolForMessages(t *testing.T) {
	tests := []struct {
		name        string
		cap         int
		wantCalls   int32
		description string
	}{
		{
			name:        "cap set",
			cap:         2,
			wantCalls:   2,
			description: "the member gives up after two failed accounts",
		},
		{
			name:        "cap off",
			cap:         0,
			wantCalls:   4,
			description: "the exhaustive walk still tries every account",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stalledCalls atomic.Int32
			stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				stalledCalls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				fmt.Fprint(w, `{"error":{"message":"rate limited","type":"rate_limit_error"}}`)
			}))
			defer stalled.Close()

			healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"second member answered"}}]}`))
			}))
			defer healthy.Close()

			database, cleanup := setupChatTestDB(t)
			defer cleanup()

			if _, err := database.Exec(`DELETE FROM providerConnections WHERE id IN ('conn-ds', 'conn-gq')`); err != nil {
				t.Fatal(err)
			}
			stalledData, _ := json.Marshal(map[string]any{"apiKey": "sk-test", "baseUrl": stalled.URL})
			healthyData, _ := json.Marshal(map[string]any{"apiKey": "sk-test", "baseUrl": healthy.URL})
			if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
				('conn-ds-1', 'deepseek', 'apikey', 'Stalled 1', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z'),
				('conn-ds-2', 'deepseek', 'apikey', 'Stalled 2', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z'),
				('conn-ds-3', 'deepseek', 'apikey', 'Stalled 3', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z'),
				('conn-ds-4', 'deepseek', 'apikey', 'Stalled 4', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z'),
				('conn-gq', 'groq', 'apikey', 'Healthy', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
				string(stalledData), string(stalledData), string(stalledData), string(stalledData), string(healthyData)); err != nil {
				t.Fatalf("insert providerConnections: %v", err)
			}

			comboModels, _ := json.Marshal([]string{"deepseek/deepseek-chat", "groq/qwen3-32b"})
			if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES
				('c-cap-msg', 'cap-combo-msg', 'llm', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
				string(comboModels)); err != nil {
				t.Fatalf("seed combo: %v", err)
			}

			repo := db.NewRepo(database)
			if tt.cap > 0 {
				if err := repo.UpdateSettingsRaw(map[string]any{"comboMemberAttemptCap": tt.cap}); err != nil {
					t.Fatal(err)
				}
			}

			h := NewChatHandler(repo)
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/messages",
				strings.NewReader(`{"model":"cap-combo-msg","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`))
			h.HandleMessages(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "second member answered") {
				t.Fatalf("body = %s, want the second member's answer", w.Body.String())
			}
			if got := stalledCalls.Load(); got != tt.wantCalls {
				t.Errorf("%s: stalled provider calls = %d, want %d", tt.description, got, tt.wantCalls)
			}
		})
	}
}
