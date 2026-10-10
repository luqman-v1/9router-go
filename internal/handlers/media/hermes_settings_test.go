package media

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("Unmarshal body %q: %v", rec.Body.String(), err)
	}
	return out
}

func postSettings(t *testing.T, h *HermesHandler, body any) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/cli-tools/hermes-settings", strings.NewReader(string(payload)))
	rec := httptest.NewRecorder()
	h.HandlePost(rec, req)
	return rec
}

func TestHermesGet_InvalidProfileIs400(t *testing.T) {
	hermesTestHome(t)
	h := NewHermesHandler()

	req := httptest.NewRequest(http.MethodGet, "/api/cli-tools/hermes-settings?profile=..%2Fsecrets", nil)
	rec := httptest.NewRecorder()
	h.HandleGet(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %q)", rec.Code, rec.Body.String())
	}
}

func TestHermesGet_MissingNamedProfileIs404(t *testing.T) {
	root := hermesTestHome(t)
	h := NewHermesHandler()

	req := httptest.NewRequest(http.MethodGet, "/api/cli-tools/hermes-settings?profile=ghost", nil)
	rec := httptest.NewRecorder()
	h.HandleGet(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %q)", rec.Code, rec.Body.String())
	}
	// The default profile must stay reachable even though the named one is not:
	// a missing profile is a dashboard-selection problem, not a missing install.
	if err := os.WriteFile(filepath.Join(root, "config.yaml"), []byte("telemetry: false\n"), 0o644); err != nil {
		t.Fatalf("seed default config: %v", err)
	}
	req2 := httptest.NewRequest(http.MethodGet, "/api/cli-tools/hermes-settings", nil)
	rec2 := httptest.NewRecorder()
	h.HandleGet(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("default profile status = %d, want 200 (body %q)", rec2.Code, rec2.Body.String())
	}
}

