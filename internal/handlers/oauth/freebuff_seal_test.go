package oauth

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/vault"
)

type freebuffCountingBody struct {
	remaining int64
	readBytes int64
}

func (b *freebuffCountingBody) Read(p []byte) (int, error) {
	if b.remaining <= 0 {
		return 0, io.EOF
	}
	n := len(p)
	if int64(n) > b.remaining {
		n = int(b.remaining)
	}
	for i := range n {
		p[i] = 'a'
	}
	b.remaining -= int64(n)
	b.readBytes += int64(n)
	return n, nil
}

func (b *freebuffCountingBody) Close() error { return nil }

func TestHandleFreebuffPoll_SealsTokensThroughVaultRepo(t *testing.T) {
	const secretToken = "fb-vault-seal-secret-token"
	email := "first@freebuff.com"
	name := "First Freebuff"

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/cli/status" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"authorized","authToken":"` + secretToken + `","email":"` + email + `","name":"` + name + `"}`))
	}))
	defer mockServer.Close()

	oldURL := freebuffAuthBaseURL
	freebuffAuthBaseURL = mockServer.URL
	defer func() { freebuffAuthBaseURL = oldURL }()

	database, cleanup := setupTestDB(t)
	defer cleanup()

	v, err := vault.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatalf("build vault: %v", err)
	}
	repo := db.NewRepo(database)
	repo.SetVault(v)

	handler := NewOAuthHandler(repo)
	pollBody := `{"fingerprintId":"fp-seal-1","fingerprintHash":"hash-seal-1"}`

	// Create pass.
	req1 := httptest.NewRequest(http.MethodPost, "/api/oauth/freebuff/poll", strings.NewReader(pollBody))
	req1.Header.Set("Content-Type", "application/json")
	rec1 := httptest.NewRecorder()
	handler.HandleFreebuffPoll(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("create poll status = %d: %s", rec1.Code, rec1.Body.String())
	}

	connID := "fb-" + shortHash(secretToken)
	var raw string
	if err := repo.RawDB().QueryRow(`SELECT data FROM providerConnections WHERE id = ?`, connID).Scan(&raw); err != nil {
		t.Fatalf("read raw data after create: %v", err)
	}
	if strings.Contains(raw, secretToken) {
		t.Fatalf("freebuff token stored plaintext at rest after create: %s", raw)
	}

	got, err := repo.GetProviderConnectionByID(connID)
	if err != nil {
		t.Fatalf("hydrate connection after create: %v", err)
	}
	if !strings.Contains(got.Data, secretToken) {
		t.Fatalf("reader did not hydrate token after create: %s", got.Data)
	}

	// Update pass on existing sealed row.
	email = "updated@freebuff.com"
	name = "Updated Freebuff"
	req2 := httptest.NewRequest(http.MethodPost, "/api/oauth/freebuff/poll", strings.NewReader(pollBody))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	handler.HandleFreebuffPoll(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("update poll status = %d: %s", rec2.Code, rec2.Body.String())
	}

	var rawAfterUpdate string
	if err := repo.RawDB().QueryRow(`SELECT data FROM providerConnections WHERE id = ?`, connID).Scan(&rawAfterUpdate); err != nil {
		t.Fatalf("read raw data after update: %v", err)
	}
	if strings.Contains(rawAfterUpdate, secretToken) {
		t.Fatalf("freebuff token stored plaintext at rest after update: %s", rawAfterUpdate)
	}

	gotAfterUpdate, err := repo.GetProviderConnectionByID(connID)
	if err != nil {
		t.Fatalf("hydrate connection after update: %v", err)
	}
	if !strings.Contains(gotAfterUpdate.Data, secretToken) {
		t.Fatalf("reader did not hydrate token after update: %s", gotAfterUpdate.Data)
	}
	if gotAfterUpdate.Name == nil || *gotAfterUpdate.Name != email {
		t.Fatalf("expected updated name %q, got %v", email, gotAfterUpdate.Name)
	}
}

func TestHandleFreebuffPoll_CapsOversizedRequestBody(t *testing.T) {
	body := &freebuffCountingBody{remaining: 2 << 20}
	handler := NewOAuthHandler(nil)

	req := httptest.NewRequest(http.MethodPost, "/api/oauth/freebuff/poll", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleFreebuffPoll(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-JSON oversized body, got %d: %s", rec.Code, rec.Body.String())
	}
	if body.readBytes > 1<<20 {
		t.Fatalf("read %d bytes from poll request body, exceeds 1 MiB cap (%d)", body.readBytes, 1<<20)
	}
}
