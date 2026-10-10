package media

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"9router/proxy/internal/handlerutil"
	json "encoding/json/v2"
)

// Hermes per-profile settings API. Port of upstream
// `src/app/api/cli-tools/hermes-settings/route.js`.
//
// Every response carries the profile it describes, because a dashboard with
// several profiles open needs to know which home it just wrote. `?profile=` is
// accepted everywhere the JSON body has a `profile` field, because the CLI
// quick-setup path reaches for the query string.

const (
	hermesAPIKeyEnv    = "OPENAI_API_KEY"
	hermesProviderName = "9router"
)

// HermesHandler serves the Hermes profile list and the profile-scoped
// settings API. Stateless: every value comes off disk per request.
type HermesHandler struct{}

// NewHermesHandler returns a HermesHandler.
func NewHermesHandler() *HermesHandler { return &HermesHandler{} }

// hermesSelection is one model slot the caller wants written.
type hermesSelection struct {
	Role  string `json:"role"`
	Model string `json:"model"`
}

// hermesSettingsRequest is the POST body.
type hermesSettingsRequest struct {
	Profile    string            `json:"profile"`
	BaseURL    string            `json:"baseUrl"`
	APIKey     string            `json:"apiKey"`
	Model      string            `json:"model"`
	ApplyToAll any               `json:"applyToAll"`
	Selections []hermesSelection `json:"selections"`
}

// hermesSettings is the parsed config of one profile home.
type hermesSettings struct {
	Model      *hermesModelBlock        `json:"model"`
	Delegation *hermesDelegationBlock   `json:"delegation"`
	Auxiliary  map[string]hermesAuxRole `json:"auxiliary"`
}