func TestHermesPost_ApplyIsScopedToOneProfile(t *testing.T) {
	root := hermesTestHome(t)
	workDir := writeProfile(t, root, "work", "")
	h := NewHermesHandler()

	rec := postSettings(t, h, map[string]any{
		"profile": "work",
		"baseUrl": "http://127.0.0.1:20130",
		"apiKey":  "sk-test-key",
		"model":   "deepseek/deepseek-chat",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}

	work := readFileString(t, filepath.Join(workDir, "config.yaml"))
	if !strings.Contains(work, `default: "deepseek/deepseek-chat"`) {
		t.Errorf("work config missing model block:\n%s", work)
	}
	if !strings.Contains(work, `base_url: "http://127.0.0.1:20130/v1"`) {
		t.Errorf("base_url not normalized to /v1:\n%s", work)
	}
	if env := readFileString(t, filepath.Join(workDir, ".env")); !strings.Contains(env, "OPENAI_API_KEY=sk-test-key") {
		t.Errorf("work .env missing key: %q", env)
	}

	// The default home must be untouched — applying to one profile is not a
	// reason to configure every profile.
	if _, err := os.Stat(filepath.Join(root, "config.yaml")); !os.IsNotExist(err) {
		t.Fatalf("default profile was written by a scoped apply (err=%v)", err)
	}
}

func TestHermesPost_KeepsForeignConfigInSameFile(t *testing.T) {
	root := hermesTestHome(t)
	dir := writeProfile(t, root, "work", "telemetry: false\ndelegation:\n  model: \"openrouter/x\"\n  provider: \"openrouter\"\n")
	h := NewHermesHandler()

	// Only the default slot is requested: a scoped apply writes what it was
	// asked to write and leaves every other block in the file alone. Handing
	// a foreign model to the delegation slot is an explicit request, and the
	// written block is 9router's to own from then on.
	rec := postSettings(t, h, map[string]any{
		"profile":    "work",
		"baseUrl":    "http://127.0.0.1:20130/v1",
		"selections": []map[string]string{{"role": "default", "model": "deepseek/chat"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}

	got := readFileString(t, filepath.Join(dir, "config.yaml"))
	if !strings.Contains(got, "telemetry: false") {
		t.Errorf("unrelated key lost:\n%s", got)
	}
	if !strings.Contains(got, `model: "openrouter/x"`) {
		t.Errorf("foreign delegation block rewritten:\n%s", got)
	}
}

func TestHermesDelete_KeepsBlocksOwnedByOtherProviders(t *testing.T) {
	root := hermesTestHome(t)
	dir := writeProfile(t, root, "work", strings.Join([]string{
		"telemetry: false",
		"model:",
		`  default: "deepseek/chat"`,
		`  provider: "openrouter"`,
		`  base_url: "https://openrouter.ai/api/v1"`,
		"delegation:",
		`  model: "deepseek/sub"`,
		`  provider: "custom"`,
		`  base_url: "http://127.0.0.1:20130/v1"`,
		"auxiliary:",
		"  compression:",
		`    provider: "custom"`,
		`    model: "deepseek/c"`,
		"  review:",
		`    provider: "openrouter"`,
		`    model: "openrouter/r"`,
		"",
	}, "\n"))
	h := NewHermesHandler()

	req := httptest.NewRequest(http.MethodDelete, "/api/cli-tools/hermes-settings", strings.NewReader(`{"profile":"work"}`))
	rec := httptest.NewRecorder()
	h.HandleDelete(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}

	body := decodeJSON(t, rec)
	removed, _ := body["removed"].(map[string]any)
	if removed["model"] != false {
		t.Errorf("foreign model block must not be removed, got %v", removed["model"])
	}
	if removed["delegation"] != true {
		t.Errorf("custom delegation must be removed, got %v", removed["delegation"])
	}
	aux, _ := removed["auxiliary"].([]any)
	if len(aux) != 1 || aux[0] != "compression" {
		t.Errorf("removed auxiliary = %v, want [compression]", aux)
	}

	got := readFileString(t, filepath.Join(dir, "config.yaml"))
	// The fixture's own quoting is reproduced exactly: the surviving file is
	// the input minus our blocks, not a re-serialized document.
	for _, want := range []string{"telemetry: false", `provider: "openrouter"`, `default: "deepseek/chat"`, "  review:"} {
		if !strings.Contains(got, want) {
			t.Errorf("user-owned content %q was destroyed:\n%s", want, got)
		}
	}
	for _, gone := range []string{`model: "deepseek/sub"`, `model: "deepseek/c"`} {
		if strings.Contains(got, gone) {
			t.Errorf("9router block %q survived the reset:\n%s", gone, got)
		}
	}
}

func TestHermesDelete_MissingConfigIsNotAnError(t *testing.T) {
	hermesTestHome(t)
	h := NewHermesHandler()

	req := httptest.NewRequest(http.MethodDelete, "/api/cli-tools/hermes-settings", nil)
	rec := httptest.NewRecorder()
	h.HandleDelete(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if msg := decodeJSON(t, rec)["message"]; msg != "No config file to reset" {
		t.Fatalf("message = %v", msg)
	}
}

func TestHermesPost_ApplyToAllReportsPerProfile(t *testing.T) {
	root := hermesTestHome(t)
	// routed: already on 9router, keeps its own model
	writeProfile(t, root, "routed", "model:\n  default: \"deepseek/keepme\"\n  provider: \"custom\"\n  base_url: \"https://old.example.com/v1\"\n")
	// foreign: someone else's provider, must be reported and left alone
	writeProfile(t, root, "foreign", "model:\n  default: \"openrouter/x\"\n  provider: \"openrouter\"\n  base_url: \"https://openrouter.ai/api/v1\"\n")
	// fresh: no model yet, gets the requested one
	writeProfile(t, root, "fresh", "")
	h := NewHermesHandler()

	rec := postSettings(t, h, map[string]any{
		"applyToAll": true,
		"baseUrl":    "http://127.0.0.1:20130",
		"apiKey":     "sk-test-key",
		"model":      "deepseek/chat",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	body := decodeJSON(t, rec)
	if body["bulk"] != true {
		t.Fatalf("bulk = %v, want true", body["bulk"])
	}
	// default, routed and fresh are updated; foreign is reported as skipped.
	if body["updated"] != float64(3) {
		t.Errorf("updated = %v, want 3", body["updated"])
	}
	if body["skipped"] != float64(1) {
		t.Errorf("skipped = %v, want 1", body["skipped"])
	}

	byProfile := map[string]map[string]any{}
	results, _ := body["results"].([]any)
	for _, raw := range results {
		entry, _ := raw.(map[string]any)
		name, _ := entry["profile"].(string)
		byProfile[name] = entry
	}
	if got := byProfile["routed"]["status"]; got != "updated" {
		t.Errorf("routed status = %v, want updated", got)
	}
	if got := byProfile["routed"]["model"]; got != "deepseek/keepme" {
		t.Errorf("routed model = %v, want its own deepseek/keepme", got)
	}
	if got := byProfile["foreign"]["status"]; got != "skipped" {
		t.Errorf("foreign status = %v, want skipped", got)
	}
	if reason, _ := byProfile["foreign"]["reason"].(string); !strings.Contains(reason, "openrouter") {
		t.Errorf("foreign reason = %q, want it to name the blocking provider", reason)
	}
	if got := byProfile["fresh"]["status"]; got != "updated" {
		t.Errorf("fresh status = %v, want updated", got)
	}

	routedYAML := readFileString(t, filepath.Join(root, "profiles", "routed", "config.yaml"))
	if !strings.Contains(routedYAML, `base_url: "http://127.0.0.1:20130/v1"`) {
		t.Errorf("routed endpoint not refreshed:\n%s", routedYAML)
	}
	foreignYAML := readFileString(t, filepath.Join(root, "profiles", "foreign", "config.yaml"))
	if strings.Contains(foreignYAML, "127.0.0.1") {
		t.Errorf("a foreign profile was rewritten:\n%s", foreignYAML)
	}
}

func TestHermesProfiles_EndpointListsEveryProfile(t *testing.T) {
	root := hermesTestHome(t)
	writeProfile(t, root, "work", "")
	h := NewHermesHandler()

	req := httptest.NewRequest(http.MethodGet, "/api/cli-tools/hermes-profiles", nil)
	rec := httptest.NewRecorder()
	h.HandleProfiles(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	profiles, _ := decodeJSON(t, rec)["profiles"].([]any)
	if len(profiles) != 2 {
		t.Fatalf("got %d profiles, want 2", len(profiles))
	}
	first, _ := profiles[0].(map[string]any)
	if first["name"] != "default" || first["isDefault"] != true {
		t.Errorf("default entry = %+v", first)
	}
}
