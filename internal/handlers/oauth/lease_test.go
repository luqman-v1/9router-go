package oauth

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	jsonv2 "encoding/json/v2"

	"9router/proxy/internal/db"
	"9router/proxy/internal/proxy/executor/mcodelease"
)

// leaseTestHandler boots the handler against a real temp database, so the
// endpoints are exercised through the same repo the production router uses.
func leaseTestHandler(t *testing.T) *OAuthHandler {
	t.Helper()
	dir := t.TempDir()
	conn, err := db.OpenDatabase(filepath.Join(dir, "test.sqlite"))
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	if err := db.EnsureCoreSchema(conn); err != nil {
		conn.Close()
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return NewOAuthHandler(db.NewRepo(conn))
}

// lease-status must answer 200 with found:false and the broker's code when no
// desktop app is installed: the dashboard's connect button reads that code to
// decide what to tell the operator.
func TestLeaseStatusWithoutBroker(t *testing.T) {
	h := leaseTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/oauth/minimax-code/lease-status?data_dir="+t.TempDir(), nil)
	req.SetPathValue("provider", "minimax-code")
	rec := httptest.NewRecorder()

	h.HandleLeaseStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := jsonv2.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["found"] != false {
		t.Errorf("found = %v, want false", out["found"])
	}
	if out["error"] != mcodelease.CodeBrokerUnavailable {
		t.Errorf("error = %v, want %s", out["error"], mcodelease.CodeBrokerUnavailable)
	}
	if _, ok := out["message"]; !ok {
		t.Error("no message for the operator")
	}
}

// A provider that has no lease lane must be refused, not probed: a request for
// codex would otherwise create a connection the executor cannot use.
func TestLeaseStatusRejectsOtherProviders(t *testing.T) {
	h := leaseTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/oauth/codex/lease-status", nil)
	req.SetPathValue("provider", "codex")
	rec := httptest.NewRecorder()

	h.HandleLeaseStatus(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// lease-connect without a broker must fail with 409 and the broker's code —
// the same status upstream uses, so the dashboard's error branch works
// unchanged.
func TestLeaseConnectWithoutBroker(t *testing.T) {
	h := leaseTestHandler(t)
	body, _ := json.Marshal(map[string]string{"dataDir": t.TempDir()})
	req := httptest.NewRequest(http.MethodPost, "/api/oauth/minimax-code/lease-connect", bytes.NewReader(body))
	req.SetPathValue("provider", "minimax-code")
	rec := httptest.NewRecorder()

	h.HandleLeaseConnect(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := jsonv2.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["code"] != mcodelease.CodeBrokerUnavailable {
		t.Errorf("code = %v, want %s", out["code"], mcodelease.CodeBrokerUnavailable)
	}
	// No connection may have been written: a dead broker must not leave a
	// row behind that looks usable.
	conns, err := h.Repo.GetProviderConnections("minimax-code", false)
	if err != nil {
		t.Fatalf("GetProviderConnections: %v", err)
	}
	if len(conns) != 0 {
		t.Errorf("%d connections written for a failed lease", len(conns))
	}
}

// A relative data dir is refused before the broker is contacted: the endpoint
// would otherwise resolve a path that does not exist and report a confusing
// broker error.
func TestLeaseConnectRejectsRelativeDataDir(t *testing.T) {
	h := leaseTestHandler(t)
	body, _ := json.Marshal(map[string]string{"dataDir": "relative/path"})
	req := httptest.NewRequest(http.MethodPost, "/api/oauth/minimax-code/lease-connect", bytes.NewReader(body))
	req.SetPathValue("provider", "minimax-code")
	rec := httptest.NewRecorder()

	h.HandleLeaseConnect(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// Both minimax lanes support lease auth; other providers do not.
func TestLeaseProviders(t *testing.T) {
	for _, p := range []string{"minimax-code", "minimax-code-global"} {
		if !isLeaseProvider(p) {
			t.Errorf("%s should support lease auth", p)
		}
	}
	for _, p := range []string{"codex", "claude", ""} {
		if isLeaseProvider(p) {
			t.Errorf("%s should not support lease auth", p)
		}
	}
}

// findLeaseConnection keys idempotency on (provider, data dir). A key-based
// connection on the same provider and data dir must not be mistaken for an
// existing lease: the idempotency check would then report "reused" for a
// connection that never borrowed the sign-in, and the dashboard would hide
// the button that creates one.
func TestFindLeaseConnectionDistinguishesAuthType(t *testing.T) {
	h := leaseTestHandler(t)
	dataDir := t.TempDir()
	keyData, err := json.Marshal(map[string]any{"mcodeDataDir": dataDir})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := h.Repo.CreateProviderConnectionFull(
		"minimax-code-key-1", "minimax-code", "api_key", "Stored Key", nil, string(keyData),
	); err != nil {
		t.Fatalf("seed key connection: %v", err)
	}
	if got := h.findLeaseConnection("minimax-code", dataDir); got != "" {
		t.Errorf("an api_key connection was reported as a lease: %q", got)
	}
	leaseData, err := json.Marshal(map[string]any{"mcodeDataDir": dataDir})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := h.Repo.CreateProviderConnectionFull(
		"minimax-code-lease-1", "minimax-code", "mcode-lease", "Desktop App", nil, string(leaseData),
	); err != nil {
		t.Fatalf("seed lease connection: %v", err)
	}
	if got := h.findLeaseConnection("minimax-code", dataDir); got != "minimax-code-lease-1" {
		t.Errorf("lease connection = %q, want minimax-code-lease-1", got)
	}
}

// readAll drains a body of any size, including one that arrives in pieces.
func TestReadAll(t *testing.T) {
	got := readAll(httptest.NewRequest(http.MethodPost, "/", strings.NewReader("hello world")))
	if string(got) != "hello world" {
		t.Errorf("readAll = %q", got)
	}
	if got := readAll(httptest.NewRequest(http.MethodPost, "/", nil)); len(got) != 0 {
		t.Errorf("readAll(nil body) = %q", got)
	}
}
