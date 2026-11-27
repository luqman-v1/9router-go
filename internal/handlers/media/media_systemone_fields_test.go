package media

import (
	"context"

	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"9router/proxy/internal/db"
)

func TestHandleSystemone_FieldsPayload(t *testing.T) {
	sqlDB, cleanup := setupEmbeddingsTestDB(t)
	defer cleanup()
	repo := db.NewRepo(sqlDB)
	p := 1

	var capturedBody atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		capturedBody.Store(string(b))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"values":{"invoice_number":{"value":"99812","confidence":0.99}}}`))
	}))
	defer server.Close()
	if err := repo.CreateProviderConnectionFull(context.Background(), "conn-cf-1", "cloudflare-ai", "apikey", "Cloudflare AI Test", &p, `{"apiKey":"cf-token","baseUrl":"`+server.URL+`","providerSpecificData":{"accountId":"acc_12345"}}`); err != nil {
		t.Fatalf("seed conn: %v", err)
	}
	handler := newTestMediaHandler(repo)

	t.Run("clairic fields", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/systemone", strings.NewReader(`{"model":"cf/@cf/cloudflare/clairic","state":"Invoice #99812","fields":{"invoice_number":{"type":"text"}}}`))
		req.Header.Set("Content-Type", "application/json")
		handler.HandleSystemone(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want 200, body: %s", rec.Code, rec.Body.String())
		}
		var sent map[string]any
		if err := json.Unmarshal([]byte(capturedBody.Load().(string)), &sent); err != nil {
			t.Fatalf("unmarshal sent: %v", err)
		}
		if sent["model"] != "clairic" {
			t.Errorf("sent model = %v, want clairic", sent["model"])
		}
	})
	t.Run("clef decision", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/systemone", strings.NewReader(`{"model":"cf/@cf/cloudflare/clef","state":"x","questions":{"q":{"type":"noul"}}}`))
		req.Header.Set("Content-Type", "application/json")
		handler.HandleSystemone(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want 200, body: %s", rec.Code, rec.Body.String())
		}
		var sent map[string]any
		if err := json.Unmarshal([]byte(capturedBody.Load().(string)), &sent); err != nil {
			t.Fatalf("unmarshal sent: %v", err)
		}
		if sent["model"] != "clef" {
			t.Errorf("sent model = %v, want clef", sent["model"])
		}
	})
	t.Run("clairic questions-only fails", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/systemone", strings.NewReader(`{"model":"cf/@cf/cloudflare/clairic","state":"x","questions":{"q":{"type":"noul"}}}`))
		req.Header.Set("Content-Type", "application/json")
		handler.HandleSystemone(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("got status %d, want 400, body: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "Missing required field: fields") {
			t.Fatalf("body = %s", rec.Body.String())
		}
	})
	t.Run("clef fields-only fails", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/systemone", strings.NewReader(`{"model":"cf/@cf/cloudflare/clef","state":"x","fields":{"f":{}}}`))
		req.Header.Set("Content-Type", "application/json")
		handler.HandleSystemone(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("got status %d, want 400, body: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "Missing required field: questions") {
			t.Fatalf("body = %s", rec.Body.String())
		}
	})
	// custom model test
	if err := repo.SetKV("customModels", "cf|custom-clairic|llm", `{"providerAlias":"cf","id":"custom-clairic","type":"llm","name":"custom-clairic","upstreamModelId":"clef-custom"}`); err != nil {
		t.Fatalf("seed custom: %v", err)
	}
	t.Run("custom upstream id", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/systemone", strings.NewReader(`{"model":"cf/custom-clairic","state":"x","questions":{"q":{"type":"noul"}}}`))
		req.Header.Set("Content-Type", "application/json")
		handler.HandleSystemone(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want 200, body: %s", rec.Code, rec.Body.String())
		}
		var sent map[string]any
		if err := json.Unmarshal([]byte(capturedBody.Load().(string)), &sent); err != nil {
			t.Fatalf("unmarshal sent: %v", err)
		}
		if sent["model"] != "clef-custom" {
			t.Errorf("sent model = %v, want clef-custom", sent["model"])
		}
	})
}
