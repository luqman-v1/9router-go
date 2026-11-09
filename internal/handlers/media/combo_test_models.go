package media

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"9router/proxy/internal/handlerutil"
)

type comboTestModelsRequest struct {
	Models []string `json:"models"`
	Kind   string   `json:"kind"`
}

type comboModelTestItem struct {
	Model     string `json:"model"`
	OK        bool   `json:"ok"`
	LatencyMs int64  `json:"latencyMs"`
	Error     string `json:"error,omitempty"`
	Status    int    `json:"status,omitempty"`
}

type comboTestSummary struct {
	Total int `json:"total"`
	Alive int `json:"alive"`
	Dead  int `json:"dead"`
}

type comboTestModelsResponse struct {
	ComboID string               `json:"comboId"`
	Summary comboTestSummary     `json:"summary"`
	Results []comboModelTestItem `json:"results"`
}

// HandleComboTestModels handles POST /api/combos/{id}/test-models.
// Probes models in a combo sequentially to avoid socket exhaustion.
// Supports both persisted combos and draft combos.
// Port of upstream open-sse / Next.js PR #5246.
func (h *MediaHandler) HandleComboTestModels(w http.ResponseWriter, r *http.Request) {
	comboID := chi.URLParam(r, "id")
	if comboID == "" {
		comboID = strings.TrimPrefix(r.URL.Path, "/api/combos/")
		comboID = strings.TrimSuffix(comboID, "/test-models")
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer func() { _ = r.Body.Close() }()

	var req comboTestModelsRequest
	if len(body) > 0 {
		_ = json.Unmarshal(body, &req)
	}

	kind := req.Kind
	if kind == "" {
		kind = "llm"
	}

	models := req.Models
	if len(models) == 0 && comboID != "" && comboID != "draft" {
		if h.Repo == nil {
			handlerutil.WriteJSONError(w, http.StatusNotFound, "Combo not found")
			return
		}
		combo, err := h.Repo.GetComboById(comboID)
		if err != nil || combo == nil {
			handlerutil.WriteJSONError(w, http.StatusNotFound, "Combo not found")
			return
		}
		if combo.Models != "" {
			_ = json.Unmarshal([]byte(combo.Models), &models)
		}
		if combo.Kind != nil && *combo.Kind != "" {
			kind = *combo.Kind
		}
	}

	if len(models) == 0 {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "No models in combo to test")
		return
	}

	results := make([]comboModelTestItem, 0, len(models))
	for _, m := range models {
		cleanModel := strings.TrimSpace(m)
		if cleanModel == "" {
			continue
		}
		probeRes := h.probeModel(r, cleanModel, kind)
		item := comboModelTestItem{
			Model:     cleanModel,
			OK:        probeRes.OK,
			LatencyMs: probeRes.LatencyMs,
			Error:     probeRes.Error,
			Status:    probeRes.Status,
		}
		results = append(results, item)
	}

	alive := 0
	for _, res := range results {
		if res.OK {
			alive++
		}
	}
	dead := len(results) - alive

	resp := comboTestModelsResponse{
		ComboID: comboID,
		Summary: comboTestSummary{
			Total: len(results),
			Alive: alive,
			Dead:  dead,
		},
		Results: results,
	}

	handlerutil.WriteJSON(w, http.StatusOK, resp)
}
