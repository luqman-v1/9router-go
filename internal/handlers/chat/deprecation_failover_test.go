package chat

import (
	json "encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

// setupDeprecatedModelCombo points a mock provider at an upstream that refuses
// one model with the HTTP 410 a provider retires models with, and serves the
// next. The combo lists the dead model first, so a gateway that fails over
// reaches the live one and a gateway that does not, returns 410.
func setupDeprecatedModelCombo(t *testing.T, deadBody string) (*ChatHandler, *db.Repo, func()) {
	t.Helper()
	database, cleanupDB := setupChatTestDB(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/models") {
			// The catalogue still lists the retired model: absence from a
			// catalogue is not evidence of death, and a sync must not clear a
			// badge the upstream 410 actually earned.
			w.Write([]byte(`{"data":[{"id":"deepseek-chat"},{"id":"qwen3-32b"}]}`))
			return
		}
		var req struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(readAll(r), &req)
		if req.Model == "deepseek-chat" {
			w.WriteHeader(http.StatusGone)
			w.Write([]byte(deadBody))
			return
		}
		w.Write([]byte(`{"id":"chatcmpl-alive","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"served by the live model"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":7}}`))
	}))

	if _, err := database.Exec(`DELETE FROM providerConnections WHERE id = 'conn-dead'`); err != nil {
		t.Fatalf("clear seeded connections: %v", err)
	}
	data, _ := json.Marshal(map[string]any{"apiKey": "sk-mock-key", "baseUrl": upstream.URL})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-dead', 'deepseek', 'apikey', 'DeepSeek Mock', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(data)); err != nil {
		t.Fatalf("insert connection: %v", err)
	}
	// A second account on the same provider, so the combo has another seat to
	// reach after the deprecation failover excludes the first.
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-live', 'deepseek', 'apikey', 'DeepSeek Live', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(data)); err != nil {
		t.Fatalf("insert second connection: %v", err)
	}

	comboModels, _ := json.Marshal([]string{"deepseek/deepseek-chat", "deepseek/qwen3-32b"})
	if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES
		('combo-deprecated', 'combo-deprecated', 'fallback', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(comboModels)); err != nil {
		t.Fatalf("insert combo: %v", err)
	}

	repo := db.NewRepo(database)
	return NewChatHandler(repo), repo, func() {
		upstream.Close()
		cleanupDB()
	}
}

// readAll drains a request body for the mock upstream.
func readAll(r *http.Request) []byte {
	if r.Body == nil {
		return nil
	}
	buf := make([]byte, 0, 512)
	tmp := make([]byte, 512)
	for {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			return buf
		}
	}
}

// The contract from the issue: a combo whose first model was retired upstream
// serves the turn from the next model instead of answering HTTP 410.
func TestComboFailsOverPastDeprecatedModel(t *testing.T) {
	handler, repo, cleanup := setupDeprecatedModelCombo(t,
		`{"error":{"type":"ModelDeprecated","message":"deepseek-chat was retired, use qwen3-32b instead","successor":"qwen3-32b"}}`)
	defer cleanup()

	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"combo-deprecated","messages":[{"role":"user","content":"hi"}],"stream":false}`))
	rec := httptest.NewRecorder()
	handler.HandleChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the combo must fail over, not surface the 410); body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "served by the live model") {
		t.Errorf("body = %s, want the next combo model's response", rec.Body.String())
	}

	dep, err := repo.GetModelDeprecation("deepseek", "deepseek-chat")
	if err != nil {
		t.Fatalf("the retired model must be recorded so the dashboard can badge it: %v", err)
	}
	if dep.Status != providers.DeprecationGone {
		t.Errorf("Status = %q, want %q", dep.Status, providers.DeprecationGone)
	}
	if dep.Successor != "qwen3-32b" {
		t.Errorf("Successor = %q, want %q", dep.Successor, "qwen3-32b")
	}
}

// A 410 that is not about the model (an expired upstream session) must not
// blacklist it: the badge would outlive the real cause and the model is fine.
func TestNonModelGoneErrorDoesNotRecordDeprecation(t *testing.T) {
	handler, repo, cleanup := setupDeprecatedModelCombo(t,
		`{"error":{"type":"session_expired","message":"session expired, re-claim it"}}`)
	defer cleanup()

	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"combo-deprecated","messages":[{"role":"user","content":"hi"}],"stream":false}`))
	rec := httptest.NewRecorder()
	handler.HandleChatCompletions(rec, req)

	// The combo still advances (any error does), so the turn is served either
	// way. What must not happen is the badge.
	if _, err := repo.GetModelDeprecation("deepseek", "deepseek-chat"); !errors.Is(err, db.ErrModelNotDeprecated) {
		t.Errorf("GetModelDeprecation err = %v, want ErrModelNotDeprecated", err)
	}
}

