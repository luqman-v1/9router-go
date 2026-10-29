package chat

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/db"
)

const customJSONProvider = "openai-compatible-custom-json-test"

func seedCustomJSONConn(t *testing.T, database *sql.DB, connID, apiKey, baseURL string, customJSON map[string]any) {
	t.Helper()
	data, _ := json.Marshal(map[string]any{
		"apiKey": apiKey,
		"providerSpecificData": map[string]any{
			"apiType": "custom-json", "baseUrl": baseURL, "prefix": "custom", "customJson": customJSON,
		},
	})
	q := `INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES (?, ?, 'apikey', ?, 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`
	if _, err := database.Exec(q, connID, customJSONProvider, connID, string(data)); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
}

func newCustomJSONHandler(t *testing.T, status int, reply string) (*ChatHandler, *sql.DB, *atomic.Int32, string) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	database, cleanup := setupChatTestDB(t)
	t.Cleanup(cleanup)
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("clear seeded connections: %v", err)
	}
	return NewChatHandler(db.NewRepo(database)), database, &calls, srv.URL + "/business"
}

// A business endpoint that fails may already have performed its operation, so
// the router must not replay it on the provider's next account.
func TestCustomJSON_FailedAttemptIsNotReplayedOnAnotherAccount(t *testing.T) {
	h, database, calls, endpoint := newCustomJSONHandler(t, http.StatusBadGateway, `{"error":"business failure"}`)
	cfg := map[string]any{"responsePath": "answer"}
	seedCustomJSONConn(t, database, "conn-a", "key-a", endpoint, cfg)
	seedCustomJSONConn(t, database, "conn-b", "key-b", endpoint, cfg)

	body := []byte(`{"model":"custom-model","messages":[{"role":"user","content":"hi"}]}`)
	err := h.handleAccountFallback(context.Background(), httptest.NewRecorder(), customJSONProvider, "custom-model", "", body, false, false, "/v1/chat/completions")
	var ue *upstreamError
	if !errors.As(err, &ue) || ue.StatusCode != http.StatusBadGateway {
		t.Fatalf("err = %v, want the upstream 502", err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("business endpoint called %d times, want exactly 1", n)
	}
}

func TestCustomJSON_NoAuthConnectionServesWithoutKey(t *testing.T) {
	h, database, calls, endpoint := newCustomJSONHandler(t, http.StatusOK, `{"answer":"pong"}`)
	seedCustomJSONConn(t, database, "conn-a", "", endpoint, map[string]any{"authType": "none", "responsePath": "answer"})

	rec := httptest.NewRecorder()
	body := []byte(`{"model":"custom-model","messages":[{"role":"user","content":"ping"}]}`)
	if err := h.handleAccountFallback(context.Background(), rec, customJSONProvider, "custom-model", "", body, false, false, "/v1/chat/completions"); err != nil {
		t.Fatalf("handleAccountFallback: %v", err)
	}
	if calls.Load() != 1 || !strings.Contains(rec.Body.String(), `"content":"pong"`) {
		t.Errorf("calls=%d body=%s", calls.Load(), rec.Body.String())
	}
}

// The combo response timeout advances to the next member, which for a custom
// JSON member would replay a business operation that may already be running.
// Its member is exempt: it runs to completion and its answer is served, and
// the next member is never called (review of #751).
func TestCustomJSON_ComboResponseTimeoutDoesNotReplay(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "stream"}[stream], func(t *testing.T) {
			var businessCalls, nextCalls atomic.Int32
			business := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				businessCalls.Add(1)
				time.Sleep(300 * time.Millisecond)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"answer":"business done"}`))
			}))
			defer business.Close()
			next := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				nextCalls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"replayed"},"finish_reason":"stop"}]}`))
			}))
			defer next.Close()

			sqlDB, cleanup := setupChatTestDB(t)
			defer cleanup()
			if _, err := sqlDB.Exec(`DELETE FROM providerConnections`); err != nil {
				t.Fatalf("clear seeded connections: %v", err)
			}
			repo := db.NewRepo(sqlDB)
			seedCustomJSONConn(t, sqlDB, "conn-biz", "key", business.URL+"/run", map[string]any{"responsePath": "answer"})
			p := 2
			if err := repo.CreateProviderConnectionFull("c2", "provider2", "apikey", "c2", &p, `{"apiKey":"sk-p2","baseUrl":"`+next.URL+`"}`); err != nil {
				t.Fatal(err)
			}
			if err := repo.CreateCombo("biz-combo", "biz-combo", "chat", `["`+customJSONProvider+`/custom-model", "provider2/m2"]`, "fallback"); err != nil {
				t.Fatal(err)
			}
			if err := repo.SetComboStrategy("biz-combo", db.ComboStrategy{Strategy: "fallback", FirstTokenTimeoutMs: 50}); err != nil {
				t.Fatal(err)
			}

			body := `{"model":"biz-combo","messages":[{"role":"user","content":"pay invoice"}],"stream":` + strconv.FormatBool(stream) + `}`
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			NewChatHandler(repo).HandleChatCompletions(rec, req)

			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "business done") {
				t.Fatalf("status=%d body=%s, want the business answer", rec.Code, rec.Body.String())
			}
			if businessCalls.Load() != 1 || nextCalls.Load() != 0 {
				t.Errorf("business calls=%d next-member calls=%d, want 1 and 0", businessCalls.Load(), nextCalls.Load())
			}
		})
	}
}
