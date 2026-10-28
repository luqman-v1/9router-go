package translator

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"9router/proxy/internal/db"
)

// openTestSignatureRepo builds a real SQLite database through the normal schema
// path, the same way the gateway bootstraps a fresh install.
func openTestSignatureRepo(t *testing.T) *db.Repo {
	t.Helper()
	path := filepath.Join(t.TempDir(), "signatures.sqlite")
	conn, err := db.OpenDatabase(path)
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.EnsureCoreSchema(conn); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	return db.NewRepo(conn)
}

// newProcess simulates a gateway restart: the in-memory map is rebuilt empty and
// the signature store opens over the same backing database.
func newProcess(t *testing.T, repo *db.Repo) {
	t.Helper()
	SetThoughtSignaturePersistence(nil) // drops L1 memory, as a process exit would
	SetThoughtSignaturePersistence(repo)
}

// TestThoughtSignatureStore_SurvivesRestart is the G26 contract: a signature
// written by one store instance must be readable by a fresh instance over the
// same database, with family and expiry rules unchanged.
func TestThoughtSignatureStore_SurvivesRestart(t *testing.T) {
	repo := openTestSignatureRepo(t)
	t.Cleanup(func() { SetThoughtSignaturePersistence(nil) })

	t.Run("RestartRecovery", func(t *testing.T) {
		const (
			callID    = "call_persist_001"
			sessionID = "sess_persist"
			sig       = "PERSISTED_SIGNATURE_001"
			model     = "gemini-2.5-flash"
		)

		SetThoughtSignaturePersistence(repo)
		StoreGeminiThoughtSignature(callID, sig, sessionID, model)
		if got := GetGeminiThoughtSignature(callID, sessionID, model); got != sig {
			t.Fatalf("pre-restart read: expected %q, got %q", sig, got)
		}

		newProcess(t, repo)

		if got := GetGeminiThoughtSignature(callID, sessionID, model); got != sig {
			t.Fatalf("after restart: expected %q from persistence, got %q", sig, got)
		}
	})

	t.Run("ExpiryStillHidesStaleRow", func(t *testing.T) {
		const (
			callID    = "call_expired_g26"
			sessionID = "sess_expired_g26"
			sig       = "STALE_SIGNATURE"
			model     = "gemini-2.5-pro"
		)

		SetThoughtSignaturePersistence(nil)
		if err := repo.SaveThoughtSignature(context.Background(), sessionID+":"+callID, sig, "gemini", time.Now().Add(-time.Minute)); err != nil {
			t.Fatalf("seed expired row: %v", err)
		}
		newProcess(t, repo)

		if got := GetGeminiThoughtSignature(callID, sessionID, model); got != "" {
			t.Fatalf("expired persisted signature returned: %q", got)
		}
	})

	t.Run("FamilyMismatchStillRefuses", func(t *testing.T) {
		const (
			callID    = "call_family_g26"
			sessionID = "sess_family_g26"
			sig       = "CLAUDE_SIGNATURE_ONLY"
		)

		SetThoughtSignaturePersistence(nil)
		newProcess(t, repo)
		StoreGeminiThoughtSignature(callID, sig, sessionID, "claude-opus-4-6-thinking")
		newProcess(t, repo)

		if got := GetGeminiThoughtSignature(callID, sessionID, "gemini-2.5-flash"); got != "" {
			t.Fatalf("cross-family persisted signature leaked: %q", got)
		}
		if got := GetGeminiThoughtSignature(callID, sessionID, "claude-sonnet-4-6"); got != sig {
			t.Fatalf("same-family persisted signature lost: expected %q, got %q", sig, got)
		}
	})
}
