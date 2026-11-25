package chat

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"9router/proxy/internal/db"
)

// A Codex account parked until its quota window resets must not take part in
// the rotation at all. The strategy stamps the row it reaches, so a parked
// account kept consuming the turn's rotation slot — and every failover pass
// re-resolved its pin before falling through — while it could not serve the
// request. Only the live accounts may be stamped and dialled.
func TestHandleAccountFallback_CodexRotationSkipsQuotaExhausted(t *testing.T) {
	ClearCodexQuotaCache()
	t.Cleanup(ClearCodexQuotaCache)

	var mu sync.Mutex
	var tokens []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		tokens = append(tokens, r.Header.Get("Authorization"))
		mu.Unlock()
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
				"event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	clearSeededConns(t, database)
	seedCodexConn(t, database, "conn-cx-a", "providerSpecificData", nil, srv.URL+"/v1/responses")
	seedCodexConn(t, database, "conn-cx-b", "providerSpecificData", nil, srv.URL+"/v1/responses")
	seedCodexConn(t, database, "conn-cx-dead", "providerSpecificData", nil, srv.URL+"/v1/responses")

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)
	if err := repo.SetProviderStrategy("codex", db.ProviderStrategy{RotateStrategy: "round-robin"}); err != nil {
		t.Fatalf("SetProviderStrategy: %v", err)
	}
	BlockCodexConnectionUntil("conn-cx-dead", time.Now().UTC().Add(time.Hour))

	body := []byte(`{"model":"gpt-6-astra","input":[{"role":"user","content":"hi"}]}`)
	for i := range 3 {
		rec := httptest.NewRecorder()
		if err := h.handleAccountFallback(context.Background(), rec, "codex", "gpt-6-astra", "", body, false, false, "/v1/responses"); err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
	}

	mu.Lock()
	got := append([]string(nil), tokens...)
	mu.Unlock()
	if len(got) != 3 {
		t.Fatalf("upstream saw %d requests, want 3 (%v)", len(got), got)
	}
	for i, tok := range got {
		if tok != "Bearer tok-conn-cx-a" && tok != "Bearer tok-conn-cx-b" {
			t.Errorf("request %d was dialled with %q; the parked account must never be dialled", i+1, tok)
		}
	}
	if got[0] == got[1] {
		t.Errorf("two live accounts must alternate, both requests went to %q", got[0])
	}

	stampOf := func(id string) *string {
		t.Helper()
		conns, err := repo.GetProviderConnections("codex", true)
		if err != nil {
			t.Fatalf("GetProviderConnections: %v", err)
		}
		for _, c := range conns {
			if c.ID == id {
				return c.LastUsedAt
			}
		}
		t.Fatalf("connection %s not found", id)
		return nil
	}
	if stamp := stampOf("conn-cx-dead"); stamp != nil {
		t.Errorf("the parked account was stamped lastUsedAt=%q; rotation must not advance over it", *stamp)
	}
	if stamp := stampOf("conn-cx-a"); stamp == nil {
		t.Error("the live account that served a request carries no rotation stamp")
	}
}
