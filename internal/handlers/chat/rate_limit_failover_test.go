package chat

import (
	"context"
	"database/sql"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"9router/proxy/internal/db"
)

// retryInfoBody is the shape Google answers a spent Gemini/Antigravity quota
// with: the wait rides on a google.rpc.RetryInfo detail, not on a field the
// router already parsed.
func retryInfoBody(delay string) string {
	return `{"error":{"code":429,"message":"Resource has been exhausted","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"` + delay + `"}]}}`
}

// rateLimitedBody carries a 429 with no wait instruction at all.
const rateLimitedBody = `{"error":{"message":"rate limited"}}`

// isoRetryAfterBody carries the wait as an absolute timestamp, the shape
// extractRetryAfter has always understood.
func isoRetryAfterBody(d time.Duration) string {
	return `{"error":{"message":"rate limited","retryAfter":"` + time.Now().Add(d).UTC().Format(time.RFC3339) + `"}}`
}

// TestRetryAfterWait_Extraction pins every carrier of a retry instruction. The
// header and the RetryInfo detail were both invisible to the router before:
// UpstreamError carried no headers at all, and retryDelay sits on the detail
// rather than in the ErrorInfo metadata extractResetDuration reads.
func TestRetryAfterWait_Extraction(t *testing.T) {
	httpDate := time.Now().Add(45 * time.Second).UTC().Format(http.TimeFormat)
	pastDate := time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat)

	tests := []struct {
		name   string
		header http.Header
		body   string
		want   time.Duration
	}{
		{"retryinfo detail", nil, retryInfoBody("120s"), 2 * time.Minute},
		{"retryinfo in metadata", nil, `{"error":{"details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","metadata":{"retryDelay":"45s"}}]}}`, 45 * time.Second},
		{"header delta seconds", http.Header{"Retry-After": {"30"}}, rateLimitedBody, 30 * time.Second},
		{"header http date", http.Header{"Retry-After": {httpDate}}, rateLimitedBody, 45 * time.Second},
		{"longest carrier wins", http.Header{"Retry-After": {"30"}}, retryInfoBody("120s"), 2 * time.Minute},
		{"iso body field", nil, isoRetryAfterBody(10 * time.Second), 10 * time.Second},
		{"duration body field", nil, `{"retry_after":"90s"}`, 90 * time.Second},
		{"quota reset in message", nil, `{"error":{"message":"Quota exceeded. Resets in 2m."}}`, 2 * time.Minute},
		{"malformed header", http.Header{"Retry-After": {"soon"}}, rateLimitedBody, 0},
		{"empty header", http.Header{"Retry-After": {""}}, retryInfoBody("120s"), 2 * time.Minute},
		{"past http date", http.Header{"Retry-After": {pastDate}}, rateLimitedBody, 0},
		{"zero retry delay", nil, retryInfoBody("0s"), 0},
		{"unparseable retry delay", nil, retryInfoBody("later"), 0},
		{"empty body", nil, "", 0},
		{"non json body", nil, `<html>gateway timeout</html>`, 0},
		{"no carriers", nil, rateLimitedBody, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ue := &upstreamError{StatusCode: http.StatusTooManyRequests, Body: []byte(tt.body), Header: tt.header}
			got, ok := retryAfterWait(ue)
			if tt.want == 0 {
				if ok {
					t.Errorf("retryAfterWait = (%v, true), want no usable wait from %q / %v", got, tt.body, tt.header)
				}
				return
			}
			if !ok {
				t.Fatalf("retryAfterWait found no wait in %q / %v, want %v", tt.body, tt.header, tt.want)
			}
			// Absolute timestamps and HTTP-dates are read against the wall
			// clock, so allow a second of slack either way.
			if diff := got - tt.want; diff < -time.Second || diff > time.Second {
				t.Errorf("retryAfterWait = %v, want %v (+/-1s)", got, tt.want)
			}
		})
	}
}