// hermesApplyResult is one line of the applyToAll report.
type hermesApplyResult struct {
	Profile string `json:"profile"`
	Status  string `json:"status"` // updated | skipped | failed
	Model   string `json:"model,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// HandleProfiles serves GET /api/cli-tools/hermes-profiles.
func (h *HermesHandler) HandleProfiles(w http.ResponseWriter, r *http.Request) {
	profiles, err := listHermesProfiles()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to list hermes profiles")
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"profiles": profiles})
}

// HandleGet serves GET /api/cli-tools/hermes-settings[?profile=].
func (h *HermesHandler) HandleGet(w http.ResponseWriter, r *http.Request) {
	// The profile is validated first: a bad name is a caller error whatever
	// the install state, and answering "not installed" would hide it.
	home, ok := resolveHermesHomeParam(w, r.URL.Query().Get("profile"))
	if !ok {
		return
	}
	if !hermesInstalled(home) {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"installed": false,
			"settings":  nil,
			"message":   "Hermes Agent is not installed",
		})
		return
	}
	yaml, err := readHermesText(home.ConfigPath)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to check hermes settings")
		return
	}
	settings := parseHermesSettings(yaml)
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"installed":  true,
		"profile":    home,
		"settings":   settings,
		"has9Router": settings.has9RouterConfig(),
		"configPath": home.ConfigPath,
	})
}

// HandlePost serves POST /api/cli-tools/hermes-settings — one profile, or
// every profile when applyToAll is set.
func (h *HermesHandler) HandlePost(w http.ResponseWriter, r *http.Request) {
	var req hermesSettingsRequest
	if err := json.UnmarshalRead(r.Body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	selections := req.selections()
	baseURL := strings.TrimSuffix(strings.TrimSpace(req.BaseURL), "/")
	if baseURL == "" || len(selections) == 0 {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "baseUrl and model are required")
		return
	}
	if !strings.HasSuffix(baseURL, "/v1") {
		baseURL += "/v1"
	}

	if truthyHermesFlag(req.ApplyToAll) || truthyHermesFlag(r.URL.Query().Get("applyToAll")) {
		h.applyToAllProfiles(w, baseURL, req.APIKey, defaultHermesModel(selections))
		return
	}

	home, ok := resolveHermesHomeParam(w, req.Profile)
	if !ok {
		return
	}
	yaml, err := readHermesText(home.ConfigPath)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to update hermes settings")
		return
	}
	for _, sel := range selections {
		yaml = upsertHermesSelection(yaml, sel.Role, sel.Model, baseURL)
	}
	if err := writeHermesConfig(home, yaml); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to update hermes settings")
		return
	}
	if err := writeHermesEnv(home, req.APIKey); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to update hermes settings")
		return
	}

	message := "Hermes settings applied successfully!"
	if !home.IsDefault {
		message = fmt.Sprintf("Hermes settings applied to profile %q!", home.Name)
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"message":    message,
		"profile":    home,
		"configPath": home.ConfigPath,
	})
}

// HandleDelete serves DELETE /api/cli-tools/hermes-settings — removes only
// the blocks 9router wrote, scoped to one profile.
func (h *HermesHandler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	home, ok := resolveHermesHomeParam(w, deleteProfileParam(r))
	if !ok {
		return
	}
	yaml, err := os.ReadFile(home.ConfigPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to reset hermes settings")
			return
		}
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"profile": home.Name,
			"message": "No config file to reset",
		})
		return
	}

	remaining, removed, kept := resetHermesConfig(string(yaml))
	if err := writeHermesConfig(home, remaining); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to reset hermes settings")
		return
	}

	scope := ""
	if !home.IsDefault {
		scope = fmt.Sprintf(" in profile %q", home.Name)
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"profile": home.Name,
		"removed": removed,
		"kept":    kept,
		"message": fmt.Sprintf("%s model blocks removed%s", hermesProviderName, scope),
	})
}

// deleteProfileParam reads the profile from the JSON body, falling back to the
// query string. A CLI reset sends an empty body, which means the default.
func deleteProfileParam(r *http.Request) string {
	var body struct {
		Profile string `json:"profile"`
	}
	if err := json.UnmarshalRead(r.Body, &body); err == nil && body.Profile != "" {
		return body.Profile
	}
	return r.URL.Query().Get("profile")
}

// applyToAllProfiles propagates the endpoint and API key across every profile.
//
// A profile already routed through 9router keeps its own model and only has
// the endpoint refreshed. A profile on another provider is reported, never
// rewritten. A profile with no model yet is wired to the model chosen in this
// request, which is what makes a freshly created profile usable in one click.
func (h *HermesHandler) applyToAllProfiles(w http.ResponseWriter, baseURL, apiKey, model string) {
	profiles, err := listHermesProfiles()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to update hermes settings")
		return
	}
	results := make([]hermesApplyResult, 0, len(profiles))
	updated := 0
	for _, profile := range profiles {
		result := applyToOneProfile(profile.Name, baseURL, apiKey, model)
		if result.Status == "updated" {
			updated++
		}
		results = append(results, result)
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": updated > 0,
		"bulk":    true,
		"results": results,
		"updated": updated,
		"skipped": len(results) - updated,
		"message": fmt.Sprintf("Updated endpoint on %d profile(s)", updated),
	})
}

// applyToOneProfile refreshes one profile and reports what happened to it.
// An unresolvable home (a name that stopped being valid between the listing
// and the write) is reported per profile, never fatal for the batch.
func applyToOneProfile(name, baseURL, apiKey, model string) hermesApplyResult {
	failed := func(reason error) hermesApplyResult {
		return hermesApplyResult{Profile: name, Status: "failed", Reason: reason.Error()}
	}
	home, err := resolveHermesHome(name)
	if err != nil {
		return failed(err)
	}
	yaml, err := readHermesText(home.ConfigPath)
	if err != nil {
		return failed(err)
	}
	main := parseHermesModelBlock(yaml)

	switch {
	case main.defaultModel() != nil && isCustomBlock(main.provider()):
		yaml = upsertModelBlock(yaml, buildModelBlock(*main.defaultModel(), baseURL))
		yaml = refreshCustomBlocks(yaml, baseURL)
		if err := writeHermesConfig(home, yaml); err != nil {
			return failed(err)
		}
		if err := writeHermesEnv(home, apiKey); err != nil {
			return failed(err)
		}
		return hermesApplyResult{Profile: name, Status: "updated", Model: *main.defaultModel()}

	case main.defaultModel() != nil:
		return hermesApplyResult{
			Profile: name,
			Status:  "skipped",
			Reason:  fmt.Sprintf("main model uses provider %q — configure this profile individually", derefOr(main.provider(), "unknown")),
		}
	case model != "":
		if err := writeHermesConfig(home, upsertModelBlock(yaml, buildModelBlock(model, baseURL))); err != nil {
			return failed(err)
		}
		if err := writeHermesEnv(home, apiKey); err != nil {
			return failed(err)
		}
		return hermesApplyResult{Profile: name, Status: "updated", Model: model}

	default:
		return hermesApplyResult{Profile: name, Status: "skipped", Reason: "no main model configured"}
	}
}

// refreshCustomBlocks re-points the delegation and auxiliary blocks a profile
// already routes through 9router, leaving foreign-provider blocks untouched.
func refreshCustomBlocks(yaml, baseURL string) string {
	if d := parseHermesDelegationBlock(yaml); d != nil && d.Model != nil && isCustomBlock(d.provider()) {
		yaml = upsertDelegationBlock(yaml, buildDelegationBlock(*d.Model, baseURL))
	}
	for role, cfg := range parseHermesAuxRoles(yaml) {
		if cfg.Model != nil && isCustomBlock(cfg.Provider) {
			yaml = upsertHermesAuxRole(yaml, role, buildAuxRoleBlock(role, *cfg.Model, baseURL))
		}
	}
	return yaml
}

// resetHermesConfig strips the blocks 9router owns and reports both lists.
// A block owned by another provider is this profile's own configuration and
// must survive a reset, so it is kept and reported instead of deleted.
func resetHermesConfig(yaml string) (remaining string, removed map[string]any, kept []string) {
	removed = map[string]any{"model": false, "delegation": false, "auxiliary": []string{}}
	kept = []string{}
	removedAux := []string{}

	if main := parseHermesModelBlock(yaml); main != nil {
		if isCustomBlock(main.provider()) {
			yaml = removeModelBlock(yaml)
			removed["model"] = true
		} else {
			kept = append(kept, "model")
		}
	}
	if d := parseHermesDelegationBlock(yaml); d != nil {
		if isCustomBlock(d.provider()) {
			yaml = removeDelegationBlock(yaml)
			removed["delegation"] = true
		} else {
			kept = append(kept, "delegation")
		}
	}
	roles := parseHermesAuxRoles(yaml)
	for _, role := range slices.Sorted(maps.Keys(roles)) {
		if isCustomBlock(roles[role].Provider) {
			yaml = removeHermesAuxRole(yaml, role)
			removedAux = append(removedAux, role)
		} else {
			kept = append(kept, role)
		}
	}
	removed["auxiliary"] = removedAux
	return strings.TrimLeft(yaml, "\n"), removed, kept
}

// parseHermesSettings reads every block of one config.yaml.
func parseHermesSettings(yaml string) hermesSettings {
	return hermesSettings{
		Model:      parseHermesModelBlock(yaml),
		Delegation: parseHermesDelegationBlock(yaml),
		Auxiliary:  parseHermesAuxRoles(yaml),
	}
}

// has9RouterConfig reports whether any block points at a local 9router.
func (s hermesSettings) has9RouterConfig() bool {
	if has9RouterConfig(s.Model.provider(), s.Model.baseURL()) || has9RouterConfig(s.Delegation.provider(), s.Delegation.baseURL()) {
		return true
	}
	for _, cfg := range s.Auxiliary {
		if has9RouterConfig(cfg.Provider, cfg.BaseURL) {
			return true
		}
	}
	return false
}

// selections normalizes the model slots: a bare `model` is the default role,
// which is what the CLI quick-setup sends.
func (r hermesSettingsRequest) selections() []hermesSelection {
	out := make([]hermesSelection, 0, len(r.Selections)+1)
	for _, sel := range r.Selections {
		if sel.Role != "" && strings.TrimSpace(sel.Model) != "" {
			out = append(out, hermesSelection{Role: sel.Role, Model: strings.TrimSpace(sel.Model)})
		}
	}
	if defaultHermesModel(out) == "" && strings.TrimSpace(r.Model) != "" {
		out = append(out, hermesSelection{Role: "default", Model: strings.TrimSpace(r.Model)})
	}
	return out
}

func defaultHermesModel(sels []hermesSelection) string {
	for _, sel := range sels {
		if sel.Role == "default" {
			return sel.Model
		}
	}
	return ""
}

// upsertHermesSelection writes one role block. `delegation` is a top-level
// block; everything else is an auxiliary role.
func upsertHermesSelection(yaml, role, model, baseURL string) string {
	switch role {
	case "default":
		return upsertModelBlock(yaml, buildModelBlock(model, baseURL))
	case "delegation":
		return upsertDelegationBlock(yaml, buildDelegationBlock(model, baseURL))
	default:
		return upsertHermesAuxRole(yaml, role, buildAuxRoleBlock(role, model, baseURL))
	}
}

// resolveHermesHomeParam resolves the target profile and answers 400/404
// itself when it cannot be used. A false return means the response is written.
func resolveHermesHomeParam(w http.ResponseWriter, profile string) (hermesHome, bool) {
	home, err := resolveHermesHome(profile)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return hermesHome{}, false
	}
	// Named profile homes are created by `hermes profile create`, never by us.
	// A missing one is 404, so the dashboard can fall back to default.
	if !home.IsDefault && !dirExists(home.Dir) {
		handlerutil.WriteJSONError(w, http.StatusNotFound, fmt.Sprintf("Hermes profile %q not found", home.Name))
		return hermesHome{}, false
	}
	return home, true
}


// writeHermesConfig writes config.yaml. The default home is created on demand;
// a named profile's directory is never fabricated, so a typo cannot leave an
// orphan directory behind that Hermes would later treat as a profile.
func writeHermesConfig(home hermesHome, yaml string) error {
	if home.IsDefault {
		if err := os.MkdirAll(home.Dir, 0o755); err != nil {
			return fmt.Errorf("writeHermesConfig mkdir: %w", err)
		}
	}
	if err := os.WriteFile(home.ConfigPath, []byte(yaml), 0o644); err != nil {
		return fmt.Errorf("writeHermesConfig %s: %w", home.ConfigPath, err)
	}
	return nil
}

// writeHermesEnv upserts only OPENAI_API_KEY. A profile .env also carries bot
// tokens and messaging-channel credentials that must stay untouched.
func writeHermesEnv(home hermesHome, apiKey string) error {
	if strings.TrimSpace(apiKey) == "" {
		return nil
	}
	if home.IsDefault {
		if err := os.MkdirAll(home.Dir, 0o755); err != nil {
			return fmt.Errorf("writeHermesEnv mkdir: %w", err)
		}
	}
	current, err := readHermesText(home.EnvPath)
	if err != nil {
		return err
	}
	env := upsertEnvVar(current, hermesAPIKeyEnv, apiKey)
	if err := os.WriteFile(home.EnvPath, []byte(env), 0o600); err != nil {
		return fmt.Errorf("writeHermesEnv %s: %w", home.EnvPath, err)
	}
	return nil
}

// hermesInstalled reports whether Hermes is on PATH, falling back to the
// presence of the profile's own config so an install without a PATH entry (a
// GUI launch, a service) is still recognised.
func hermesInstalled(home hermesHome) bool {
	if _, err := exec.LookPath("hermes"); err == nil {
		return true
	}
	_, err := os.Stat(filepath.Clean(home.ConfigPath))
	return err == nil
}

// truthyHermesFlag reads a flag that arrives as a JSON bool or a query string.
func truthyHermesFlag(v any) bool {
	switch value := v.(type) {
	case bool:
		return value
	case string:
		return value == "true" || value == "1"
	default:
		return false
	}
}

func derefOr(s *string, fallback string) string {
	if s == nil || *s == "" {
		return fallback
	}
	return *s
}
