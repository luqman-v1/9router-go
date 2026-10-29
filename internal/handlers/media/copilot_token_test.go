package media

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/chat"
)

// stubCopilotTokenServer points the exchange at a throwaway server and restores
// the real endpoint when the test ends. The endpoint is a package var for
// exactly this reason: a test must never let the exchange leave the process.
func stubCopilotTokenServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	prev := copilotTokenExchangeURL
	copilotTokenExchangeURL = srv.URL
	t.Cleanup(func() {
		copilotTokenExchangeURL = prev
		srv.Close()
	})
	return srv
}

// seedGithubConn writes one github oauth row carrying the given data payload.
// The copilot keys belong inside providerSpecificData: the device-login writer
// nests them there, and a top-level copy is parsed away by the connection
// reader as an unknown field.
func seedGithubConn(t *testing.T, database *sql.DB, id string, data map[string]any) {
	t.Helper()
	psd := map[string]any{"authMethod": "device"}
	for _, key := range []string{"copilotToken", "copilotTokenExpiresAt"} {
		if v, ok := data[key]; ok {
			psd[key] = v
			delete(data, key)
		}
	}
	if existing, ok := data["providerSpecificData"].(map[string]any); ok {
		for k, v := range existing {
			psd[k] = v
		}
	}
	data["providerSpecificData"] = psd
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal connection data: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		(?, 'github', 'oauth', 'GH', 1, 1, ?, '2026-10-18T00:00:00Z', '2026-10-18T00:00:00Z')`, id, string(encoded)); err != nil {
		t.Fatalf("seed connection %s: %v", id, err)
	}
}

// readConnData reloads the row's data payload so a test can assert on what was
// actually persisted instead of on what the handler kept in memory.
func readConnData(t *testing.T, database *sql.DB, id string) map[string]any {
	t.Helper()
	var raw string
	if err := database.QueryRow(`SELECT data FROM providerConnections WHERE id = ?`, id).Scan(&raw); err != nil {
		t.Fatalf("read %s: %v", id, err)
	}
	out := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("parse %s data: %v", id, err)
	}
	return out
}

func TestCopilotTokenExpiry(t *testing.T) {
	unixSecs := time.Date(2026, 10, 20, 21, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		raw    any
		want   time.Time
		wantOK bool
	}{
		{"unix seconds as string", strconv.FormatInt(unixSecs.Unix(), 10), unixSecs, true},
		{"unix seconds as number", float64(unixSecs.Unix()), unixSecs, true},
		{"unix seconds as int64", unixSecs.Unix(), unixSecs, true},
		{"unix seconds as int", int(unixSecs.Unix()), unixSecs, true},
		{"unix millis as string", strconv.FormatInt(unixSecs.UnixMilli(), 10), unixSecs, true},
		{"rfc3339 stamp", "2026-10-20T21:00:00Z", unixSecs, true},
		{"surrounding space", "  2026-10-20T21:00:00Z  ", unixSecs, true},
		{"empty string", "", time.Time{}, false},
		{"whitespace only", "   ", time.Time{}, false},
		{"unparseable", "not-a-date", time.Time{}, false},
		{"zero seconds", "0", time.Time{}, false},
		{"nil", nil, time.Time{}, false},
		{"bool", true, time.Time{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := copilotTokenExpiry(tt.raw)
			if ok != tt.wantOK {
				t.Fatalf("copilotTokenExpiry(%#v) ok = %v, want %v", tt.raw, ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if !got.Equal(tt.want) {
				t.Errorf("copilotTokenExpiry(%#v) = %s, want %s", tt.raw, got.UTC(), tt.want.UTC())
			}
		})
	}
}

func TestStoredCopilotToken(t *testing.T) {
	now := time.Date(2026, 10, 20, 20, 0, 0, 0, time.UTC)
	// The skew means a token expiring inside the window is already stale.
	insideSkew := now.Add(copilotTokenSkew / 2).Unix()
	outsideSkew := now.Add(copilotTokenSkew * 4).Unix()
	tests := []struct {
		name      string
		data      map[string]any
		wantToken string
		wantFresh bool
		wantKnown bool
	}{
		{"nil data", nil, "", false, false},
		{"no token key", map[string]any{"apiKey": "x"}, "", false, false},
		{"empty token", map[string]any{"copilotToken": ""}, "", false, false},
		{"token with no expiry", map[string]any{"copilotToken": "tok"}, "tok", false, false},
		{"token with junk expiry", map[string]any{"copilotToken": "tok", "copilotTokenExpiresAt": "soon"}, "tok", false, false},
		{"fresh token", map[string]any{"copilotToken": "tok", "copilotTokenExpiresAt": strconv.FormatInt(outsideSkew, 10)}, "tok", true, true},
		{"expired token", map[string]any{"copilotToken": "tok", "copilotTokenExpiresAt": strconv.FormatInt(now.Add(-time.Hour).Unix(), 10)}, "tok", false, true},
		{"expires inside skew", map[string]any{"copilotToken": "tok", "copilotTokenExpiresAt": strconv.FormatInt(insideSkew, 10)}, "tok", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token, fresh, known := storedCopilotToken(tt.data, now)
			if token != tt.wantToken || fresh != tt.wantFresh || known != tt.wantKnown {
				t.Errorf("storedCopilotToken(%v) = (%q, %v, %v), want (%q, %v, %v)",
					tt.data, token, fresh, known, tt.wantToken, tt.wantFresh, tt.wantKnown)
			}
		})
	}
}

func TestRefreshCopilotToken_ExchangesAndPersists(t *testing.T) {
	database, cleanup := setupEmbeddingsTestDB(t)
	defer cleanup()

	var gotAuth, gotUA string
	var calls int32
	stubCopilotTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		gotAuth = r.Header.Get("Authorization")
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"token":"copilot-fresh","expires_at":1793000000}`)
	})

	seedGithubConn(t, database, "conn-gh", map[string]any{
		"apiKey": "gho_oauth", "accessToken": "gho_oauth",
		"copilotToken": "copilot-stale", "copilotTokenExpiresAt": "1700000000",
	})

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)
	conn, err := repo.GetProviderConnectionByID("conn-gh")
	if err != nil || conn == nil {
		t.Fatalf("read connection: %v", err)
	}
	connData := &chat.ConnectionData{ProviderSpecificData: map[string]any{
		"copilotToken": "copilot-stale", "copilotTokenExpiresAt": "1700000000",
	}}

	token, err := handler.refreshCopilotToken(context.Background(), conn, connData, "gho_oauth")
	if err != nil {
		t.Fatalf("refreshCopilotToken: %v", err)
	}
	if token != "copilot-fresh" {
		t.Errorf("returned token = %q, want copilot-fresh", token)
	}
	if gotAuth != "Bearer gho_oauth" {
		t.Errorf("exchange Authorization = %q, want the OAuth credential, not the Copilot one", gotAuth)
	}
	if gotUA != "GitHubCopilotChat/0.26.7" {
		t.Errorf("exchange User-Agent = %q, want the agent upstream gates on", gotUA)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("exchange calls = %d, want 1", n)
	}

	stored := readConnData(t, database, "conn-gh")
	psd, _ := stored["providerSpecificData"].(map[string]any)
	if psd["copilotToken"] != "copilot-fresh" {
		t.Errorf("persisted copilotToken = %v, want copilot-fresh under providerSpecificData", psd["copilotToken"])
	}
	if psd["copilotTokenExpiresAt"] != "1793000000" {
		t.Errorf("persisted copilotTokenExpiresAt = %v, want 1793000000", psd["copilotTokenExpiresAt"])
	}
	if _, legacy := stored["copilotToken"]; legacy {
		t.Errorf("wrote a top-level copilotToken the reader cannot see: %v", stored)
	}
	if stored["accessToken"] != "gho_oauth" {
		t.Errorf("persist dropped the OAuth credential: %v", stored)
	}
	if connData.ProviderSpecificData["copilotToken"] != "copilot-fresh" {
		t.Errorf("in-memory token not updated: %v", connData.ProviderSpecificData["copilotToken"])
	}
}