// TestRetryableCooldownSec pins how long a failed account stays parked. The
// classifier's own backoff is the baseline, not the answer: an account Google
// said to leave alone for two minutes used to be handed back after two
// seconds, which is what kept it absorbing failing requests.
func TestRetryableCooldownSec(t *testing.T) {
	tests := []struct {
		name   string
		status int
		base   time.Duration
		header http.Header
		body   string
		want   int
	}{
		{"429 without a wait falls back to the classifier", http.StatusTooManyRequests, 2 * time.Second, nil, rateLimitedBody, 2},
		{"429 retryinfo window wins", http.StatusTooManyRequests, 2 * time.Second, nil, retryInfoBody("120s"), 120},
		{"429 header window wins", http.StatusTooManyRequests, 2 * time.Second, http.Header{"Retry-After": {"30"}}, rateLimitedBody, 30},
		{"429 short window shortens the lock so the brief retry can run", http.StatusTooManyRequests, 2 * time.Second, nil, retryInfoBody("1s"), 1},
		{"429 nonsense window is capped", http.StatusTooManyRequests, 2 * time.Second, nil, retryInfoBody("72h"), int(maxResetCooldown / time.Second)},
		{"503 keeps its own backoff", http.StatusServiceUnavailable, 30 * time.Second, nil, rateLimitedBody, 30},
		{"503 still reads the quota reset", http.StatusServiceUnavailable, 30 * time.Second, nil, `{"error":{"message":"Quota exceeded. Resets in 2m."}}`, 120},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ue := &upstreamError{StatusCode: tt.status, Body: []byte(tt.body), Header: tt.header}
			if got := retryableCooldownSec(tt.status, tt.base, ue); got != tt.want {
				t.Errorf("retryableCooldownSec(%d, %v) = %ds, want %ds", tt.status, tt.base, got, tt.want)
			}
		})
	}
}

// seedPooledConns inserts n connections for one provider, all pointing at
// upstream, ordered so the pool is picked from first to last.
func seedPooledConns(t *testing.T, database *sql.DB, n int, upstream string) {
	t.Helper()
	for i := 1; i <= n; i++ {
		connID := "conn-pool-" + strconv.Itoa(i)
		key := "sk-pool-" + strconv.Itoa(i)
		data, _ := json.Marshal(map[string]any{"apiKey": key, "baseUrl": upstream})
		if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
			VALUES (?, 'deepseek', 'apikey', 'Pooled', ?, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
			connID, i, string(data)); err != nil {
			t.Fatalf("seed connection %s: %v", connID, err)
		}
	}
}

