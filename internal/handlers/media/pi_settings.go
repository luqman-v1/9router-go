package media

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"encoding/json/jsontext"
	json "encoding/json/v2"
)

// Pi settings API. Port of upstream
// `src/app/api/cli-tools/pi-settings/route.js`, including the POST merge that
// commit 342ecb0d fixed there: previously saved fields of the 9router block
// (a stored key, a custom `api`) survive an apply instead of being dropped.
//
// The config is the pi coding agent's models.json, read and rewritten whole;
// everything else in the file belongs to pi or the user and is preserved.

const piProviderName = "9router"

// piPlaceholderKey is the fake key old clients sent when they could not read
// the stored one back. It was never a real credential and must never be
// written into a config.
const piPlaceholderKey = "sk_9router"

// PiHandler serves the pi settings API. repo backs the API-key fallback for a
// caller that sends none.
type PiHandler struct {
	repo *db.Repo
}

// NewPiHandler returns a PiHandler reading dashboard keys from repo.
func NewPiHandler(repo *db.Repo) *PiHandler { return &PiHandler{repo: repo} }

// piModelsPaths are the two locations pi reads, in upstream precedence order:
// the agent-scoped file when it exists, else the root one.
func piModelsPaths() (agent, root string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", ""
	}
	return filepath.Join(home, ".pi", "agent", "models.json"), filepath.Join(home, ".pi", "models.json")
}

// resolvePiModelsPath picks the file a write targets: an existing agent-scoped
// file wins, then an existing root file, then the agent path pi reads first.
func resolvePiModelsPath() string {
	agent, root := piModelsPaths()
	if _, err := os.Stat(agent); err == nil {
		return agent
	}
	if _, err := os.Stat(root); err == nil {
		return root
	}
	return agent
}

// piInstalled reports whether pi is on PATH or has left a models.json behind,
// so an install without a PATH entry is still recognised (upstream
// checkPiInstalled).
func piInstalled() bool {
	if _, err := exec.LookPath("pi"); err == nil {
		return true
	}
	agent, root := piModelsPaths()
	_, err := os.Stat(agent)
	if err == nil {
		return true
	}
	_, err = os.Stat(root)
	return err == nil
}

// readPiConfig parses models.json whole. An unreadable or unparsable file
// reads as "no config", which is how upstream readConfig behaves.
func readPiConfig() map[string]any {
	agent, root := piModelsPaths()
	for _, path := range []string{agent, root} {
		if path == "" {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var config map[string]any
		if json.Unmarshal(raw, &config) == nil && config != nil {
			return config
		}
	}
	return nil
}

// piHas9RouterConfig reports whether any provider block points at a local
// 9router: the named block with a baseUrl, or any block carrying one of the
// two default gateway ports (20128 upstream's, 20130 this gateway's).
func piHas9RouterConfig(config map[string]any) bool {
	providers, _ := config["providers"].(map[string]any)
	if p, ok := providers[piProviderName].(map[string]any); ok {
		if b, _ := p["baseUrl"].(string); strings.TrimSpace(b) != "" {
			return true
		}
	}
	for _, v := range providers {
		p, ok := v.(map[string]any)
		if !ok {
			continue
		}
		b, _ := p["baseUrl"].(string)
		if strings.Contains(b, "20128") || strings.Contains(b, "20130") {
			return true
		}
	}
	return false
}

// HandleGet serves GET /api/cli-tools/pi-settings.
func (h *PiHandler) HandleGet(w http.ResponseWriter, r *http.Request) {
	if !piInstalled() {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"installed": false,
			"config":    nil,
			"message":   "Pi CLI is not installed",
		})
		return
	}
	config := readPiConfig()
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"installed":  true,
		"config":     config,
		"has9Router": piHas9RouterConfig(config),
		"configPath": resolvePiModelsPath(),
	})
}

// piSettingsRequest is the POST body. models entries arrive as ids or as
// objects, which piModelList normalizes.
type piSettingsRequest struct {
	BaseURL string `json:"baseUrl"`
	APIKey  string `json:"apiKey"`
	Model   string `json:"model"`
	Models  []any  `json:"models"`
}

// HandlePost serves POST /api/cli-tools/pi-settings — writes the 9router
// provider block into models.json, merging over the previously saved one.
func (h *PiHandler) HandlePost(w http.ResponseWriter, r *http.Request) {
	var req piSettingsRequest
	if err := json.UnmarshalRead(r.Body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	baseURL := strings.TrimSpace(req.BaseURL)
	if baseURL == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "baseUrl is required")
		return
	}
	if !strings.HasSuffix(baseURL, "/v1") {
		baseURL += "/v1"
	}

	configPath := resolvePiModelsPath()
	if configPath == "" {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to update pi settings")
		return
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to update pi settings")
		return
	}

	existing := readPiConfig()
	if existing == nil {
		existing = map[string]any{}
	}
	providers, _ := existing["providers"].(map[string]any)
	if providers == nil {
		providers = map[string]any{}
	}

	previous, _ := providers[piProviderName].(map[string]any)
	if previous == nil {
		previous = map[string]any{}
	}
	updated := maps.Clone(previous)
	updated["baseUrl"] = baseURL
	updated["apiKey"] = h.resolveAPIKey(req.APIKey, previous["apiKey"])
	if api, _ := updated["api"].(string); strings.TrimSpace(api) == "" {
		updated["api"] = "openai-completions"
	}
	updated["models"] = piModelList(req)

	providers[piProviderName] = updated
	existing["providers"] = providers
	if err := writePiConfig(configPath, existing); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to update pi settings")
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"message":    "Pi settings applied! Use /model in Pi to select the 9Router model.",
		"configPath": configPath,
	})
}

