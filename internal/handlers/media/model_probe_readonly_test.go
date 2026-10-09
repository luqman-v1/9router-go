// A dashboard model probe reaches the media handlers with a probe-tagged
// context and must observe production state without changing it. The failure
// path already refused to lock accounts on a probe; the success path had the
// mirror defect and erased the very backoff a real failure had recorded, so
// one click of "Test" undid production cooldown state. These two cases pin
// both directions of the gate: a probe keeps the lock, real traffic clears it.
package media

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	json "encoding/json/v2"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/handlers/chat"
)

// setupNvidiaTTS seeds one nvidia connection pointed at mock and returns the
// handler, the repo and the live connection.
func setupNvidiaTTS(t *testing.T) (*MediaHandler, *db.Repo, *chat.ModelInfo, *chat.ConnectionData, *http.Request) {
	t.Helper()

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/wav")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(mock.Close)

	database, cleanup := setupMultimodalTestDB(t)
	t.Cleanup(cleanup)

	seed, _ := json.Marshal(map[string]any{"apiKey": "test-nv-key", "baseUrl": mock.URL})
	if _, err := database.Exec(`INSERT INTO providerConnections
		(id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		VALUES ('conn-nv', 'nvidia', 'apikey', 'Nvidia Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(seed)); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	conn, err := repo.GetProviderConnectionByID("conn-nv")
	if err != nil || conn == nil {
		t.Fatalf("fetch conn: %v", err)
	}
	var cd chat.ConnectionData
	if err := json.Unmarshal([]byte(conn.Data), &cd); err != nil {
		t.Fatalf("parse conn data: %v", err)
	}

	modelInfo := &chat.ModelInfo{Provider: "nvidia", Model: "fastpitch/alloy"}
	req := httptest.NewRequest("POST", "/v1/audio/speech", strings.NewReader(`{}`))
	return handler, repo, modelInfo, &cd, req
}

func dialNvidiaTTS(t *testing.T, h *MediaHandler, repo *db.Repo, mi *chat.ModelInfo, cd *chat.ConnectionData, req *http.Request) {
	t.Helper()
	conn, err := repo.GetProviderConnectionByID("conn-nv")
	if err != nil || conn == nil {
		t.Fatalf("fetch conn: %v", err)
	}
	rec := httptest.NewRecorder()
	status, msg, done := h.tryNvidiaTTSConn(rec, req, []byte(`{}`), mi, "probe", "alloy", "json", conn, cd)
	if !done || status != 0 {
		t.Fatalf("expected success, got status=%d msg=%s body=%s", status, msg, rec.Body.String())
	}
}

// A PROBE must not clear a production model lock, even when the turn succeeds.
func TestProbeTTS_SuccessKeepsProductionLock(t *testing.T) {
	h, repo, mi, cd, req := setupNvidiaTTS(t)
	if err := repo.LockConnectionModel("conn-nv", "fastpitch/alloy", 3600, 2); err != nil {
		t.Fatalf("lock: %v", err)
	}

	dialNvidiaTTS(t, h, repo, mi, cd,
		req.WithContext(handlerutil.WithProbeContext(req.Context())))

	conn, _ := repo.GetProviderConnectionByID("conn-nv")
	if locked, _ := repo.IsConnectionModelLocked("conn-nv", "fastpitch/alloy"); !locked {
		t.Errorf("probe cleared the production model lock: %s", conn.Data)
	}
}

// A REAL request must still clear it — the gate must not be a blanket.
func TestProbeTTS_ProductionSuccessStillUnlocks(t *testing.T) {
	h, repo, mi, cd, req := setupNvidiaTTS(t)
	if err := repo.LockConnectionModel("conn-nv", "fastpitch/alloy", 3600, 2); err != nil {
		t.Fatalf("lock: %v", err)
	}

	dialNvidiaTTS(t, h, repo, mi, cd, req)

	conn, _ := repo.GetProviderConnectionByID("conn-nv")
	if locked, _ := repo.IsConnectionModelLocked("conn-nv", "fastpitch/alloy"); locked {
		t.Errorf("REGRESSION: production success no longer clears the lock: %s", conn.Data)
	}
}