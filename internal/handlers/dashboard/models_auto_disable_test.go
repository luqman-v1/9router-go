package dashboard

import (
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"
)

// postAutoDisable sends one POST /api/models/auto-disable and decodes the
// result, failing the test on any non-200.
func postAutoDisable(t *testing.T, router http.Handler, body map[string]any) autoDisableResult {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/models/auto-disable", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("auto-disable = %d: %s", rec.Code, rec.Body.String())
	}
	var res autoDisableResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	return res
}

// disabledIDsFor reads one provider's stored disabled list straight from the
// repo, so the assertions check what was written rather than what the handler
// said it wrote.
func disabledIDsFor(t *testing.T, repo interface {
	GetKVScope(scope string) (map[string]string, error)
}, provider string) []string {
	t.Helper()
	kv, err := repo.GetKVScope("disabledModels")
	if err != nil {
		t.Fatalf("read disabledModels: %v", err)
	}
	var ids []string
	if raw, ok := kv[provider]; ok {
		if err := json.Unmarshal([]byte(raw), &ids); err != nil {
			t.Fatalf("decode disabled list: %v", err)
		}
	}
	return ids
}

// TestAutoDisableModels_MergesIntoExisting covers the reason the endpoint
// exists: a caller holding only "these just failed" must not have to
// read-modify-write the whole list, and must not drop what is already there.
func TestAutoDisableModels_MergesIntoExisting(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	// Two models already off, written the ordinary way.
	if err := repo.SetKV("disabledModels", "vsllm", `["already-off","also-off"]`); err != nil {
		t.Fatalf("seed disabled: %v", err)
	}

	res := postAutoDisable(t, router, map[string]any{
		"provider": "vsllm",
		"models":   []string{"dead-one", "already-off", "dead-two"},
	})

	if len(res.Disabled) != 2 {
		t.Errorf("Disabled = %v, want the 2 new ids", res.Disabled)
	}
	if len(res.AlreadyDisabled) != 1 || res.AlreadyDisabled[0] != "already-off" {
		t.Errorf("AlreadyDisabled = %v, want [already-off]", res.AlreadyDisabled)
	}
	if res.Total != 3 {
		t.Errorf("Total = %d, want 3", res.Total)
	}

	got := disabledIDsFor(t, repo, "vsllm")
	want := map[string]bool{"already-off": true, "also-off": true, "dead-one": true, "dead-two": true}
	if len(got) != len(want) {
		t.Fatalf("stored list = %v, want %d entries", got, len(want))
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("unexpected id %q in stored list %v", id, got)
		}
	}
}

// TestAutoDisableModels_DryRunWritesNothing pins the preview path: the count
// has to be right, and the store has to be untouched.
func TestAutoDisableModels_DryRunWritesNothing(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	if err := repo.SetKV("disabledModels", "vsllm", `["kept"]`); err != nil {
		t.Fatalf("seed disabled: %v", err)
	}

	res := postAutoDisable(t, router, map[string]any{
		"provider": "vsllm",
		"models":   []string{"dead-one", "dead-two"},
		"dryRun":   true,
	})

	if !res.DryRun {
		t.Error("DryRun = false, want true")
	}
	if len(res.Disabled) != 2 {
		t.Errorf("Disabled = %v, want 2 previewed ids", res.Disabled)
	}
	got := disabledIDsFor(t, repo, "vsllm")
	if len(got) != 1 || got[0] != "kept" {
		t.Errorf("stored list = %v after a dry run, want [kept]", got)
	}
}

// TestAutoDisableModels_NoNewIdsIsANoOp covers the case where everything the
// caller named is already off: the row must not be rewritten, because a
// rewrite would reorder it and disturb a concurrent reader for no gain.
func TestAutoDisableModels_NoNewIdsIsANoOp(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	if err := repo.SetKV("disabledModels", "vsllm", `["off-a","off-b"]`); err != nil {
		t.Fatalf("seed disabled: %v", err)
	}

	res := postAutoDisable(t, router, map[string]any{
		"provider": "vsllm",
		"models":   []string{"off-b", "off-a", "off-b"},
	})

	if len(res.Disabled) != 0 {
		t.Errorf("Disabled = %v, want none", res.Disabled)
	}
	if len(res.AlreadyDisabled) != 2 {
		t.Errorf("AlreadyDisabled = %v, want 2 (duplicates collapsed)", res.AlreadyDisabled)
	}
	got := disabledIDsFor(t, repo, "vsllm")
	if len(got) != 2 || got[0] != "off-a" || got[1] != "off-b" {
		t.Errorf("stored list = %v, want the original order [off-a off-b]", got)
	}
}

// TestAutoDisableModels_RejectsMissingProvider covers the validation path.
func TestAutoDisableModels_RejectsMissingProvider(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	raw, err := json.Marshal(map[string]any{"models": []string{"dead"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/models/auto-disable", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

// TestAutoDisableModels_CompatibleNodeKey covers the key spelling the custom
// provider panel uses: the disabled list is keyed by the node id, and this
// endpoint has to write under that same key or the gate in /v1/models will
// never see it.
func TestAutoDisableModels_CompatibleNodeKey(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	const nodeID = "openai-compatible-chat-vsllm"
	res := postAutoDisable(t, router, map[string]any{
		"provider": nodeID,
		"models":   []string{"dead-one"},
	})
	if len(res.Disabled) != 1 {
		t.Fatalf("Disabled = %v, want 1", res.Disabled)
	}

	kv, err := repo.GetKVScope("disabledModels")
	if err != nil {
		t.Fatalf("read scope: %v", err)
	}
	if _, ok := kv[nodeID]; !ok {
		t.Errorf("disabled list not written under node id %q; keys present: %v", nodeID, kv)
	}
}