// A model that serves a request again must lose its badge, or a provider that
// restores a model leaves a permanent "Deprecated" on a working model.
func TestServedRequestClearsDeprecation(t *testing.T) {
	handler, repo, cleanup := setupDeprecatedModelCombo(t,
		`{"error":{"type":"ModelDeprecated","message":"deepseek-chat was retired"}}`)
	defer cleanup()

	if err := repo.RecordModelDeprecation(providers.ModelDeprecation{
		Provider: "deepseek",
		Model:    "qwen3-32b",
		Status:   providers.DeprecationGone,
	}); err != nil {
		t.Fatalf("seed deprecation: %v", err)
	}

	// A direct request that succeeds has to retract the recorded badge.
	err := handler.handleAccountFallback(t.Context(), httptest.NewRecorder(), "deepseek", "qwen3-32b", "",
		[]byte(`{"model":"qwen3-32b","messages":[{"role":"user","content":"hi"}]}`), false, false, "/v1/chat/completions")
	if err != nil {
		t.Fatalf("forward to the live model: %v", err)
	}

	if _, err := repo.GetModelDeprecation("deepseek", "qwen3-32b"); !errors.Is(err, db.ErrModelNotDeprecated) {
		t.Errorf("a served request must clear the badge, got err = %v", err)
	}
}

// A provider with a single account is the case the failover exists for. If
// the deprecation branch excluded that connection from the pass, the next
// combo entry would have no account left to try and the client would get the
// 410 anyway — model correctly badged, request still dead.
func TestDeprecationFailoverWithSingleAccount(t *testing.T) {
	database, cleanupDB := setupChatTestDB(t)
	defer cleanupDB()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(readAll(r), &req)
		w.Header().Set("Content-Type", "application/json")
		if req.Model == "deepseek-chat" {
			w.WriteHeader(http.StatusGone)
			w.Write([]byte(`{"error":{"type":"ModelDeprecated","message":"retired"}}`))
			return
		}
		w.Write([]byte(`{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"second model answered"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("clear seeded connections: %v", err)
	}
	data, _ := json.Marshal(map[string]any{"apiKey": "sk-mock-key", "baseUrl": upstream.URL})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-only', 'deepseek', 'apikey', 'Only Account', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(data)); err != nil {
		t.Fatalf("insert connection: %v", err)
	}
	comboModels, _ := json.Marshal([]string{"deepseek/deepseek-chat", "deepseek/qwen3-32b"})
	if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES
		('combo-single', 'combo-single', 'fallback', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(comboModels)); err != nil {
		t.Fatalf("insert combo: %v", err)
	}

	handler := NewChatHandler(db.NewRepo(database))
	rec := httptest.NewRecorder()
	handler.HandleChatCompletions(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"combo-single","messages":[{"role":"user","content":"hi"}],"stream":false}`)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; a single-account provider must still fail over. body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "second model answered") {
		t.Errorf("body = %s, want the next model's response", rec.Body.String())
	}
}
