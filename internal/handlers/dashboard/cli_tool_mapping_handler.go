package dashboard

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
)

// KnownCliTool describes a CLI tool that supports slot mapping.
type KnownCliTool struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	UserAgent   string `json:"userAgent"`
	DefaultSlot string `json:"defaultSlot"`
}

// HandleGetCliTools returns the registry of CLI tools that support slot mapping.
func (h *DashboardHandler) HandleGetCliTools(w http.ResponseWriter, r *http.Request) {
	tools := []KnownCliTool{
		{ID: "claude", Name: "Claude Code", UserAgent: "claude-cli/|claude-code/", DefaultSlot: "sonnet"},
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"tools": tools,
	})
}

// CliToolMappingInput is the request body for upserting a CLI tool mapping.
type CliToolMappingInput struct {
	ID          string `json:"id"`
	ToolID      string `json:"toolId"`
	APIKeyID    string `json:"apiKeyId"`
	SlotKey     string `json:"slotKey"`
	TargetModel string `json:"targetModel"`
	SourceModel string `json:"sourceModel"`
	Enabled     *int   `json:"enabled,omitempty"`
	CreatedAt   string `json:"createdAt,omitempty"`
}

// HandleGetCliToolMappings returns enabled mappings for a key+tool pair.
// GET /api/cli-tools/mappings?keyId=&toolId=
func (h *DashboardHandler) HandleGetCliToolMappings(w http.ResponseWriter, r *http.Request) {
	keyID := r.URL.Query().Get("keyId")
	toolID := r.URL.Query().Get("toolId")
	if keyID == "" || toolID == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "keyId and toolId query parameters are required")
		return
	}
	mappings, err := h.Repo.GetCliToolMappings(keyID, toolID)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"mappings": mappings,
	})
}

// HandleGetCliToolMappingsAll returns all mappings for a key (all tools).
// GET /api/cli-tools/mappings-all?keyId=
func (h *DashboardHandler) HandleGetCliToolMappingsAll(w http.ResponseWriter, r *http.Request) {
	keyID := r.URL.Query().Get("keyId")
	if keyID == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "keyId query parameter is required")
		return
	}
	tools := db.KnownCliTools()
	var allMappings []map[string]any
	for _, tool := range tools {
		mappings, err := h.Repo.GetCliToolMappings(keyID, tool.ID)
		if err != nil {
			continue
		}
		for _, m := range mappings {
			allMappings = append(allMappings, map[string]any{
				"id": m.ID, "toolId": m.ToolID, "apiKeyId": m.APIKeyID,
				"slotKey": m.SlotKey, "targetModel": m.TargetModel,
				"sourceModel": m.SourceModel, "enabled": m.Enabled,
				"createdAt": m.CreatedAt, "updatedAt": m.UpdatedAt,
			})
		}
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"mappings": allMappings,
	})
}

// HandleSaveCliToolMapping creates or updates a CLI tool mapping.
func (h *DashboardHandler) HandleSaveCliToolMapping(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var input CliToolMappingInput
	if err := json.Unmarshal(body, &input); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if input.ToolID == "" || input.SlotKey == "" || input.TargetModel == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "toolId, slotKey, and targetModel are required")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	enabled := 1
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	mapping := db.CliToolMapping{
		ID:          input.ID,
		ToolID:      input.ToolID,
		APIKeyID:    input.APIKeyID,
		SlotKey:     input.SlotKey,
		TargetModel: input.TargetModel,
		SourceModel: input.SourceModel,
		Enabled:     enabled,
		CreatedAt:   input.CreatedAt,
		UpdatedAt:   now,
	}
	if mapping.APIKeyID == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "apiKeyId is required")
		return
	}
	if err := h.Repo.UpsertCliToolMapping(mapping); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// HandleDeleteCliToolMapping removes a CLI tool mapping.
func (h *DashboardHandler) HandleDeleteCliToolMapping(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "id query parameter is required")
		return
	}
	if err := h.Repo.DeleteCliToolMapping(id); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// CliToolSettingsInput is the request body for settings.
type CliToolSettingsInput struct {
	APIKeyID        string `json:"apiKeyId"`
	ToolID          string `json:"toolId"`
	MappingsEnabled *int   `json:"mappingsEnabled,omitempty"`
	Mode            string `json:"mode,omitempty"`
}

// HandleGetCliToolSettings returns settings for a key+tool pair.
func (h *DashboardHandler) HandleGetCliToolSettings(w http.ResponseWriter, r *http.Request) {
	keyID := r.URL.Query().Get("keyId")
	toolID := r.URL.Query().Get("toolId")
	if keyID == "" || toolID == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "keyId and toolId query parameters are required")
		return
	}
	settings, err := h.Repo.GetCliToolSettings(keyID, toolID)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"settings": settings,
	})
}

// HandleSetCliToolSettings upserts settings for a key+tool pair.
func (h *DashboardHandler) HandleSetCliToolSettings(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var input CliToolSettingsInput
	if err := json.Unmarshal(body, &input); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if input.APIKeyID == "" || input.ToolID == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "apiKeyId and toolId are required")
		return
	}
	mappingsEnabled := 1
	if input.MappingsEnabled != nil {
		mappingsEnabled = *input.MappingsEnabled
	}
	mode := "remote"
	if input.Mode == "local" {
		mode = "local"
	}
	settings := db.CliToolSettings{
		APIKeyID:        input.APIKeyID,
		ToolID:          input.ToolID,
		MappingsEnabled: mappingsEnabled,
		Mode:            mode,
	}
	if err := h.Repo.SetCliToolSettings(settings); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// CliToolKeyPatch is the request body for updating key scopes.
type CliToolKeyPatch struct {
	GrantCliMapping *bool `json:"grantCliMapping,omitempty"`
}

// HandleGetCliToolApiKey returns an API key with its scopes.
func (h *DashboardHandler) HandleGetCliToolApiKey(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing key id")
		return
	}
	key, err := h.Repo.GetApiKeyByID(id)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if key == nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "key not found")
		return
	}
	hasScope := db.HasCliMappingScope(key)
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"key":            key,
		"hasCliMapping": hasScope,
	})
}

// HandleUpdateCliToolApiKey grants or revokes routing:cli_mapping scope.
func (h *DashboardHandler) HandleUpdateCliToolApiKey(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing key id")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var patch CliToolKeyPatch
	if err := json.Unmarshal(body, &patch); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	key, err := h.Repo.GetApiKeyByID(id)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if key == nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "key not found")
		return
	}

	scopes := append([]string(nil), key.Scopes...)
	hasNow := false
	for _, s := range scopes {
		if s == "routing:cli_mapping" {
			hasNow = true
			break
		}
	}

	wantScope := hasNow
	if patch.GrantCliMapping != nil {
		wantScope = *patch.GrantCliMapping
	}

	if wantScope && !hasNow {
		scopes = append(scopes, "routing:cli_mapping")
	} else if !wantScope && hasNow {
		var filtered []string
		for _, s := range scopes {
			if s != "routing:cli_mapping" {
				filtered = append(filtered, s)
			}
		}
		scopes = filtered
	}

	if err := h.Repo.SetApiKeyScopes(id, scopes); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, fmt.Errorf("set scopes: %w", err).Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}
