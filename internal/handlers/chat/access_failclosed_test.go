package chat

import (
	"database/sql"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/dbtest"
	"9router/proxy/internal/models"
)

// errorEnvelope is the OpenAI-style error body the access gate writes.
type errorEnvelope struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

// decodeErrorEnvelope decodes a recorded error body as the shared envelope.
func decodeErrorEnvelope(t *testing.T, body []byte) errorEnvelope {
	t.Helper()
	var env errorEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("unmarshal error body: %v", err)
	}
	return env
}

func setupFailClosedTestDB(t testing.TB) (*sql.DB, *db.Repo, func()) {
	t.Helper()
	f, err := os.CreateTemp("", "test_access_*.sqlite")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	f.Close()
	d, err := db.OpenDatabase(f.Name())
	if err != nil {
		os.Remove(f.Name())
		t.Fatalf("OpenDatabase failed: %v", err)
	}
	c := func() {
		d.Close()
		os.Remove(f.Name())
	}
	if err := dbtest.CreateTables(d); err != nil {
		c()
		t.Fatal(err)
	}
	return d, db.NewRepo(d), c
}

func TestModelAccess_FailClosedSemantics(t *testing.T) {
	d, repo, c := setupFailClosedTestDB(t)
	defer c()
	h := NewChatHandler(repo)

	t.Run("storage_error_503", func(t *testing.T) {
		d.Close()
		err := h.checkModelAccess(&models.APIKey{ID: "k1"}, "gpt-4o")
		if err == nil || !IsPolicyUnavailable(err) {
			t.Fatalf("want ErrPolicyUnavailable: %v", err)
		}
		rec := httptest.NewRecorder()
		writeModelAccessError(rec, err)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("want 503, got %d", rec.Code)
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("unmarshal error body: %v", err)
		}
		errObj, ok := body["error"].(map[string]any)
		if !ok || errObj["code"] != "policy_unavailable" {
			t.Fatalf("want error.code == policy_unavailable, got %v", body)
		}
	})

	t.Run("nil_repo_503", func(t *testing.T) {
		handlerNilRepo := &ChatHandler{Repo: nil}
		err := handlerNilRepo.checkModelAccess(&models.APIKey{ID: "k1"}, "gpt-4o")
		if err == nil || !IsPolicyUnavailable(err) {
			t.Fatalf("want ErrPolicyUnavailable on nil repo: %v", err)
		}
		rec := httptest.NewRecorder()
		writeModelAccessError(rec, err)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("want 503, got %d", rec.Code)
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("unmarshal error body: %v", err)
		}
		errObj, ok := body["error"].(map[string]any)
		if !ok || errObj["code"] != "policy_unavailable" {
			t.Fatalf("want error.code == policy_unavailable, got %v", body)
		}
	})

	t.Run("restricted_empty_403", func(t *testing.T) {
		_, r2, c2 := setupFailClosedTestDB(t)
		defer c2()
		_ = r2.SetAllowedModels("k-empty", []string{SentinelDenyAll})
		h2 := NewChatHandler(r2)
		err := h2.checkModelAccess(&models.APIKey{ID: "k-empty"}, "gpt-4o")
		if err == nil || !IsModelNotAllowed(err) {
			t.Fatalf("want ErrModelNotAllowed for !*: %v", err)
		}
		rec := httptest.NewRecorder()
		writeModelAccessError(rec, err)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("want 403, got %d", rec.Code)
		}
		env := decodeErrorEnvelope(t, rec.Body.Bytes())
		if env.Error.Code != "insufficient_quota" {
			t.Fatalf("want error.code == insufficient_quota, got %q", env.Error.Code)
		}
		if env.Error.Type != "permission_error" {
			t.Fatalf("want error.type == permission_error, got %q", env.Error.Type)
		}
		if !strings.Contains(env.Error.Message, "gpt-4o") {
			t.Fatalf("want error.message to name the model, got %q", env.Error.Message)
		}
	})

	t.Run("combo_leaves_isolation", func(t *testing.T) {
		_, r3, c3 := setupFailClosedTestDB(t)
		defer c3()
		_ = r3.SetAllowedModels("k-c", []string{"smart-mix"})
		h3 := NewChatHandler(r3)
		if err := h3.checkModelAccess(&models.APIKey{ID: "k-c"}, "smart-mix"); err != nil {
			t.Fatalf("combo error: %v", err)
		}
		errLeaf := h3.checkModelAccess(&models.APIKey{ID: "k-c"}, "gpt-4o")
		if errLeaf == nil || !IsModelNotAllowed(errLeaf) {
			t.Fatalf("direct leaf must be 403: %v", errLeaf)
		}
		rec := httptest.NewRecorder()
		writeModelAccessError(rec, errLeaf)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("want 403, got %d", rec.Code)
		}
		env := decodeErrorEnvelope(t, rec.Body.Bytes())
		if env.Error.Code != "insufficient_quota" {
			t.Fatalf("want error.code == insufficient_quota, got %q", env.Error.Code)
		}
		if env.Error.Type != "permission_error" {
			t.Fatalf("want error.type == permission_error, got %q", env.Error.Type)
		}
		if !strings.Contains(env.Error.Message, "gpt-4o") {
			t.Fatalf("want error.message to name the model, got %q", env.Error.Message)
		}
	})

	t.Run("unauthenticated_bypass_unaffected", func(t *testing.T) {
		patterns, err := (&ChatHandler{Repo: nil}).effectiveAllowedModels(nil)
		if err != nil || patterns != nil {
			t.Fatalf("nil key must return nil, nil: patterns=%v, err=%v", patterns, err)
		}
		patternsKeyEmpty, err := (&ChatHandler{Repo: nil}).effectiveAllowedModels(&models.APIKey{ID: ""})
		if err != nil || patternsKeyEmpty != nil {
			t.Fatalf("empty key.ID must return nil, nil: patterns=%v, err=%v", patternsKeyEmpty, err)
		}
	})

	t.Run("allowedModelPatterns_failclosed", func(t *testing.T) {
		d4, r4, c4 := setupFailClosedTestDB(t)
		defer c4()
		h4 := NewChatHandler(r4)
		// Empty keyID returns nil (allow all)
		if p := h4.allowedModelPatterns(""); p != nil {
			t.Fatalf("empty keyID must return nil, got %v", p)
		}
		// Nil repo with keyID returns SentinelDenyAll
		hNil := &ChatHandler{Repo: nil}
		pNil := hNil.allowedModelPatterns("k-test")
		if len(pNil) != 1 || pNil[0] != SentinelDenyAll {
			t.Fatalf("nil repo must return [%s], got %v", SentinelDenyAll, pNil)
		}
		// Closed db with keyID returns SentinelDenyAll
		d4.Close()
		pErr := h4.allowedModelPatterns("k-test")
		if len(pErr) != 1 || pErr[0] != SentinelDenyAll {
			t.Fatalf("closed db must return [%s], got %v", SentinelDenyAll, pErr)
		}
	})
}
