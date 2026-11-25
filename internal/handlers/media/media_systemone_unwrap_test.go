package media

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

func TestHandleSystemone_CloudflareUnwrap(t *testing.T) {
	wrapped := `{"result":{"model":"clef-flash","answers":{"is_urgent":{"type":"noul","noul":0.9}},"usage":{"input_tokens":10}},"success":true,"errors":[],"messages":[]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(wrapped))
	}))
	defer server.Close()

	sqlDB, cleanup := setupEmbeddingsTestDB(t)
	defer cleanup()
	repo := db.NewRepo(sqlDB)
	p := 1
	err := repo.CreateProviderConnectionFull("conn-oc-1", "opencode", "none", "OpenCode Free", &p, `{"baseUrl":"`+server.URL+`"}`)
	if err != nil {
		t.Fatalf("failed to create connection: %v", err)
	}
	handler := newTestMediaHandler(repo)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/systemone", strings.NewReader(`{"model":"oc/jev-1.13-free","state":"s","questions":{}}`))
	req.Header.Set("Content-Type", "application/json")
	handler.HandleSystemone(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, body: %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, ok := got["result"]; ok {
		t.Errorf("expected unwrapped (no result), got %s", rec.Body.String())
	}
	if got["model"] != "clef-flash" {
		t.Errorf("model = %v, want clef-flash", got["model"])
	}
	ans, _ := got["answers"].(map[string]any)
	if ans == nil || ans["is_urgent"] == nil {
		t.Errorf("answers missing: %s", rec.Body.String())
	}
}

func TestHandleSystemone_NormalPassthrough(t *testing.T) {
	normal := `{"model":"jev-1.13","answers":{"is_urgent":{"type":"noul","noul":0.5}}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(normal))
	}))
	defer server.Close()

	sqlDB, cleanup := setupEmbeddingsTestDB(t)
	defer cleanup()
	repo := db.NewRepo(sqlDB)
	p := 1
	err := repo.CreateProviderConnectionFull("conn-oc-2", "opencode", "none", "OpenCode Free", &p, `{"baseUrl":"`+server.URL+`"}`)
	if err != nil {
		t.Fatalf("failed to create connection: %v", err)
	}
	handler := newTestMediaHandler(repo)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/systemone", strings.NewReader(`{"model":"oc/jev-1.13-free","state":"s","questions":{}}`))
	req.Header.Set("Content-Type", "application/json")
	handler.HandleSystemone(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, body: %s", rec.Code, rec.Body.String())
	}
	if strings.TrimSpace(rec.Body.String()) != normal {
		t.Errorf("body changed: got %s, want %s", rec.Body.String(), normal)
	}
}