func TestRefreshCopilotToken_FailuresLeaveTheRowAlone(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		oauth   string
	}{
		{
			name: "upstream refuses the exchange",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				io.WriteString(w, `{"message":"Bad credentials"}`)
			},
			oauth: "gho_oauth",
		},
		{
			name: "empty token in the answer",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"token":"","expires_at":1793000000}`)
			},
			oauth: "gho_oauth",
		},
		{
			name: "not JSON",
			handler: func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, `not json`)
			},
			oauth: "gho_oauth",
		},
		{
			name:    "no oauth credential to trade",
			handler: func(w http.ResponseWriter, r *http.Request) { t.Error("exchange must not be attempted") },
			oauth:   " ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database, cleanup := setupEmbeddingsTestDB(t)
			defer cleanup()
			stubCopilotTokenServer(t, tt.handler)
			seedGithubConn(t, database, "conn-gh", map[string]any{
				"accessToken": "gho_oauth", "copilotToken": "copilot-stale",
			})

			repo := db.NewRepo(database)
			handler := newTestMediaHandler(repo)
			conn, err := repo.GetProviderConnectionByID("conn-gh")
			if err != nil || conn == nil {
				t.Fatalf("read connection: %v", err)
			}
			connData := &chat.ConnectionData{ProviderSpecificData: map[string]any{"copilotToken": "copilot-stale"}}

			if token, err := handler.refreshCopilotToken(context.Background(), conn, connData, tt.oauth); err == nil {
				t.Fatalf("expected an error, got token %q", token)
			}
			if connData.ProviderSpecificData["copilotToken"] != "copilot-stale" {
				t.Errorf("a failed refresh changed the in-memory token to %v", connData.ProviderSpecificData["copilotToken"])
			}
			stored, _ := readConnData(t, database, "conn-gh")["providerSpecificData"].(map[string]any)
			if stored["copilotToken"] != "copilot-stale" {
				t.Errorf("a failed refresh rewrote the row: %v", stored["copilotToken"])
			}
		})
	}
}

// A token whose expiry is already past is re-exchanged before the request is
// built, so the caller never spends a round trip on a refusal the row already
// predicted.
func TestHandleEmbeddings_RefreshesExpiredCopilotToken(t *testing.T) {
	database, cleanup := setupEmbeddingsTestDB(t)
	defer cleanup()

	var embeddingsCalls int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&embeddingsCalls, 1)
		if got := r.Header.Get("Authorization"); got != "Bearer copilot-fresh" {
			t.Errorf("embeddings Authorization = %q, want the re-exchanged token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"object":"list","data":[{"embedding":[0.1]}]}`)
	}))
	defer upstream.Close()

	var exchangeCalls int32
	stubCopilotTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&exchangeCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"token":"copilot-fresh","expires_at":`+strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)+`}`)
	})

	seedGithubConn(t, database, "conn-gh", map[string]any{
		"apiKey": "gho_oauth", "accessToken": "gho_oauth", "baseUrl": upstream.URL,
		"copilotToken": "copilot-stale", "copilotTokenExpiresAt": strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10),
	})

	handler := newTestMediaHandler(db.NewRepo(database))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(`{"model":"github/text-embedding-ada-002","input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	handler.HandleEmbeddings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if n := atomic.LoadInt32(&exchangeCalls); n != 1 {
		t.Errorf("exchange calls = %d, want 1", n)
	}
	if n := atomic.LoadInt32(&embeddingsCalls); n != 1 {
		t.Errorf("embeddings calls = %d, want 1 (a known-stale token is refreshed up front, not after a 401)", n)
	}
	if stored, _ := readConnData(t, database, "conn-gh")["providerSpecificData"].(map[string]any); stored["copilotToken"] != "copilot-fresh" {
		t.Errorf("row not healed: %v", stored["copilotToken"])
	}
}

