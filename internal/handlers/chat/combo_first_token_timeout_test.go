package chat

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/db"
)

func TestComboChat_FirstTokenTimeoutAdvancesToNextModel(t *testing.T) {
	sqlDB, cleanup := setupChatTestDB(t)
	defer cleanup()
	database := db.NewRepo(sqlDB)

	// Member 1 stalls on first token (headers sent immediately, but no tokens arrive for 500ms).
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(500 * time.Millisecond)
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"slow\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer srv1.Close()

	// Member 2 succeeds immediately.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"fast answer\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer srv2.Close()

	p1 := 1
	p2 := 2
	if err := database.CreateProviderConnectionFull("c1", "provider1", "apikey", "c1", &p1, `{"apiKey":"sk-p1","baseUrl":"`+srv1.URL+`"}`); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateProviderConnectionFull("c2", "provider2", "apikey", "c2", &p2, `{"apiKey":"sk-p2","baseUrl":"`+srv2.URL+`"}`); err != nil {
		t.Fatal(err)
	}

	if err := database.CreateCombo("test-timeout-combo", "test-timeout-combo", "chat", `["provider1/m1", "provider2/m2"]`, "fallback"); err != nil {
		t.Fatal(err)
	}

	// Configure firstTokenTimeoutMs = 50ms for test-timeout-combo.
	if err := database.SetComboStrategy("test-timeout-combo", db.ComboStrategy{
		Strategy:            "fallback",
		FirstTokenTimeoutMs: 50,
	}); err != nil {
		t.Fatal(err)
	}

	handler := NewChatHandler(database)

	reqBody := `{"model":"test-timeout-combo","messages":[{"role":"user","content":"hi"}],"stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	out := rec.Body.String()
	if !strings.Contains(out, "fast answer") {
		t.Errorf("expected fast answer from member 2, got: %s", out)
	}
	if strings.Contains(out, "slow") {
		t.Errorf("expected slow member 1 output to be discarded, got: %s", out)
	}
}

func TestComboChat_FirstTokenTimeoutIgnoredOnLastMember(t *testing.T) {
	sqlDB, cleanup := setupChatTestDB(t)
	defer cleanup()
	database := db.NewRepo(sqlDB)

	// Only 1 member, stalls for 100ms before returning answer.
	// Since it is the last (and only) member, the per-combo firstTokenTimeoutMs (50ms)
	// must NOT cancel it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(100 * time.Millisecond)
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"last member answer\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	p1 := 1
	if err := database.CreateProviderConnectionFull("c1", "provider1", "apikey", "c1", &p1, `{"apiKey":"sk-p1","baseUrl":"`+srv.URL+`"}`); err != nil {
		t.Fatal(err)
	}

	if err := database.CreateCombo("test-single-combo", "test-single-combo", "chat", `["provider1/m1"]`, "fallback"); err != nil {
		t.Fatal(err)
	}

	if err := database.SetComboStrategy("test-single-combo", db.ComboStrategy{
		Strategy:            "fallback",
		FirstTokenTimeoutMs: 50,
	}); err != nil {
		t.Fatal(err)
	}

	handler := NewChatHandler(database)

	reqBody := `{"model":"test-single-combo","messages":[{"role":"user","content":"hi"}],"stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	out := rec.Body.String()
	if !strings.Contains(out, "last member answer") {
		t.Errorf("expected last member answer, got: %s", out)
	}
}

func TestComboChat_FirstTokenArrivesBeforeTimeoutLetsStreamComplete(t *testing.T) {
	sqlDB, cleanup := setupChatTestDB(t)
	defer cleanup()
	database := db.NewRepo(sqlDB)

	// Member 1 flushes its first token immediately (<10ms), then completes after 200ms.
	// Since firstTokenTimeoutMs = 50ms, the timer must be disarmed on the first token
	// and allow the stream to finish cleanly with [DONE] instead of cutting it off.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"start\"}}]}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(200 * time.Millisecond)
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"finish\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	p1 := 1
	p2 := 2
	if err := database.CreateProviderConnectionFull("c1", "provider1", "apikey", "c1", &p1, `{"apiKey":"sk-p1","baseUrl":"`+srv.URL+`"}`); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateProviderConnectionFull("c2", "provider2", "apikey", "c2", &p2, `{"apiKey":"sk-p2","baseUrl":"http://127.0.0.1:9999"}`); err != nil {
		t.Fatal(err)
	}

	if err := database.CreateCombo("test-complete-combo", "test-complete-combo", "chat", `["provider1/m1", "provider2/m2"]`, "fallback"); err != nil {
		t.Fatal(err)
	}

	if err := database.SetComboStrategy("test-complete-combo", db.ComboStrategy{
		Strategy:            "fallback",
		FirstTokenTimeoutMs: 50,
	}); err != nil {
		t.Fatal(err)
	}

	handler := NewChatHandler(database)

	reqBody := `{"model":"test-complete-combo","messages":[{"role":"user","content":"hi"}],"stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	out := rec.Body.String()
	if !strings.Contains(out, "start") || !strings.Contains(out, "finish") || !strings.Contains(out, "[DONE]") {
		t.Errorf("expected full stream completed with [DONE], got: %s", out)
	}
}