// TestComboRateLimitFailover drives the real combo loop over a pool of accounts
// that all answer the same way, and pins how many requests the client spends.
//
// A 429 whose wait outruns brief429RetryTolerance must not come back to the
// accounts that just refused it; a shorter one still gets the bounded second
// pass, and a transient 5xx keeps the wider cap it has always had.
func TestComboRateLimitFailover(t *testing.T) {
	const okBody = `{"id":"chatcmpl-1","object":"chat.completion","created":0,"model":"deepseek-chat","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`

	tests := []struct {
		name          string
		accounts      int
		status        int
		header        http.Header
		body          func() string
		failFirstHits int32
		wantHits      int32
		wantMinWait   time.Duration
		wantStatus    int
		// wantRetryAfter is the Retry-After the client must be told to wait,
		// when checkRetryAfter is set. The window is the contract the client
		// schedules against, so an unparsed carrier fails here too.
		checkRetryAfter bool
		wantRetryAfter  string
	}{
		{
			name: "429 retryinfo window advances and never re-hits the account",
			// A Retry-After long enough to be a quota window, not a blip.
			accounts: 2, status: http.StatusTooManyRequests,
			body:          func() string { return retryInfoBody("120s") },
			failFirstHits: 1, wantHits: 2, wantStatus: http.StatusOK,
		},
		{
			name:     "429 retryinfo window is not retried once the pool is spent",
			accounts: 2, status: http.StatusTooManyRequests,
			body:          func() string { return retryInfoBody("120s") },
			failFirstHits: 2, wantHits: 2, wantStatus: http.StatusTooManyRequests,
			checkRetryAfter: true, wantRetryAfter: "120",
		},
		{
			name:     "429 retry-after header is not retried once the pool is spent",
			accounts: 2, status: http.StatusTooManyRequests,
			header:        http.Header{"Retry-After": {"45"}},
			body:          func() string { return rateLimitedBody },
			failFirstHits: 2, wantHits: 2, wantStatus: http.StatusTooManyRequests,
			checkRetryAfter: true, wantRetryAfter: "45",
		},
		{
			name:     "429 with a retry-after past the tolerance is not retried",
			accounts: 2, status: http.StatusTooManyRequests,
			body:          func() string { return isoRetryAfterBody(5 * time.Second) },
			failFirstHits: 2, wantHits: 2, wantStatus: http.StatusTooManyRequests,
		},
		{
			name:     "429 without any retry-after is not retried",
			accounts: 2, status: http.StatusTooManyRequests,
			body:          func() string { return rateLimitedBody },
			failFirstHits: 2, wantHits: 2, wantStatus: http.StatusTooManyRequests,
			checkRetryAfter: true, wantRetryAfter: "",
		},
		{
			// One account in the pool, so a second upstream request can only
			// come from the bounded second pass. Nothing else here changes:
			// a Retry-After inside the tolerance still buys that pass, and the
			// lock it takes is short enough for the pass to find the account.
			name:     "429 with a brief retry-after still gets the bounded second pass",
			accounts: 1, status: http.StatusTooManyRequests,
			body:          func() string { return isoRetryAfterBody(time.Second) },
			failFirstHits: 1, wantHits: 2, wantStatus: http.StatusOK,
		},
		{
			// An RFC3339 Retry-After is truncated to whole seconds, so the wait
			// lands anywhere in (2s, 3s]; the floor is what is pinned.
			name:     "a transient 503 keeps the wider retry cap",
			accounts: 2, status: http.StatusServiceUnavailable,
			body:          func() string { return isoRetryAfterBody(3 * time.Second) },
			failFirstHits: 3, wantHits: 4, wantMinWait: 2 * time.Second, wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if n := hits.Add(1); n <= tt.failFirstHits {
					for k, vs := range tt.header {
						for _, v := range vs {
							w.Header().Add(k, v)
						}
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tt.status)
					w.Write([]byte(tt.body()))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(okBody))
			}))
			defer srv.Close()

			database, cleanup := setupChatTestDB(t)
			defer cleanup()
			if _, err := database.Exec(`DELETE FROM providerConnections WHERE id IN ('conn-1', 'conn-2')`); err != nil {
				t.Fatalf("clear seeded connections: %v", err)
			}
			seedPooledConns(t, database, tt.accounts, srv.URL)

			repo := db.NewRepo(database)
			h := NewChatHandler(repo)

			comboModels := []string{"deepseek/deepseek-chat"}
			modelsJSON, _ := json.Marshal(comboModels)
			comboID := "combo-rl"
			if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES (?, ?, 'fallback', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, comboID, comboID, string(modelsJSON)); err != nil {
				t.Fatalf("seed combo: %v", err)
			}

			translatedReq := map[string]any{
				"model":      "deepseek-chat",
				"max_tokens": 100,
				"messages":   []map[string]any{{"role": "user", "content": "hi"}},
			}
			rec := httptest.NewRecorder()
			start := time.Now()
			h.handleMessagesComboFallback(context.Background(), rec, translatedReq, comboModels, "fallback", false, comboID, 0)
			elapsed := time.Since(start)

			if got := hits.Load(); got != tt.wantHits {
				t.Errorf("upstream received %d requests, want %d (status %d, body %s)", got, tt.wantHits, rec.Code, rec.Body.String())
			}
			if rec.Code != tt.wantStatus {
				t.Errorf("client status = %d, want %d (body %s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.checkRetryAfter {
				if got := rec.Header().Get("Retry-After"); got != tt.wantRetryAfter {
					t.Errorf("Retry-After header = %q, want %q", got, tt.wantRetryAfter)
				}
			}
			if elapsed < tt.wantMinWait {
				t.Errorf("handler returned after %v, want it to hold the request for at least %v", elapsed, tt.wantMinWait)
			}
		})
	}
}

// A rate limit that names its window must park the account for that window. It
// used to park for the classifier's 2s base instead, so the very next request
// picked the same exhausted account again.
func TestRateLimitCooldownParksAccountForTheNamedWindow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(retryInfoBody("45s")))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections WHERE id IN ('conn-1', 'conn-2')`); err != nil {
		t.Fatalf("clear seeded connections: %v", err)
	}
	seedConnDB(t, database, "deepseek", "conn-ag", "sk-ag", srv.URL)

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	body := []byte(`{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	if err := h.handleAccountFallback(context.Background(), rec, "deepseek", "deepseek-chat", "", body, false, false, "/v1/chat/completions"); err == nil {
		t.Fatal("expected the 429 to be returned as an error")
	}

	var rawData string
	if err := database.QueryRow(`SELECT data FROM providerConnections WHERE id = 'conn-ag'`).Scan(&rawData); err != nil {
		t.Fatalf("read connection row: %v", err)
	}
	until, ok := db.ConnectionCooldownUntil(rawData)
	if !ok {
		t.Fatal("expected the account to carry an account-scoped cooldown after a 429")
	}
	if left := time.Until(until); left < 40*time.Second || left > 45*time.Second {
		t.Errorf("account parked for %v, want the 45s window the upstream named (2s base backoff would read as ~%v)", left, left.Round(time.Second))
	}
}