// A token whose recorded expiry is missing or unreadable is sent as-is; the 401
// is then the thing that triggers exactly one refresh and one retry.
func TestHandleEmbeddings_RetriesOnceOn401(t *testing.T) {
	database, cleanup := setupEmbeddingsTestDB(t)
	defer cleanup()

	var embeddingsCalls int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&embeddingsCalls, 1)
		if r.Header.Get("Authorization") == "Bearer copilot-stale" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":"token expired"}`)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer copilot-fresh" {
			t.Errorf("retry Authorization = %q, want the re-exchanged token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"object":"list","data":[{"embedding":[0.1]}]}`)
	}))
	defer upstream.Close()

	var exchangeCalls int32
	stubCopilotTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&exchangeCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"token":"copilot-fresh","expires_at":`+strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)+`}`)
	})

	seedGithubConn(t, database, "conn-gh", map[string]any{
		"apiKey": "gho_oauth", "accessToken": "gho_oauth", "baseUrl": upstream.URL,
		"copilotToken": "copilot-stale",
	})

	handler := newTestMediaHandler(db.NewRepo(database))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(`{"model":"github/text-embedding-ada-002","input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	handler.HandleEmbeddings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 after one retry, got %d: %s", rec.Code, rec.Body.String())
	}
	if n := atomic.LoadInt32(&embeddingsCalls); n != 2 {
		t.Errorf("embeddings calls = %d, want 2 (stale, then refreshed)", n)
	}
	if n := atomic.LoadInt32(&exchangeCalls); n != 1 {
		t.Errorf("exchange calls = %d, want 1", n)
	}
}