// HandleDelete serves DELETE /api/cli-tools/pi-settings — removes the 9router
// block and leaves every other provider untouched.
func (h *PiHandler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	configPath := resolvePiModelsPath()
	raw, err := os.ReadFile(configPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to reset pi settings")
			return
		}
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "No config file to reset",
		})
		return
	}
	var existing map[string]any
	if json.Unmarshal(raw, &existing) != nil || existing == nil {
		// A file pi cannot parse back is not ours to rewrite; the block the
		// dashboard would remove is not in there.
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "9Router removed from Pi",
		})
		return
	}

	providers, _ := existing["providers"].(map[string]any)
	if block, ok := providers[piProviderName]; ok && block != nil {
		delete(providers, piProviderName)
		if len(providers) == 0 {
			delete(existing, "providers")
		}
		if err := writePiConfig(configPath, existing); err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to reset pi settings")
			return
		}
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "9Router removed from Pi",
	})
}

// piModelList normalizes the requested models: a bare id becomes a full entry,
// an object keeps the caller's values where set, and the single `model` field
// fills in when no list arrived (upstream POST defaults).
func piModelList(req piSettingsRequest) []map[string]any {
	const (
		defaultModelID    = "provider/model-id"
		defaultContextWin = 128000
		defaultMaxTokens  = 16384
	)
	out := make([]map[string]any, 0, len(req.Models))
	for _, m := range req.Models {
		switch entry := m.(type) {
		case string:
			if id := strings.TrimSpace(entry); id != "" {
				out = append(out, piModelEntry(id, id, defaultContextWin, defaultMaxTokens))
			}
		case map[string]any:
			id, _ := entry["id"].(string)
			if strings.TrimSpace(id) == "" {
				id = defaultModelID
			}
			name, _ := entry["name"].(string)
			if strings.TrimSpace(name) == "" {
				name = id
			}
			contextWindow := piIntOr(entry["contextWindow"], defaultContextWin)
			maxTokens := piIntOr(entry["maxTokens"], defaultMaxTokens)
			out = append(out, piModelEntry(id, name, contextWindow, maxTokens))
		}
	}
	if len(out) > 0 {
		return out
	}
	id := strings.TrimSpace(req.Model)
	if id == "" {
		id = defaultModelID
	}
	return []map[string]any{piModelEntry(id, id, defaultContextWin, defaultMaxTokens)}
}

func piModelEntry(id, name string, contextWindow, maxTokens int) map[string]any {
	return map[string]any{
		"id":            id,
		"name":          name,
		"contextWindow": contextWindow,
		"maxTokens":     maxTokens,
	}
}

// piIntOr reads a JSON number, falling back when absent or not a number.
func piIntOr(v any, fallback int) int {
	if n, ok := v.(int64); ok {
		return int(n)
	}
	if n, ok := v.(float64); ok {
		return int(n)
	}
	return fallback
}

// resolveAPIKey ports resolveCliApiKey (resolveApiKey.js): the caller's key,
// then the previously stored one, then the first active dashboard key, then ""
// — a deployment without requireApiKey works with no key at all, and the
// placeholder is never written.
func (h *PiHandler) resolveAPIKey(caller, stored any) string {
	if key := strings.TrimSpace(strAny(caller)); key != "" && key != piPlaceholderKey {
		return key
	}
	if key := strings.TrimSpace(strAny(stored)); key != "" && key != piPlaceholderKey {
		return key
	}
	if h.repo == nil {
		return ""
	}
	keys, err := h.repo.GetApiKeys()
	if err != nil {
		return ""
	}
	for _, k := range keys {
		if k.IsActive == 1 {
			if key := strings.TrimSpace(k.Key); key != "" {
				return key
			}
		}
	}
	return ""
}

// strAny reads the string a config value decodes to, "" otherwise.
func strAny(v any) string {
	s, _ := v.(string)
	return s
}

// writePiConfig marshals with the two-space indent pi's own tooling writes and
// hands the file back with the same permissions upstream writes it with.
func writePiConfig(path string, config map[string]any) error {
	out, err := json.Marshal(config, jsontext.WithIndent("  "))
	if err != nil {
		return fmt.Errorf("marshal pi models.json: %w", err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