// A second 401 is the upstream's answer about the account, not the token: the
// lane retries once and stops rather than trading the credential in a loop.
func TestHandleEmbeddings_StopsAfterOneRetry(t *testing.T) {
	database, cleanup := setupEmbeddingsTestDB(t)
	defer cleanup()

	var embeddingsCalls int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&embeddingsCalls, 1)
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":"nope"}`)
	}))
	defer upstream.Close()

	var exchangeCalls int32
	stubCopilotTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&exchangeCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"token":"copilot-fresh","expires_at":`+strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)+`}`)
	})

	seedGithubConn(t, database, "conn-gh", map[string]any{
		"apiKey": "gho_oauth", "accessToken": "gho_oauth", "baseUrl": upstream.URL,
		"copilotToken": "copilot-stale",
	})

	handler := newTestMediaHandler(db.NewRepo(database))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(`{"model":"github/text-embedding-ada-002","input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	handler.HandleEmbeddings(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 to reach the caller, got %d: %s", rec.Code, rec.Body.String())
	}
	if n := atomic.LoadInt32(&embeddingsCalls); n != 2 {
		t.Errorf("embeddings calls = %d, want 2 (one retry, then stop)", n)
	}
	if n := atomic.LoadInt32(&exchangeCalls); n != 1 {
		t.Errorf("exchange calls = %d, want 1", n)
	}
}

// A token that is still fresh must not cost an exchange: the whole point of
// recording the expiry is to skip the round trip.
func TestHandleEmbeddings_FreshCopilotTokenIsNotExchanged(t *testing.T) {
	database, cleanup := setupEmbeddingsTestDB(t)
	defer cleanup()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer copilot-stored" {
			t.Errorf("embeddings Authorization = %q, want the stored token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"object":"list","data":[{"embedding":[0.1]}]}`)
	}))
	defer upstream.Close()

	var exchangeCalls int32
	stubCopilotTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&exchangeCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"token":"copilot-fresh","expires_at":1793000000}`)
	})

	seedGithubConn(t, database, "conn-gh", map[string]any{
		"apiKey": "gho_oauth", "accessToken": "gho_oauth", "baseUrl": upstream.URL,
		"copilotToken": "copilot-stored", "copilotTokenExpiresAt": strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10),
	})

	handler := newTestMediaHandler(db.NewRepo(database))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(`{"model":"github/text-embedding-ada-002","input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	handler.HandleEmbeddings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if n := atomic.LoadInt32(&exchangeCalls); n != 0 {
		t.Errorf("exchange calls = %d, want 0 for a fresh token", n)
	}
}
