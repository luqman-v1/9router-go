package dashboard

import (
	"io"
	"math"
	"net/http"
	"strings"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/tokensaver"
	json "encoding/json/v2"
)

// TestRTKRequest represents the request body for POST /api/tokensaver/rtk/test.
type TestRTKRequest struct {
	Text   string                `json:"text"`
	Config *tokensaver.RTKConfig `json:"config,omitempty"`
}

// HandleTestRTK handles POST /api/tokensaver/rtk/test.
func (h *DashboardHandler) HandleTestRTK(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var req TestRTKRequest
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	cfg := tokensaver.DefaultRTKConfig()
	if req.Config != nil {
		cfg = *req.Config
	} else if h.Repo != nil {
		// Populate from stored settings if available
		if s, err := h.Repo.GetSettings(); err == nil && s != nil {
			cfg.Mode = s.RTKMode
			cfg.Intensity = s.RTKIntensity
			if s.RTKMaxLines > 0 {
				cfg.MaxLines = s.RTKMaxLines
			}
			if s.RTKMaxChars > 0 {
				cfg.MaxChars = s.RTKMaxChars
			}
			cfg.Deduplicate = s.RTKDeduplicate
			if s.RTKCategories != nil {
				cfg.Categories = s.RTKCategories
			}
			if s.RTKFilters != nil {
				cfg.Filters = s.RTKFilters
			}
			cfg.RawRetention = s.RTKRawRetention
		}
	}

	res := tokensaver.CompressTextDetailed(req.Text, cfg)
	handlerutil.WriteJSON(w, http.StatusOK, res)
}

// HandleGetRTKFilters handles GET /api/tokensaver/rtk/filters.
func (h *DashboardHandler) HandleGetRTKFilters(w http.ResponseWriter, r *http.Request) {
	cfg := tokensaver.DefaultRTKConfig()
	if h.TokenSaver != nil {
		cfg = h.TokenSaver.RTKConfig()
	} else if h.Repo != nil {
		if s, err := h.Repo.GetSettings(); err == nil && s != nil {
			if s.RTKCategories != nil {
				cfg.Categories = s.RTKCategories
			}
			if s.RTKFilters != nil {
				cfg.Filters = s.RTKFilters
			}
		}
	}

	filters := tokensaver.GetFilterCatalog(cfg)
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"filters": filters,
	})
}

// TestCavemanRequest represents the request body for POST /api/tokensaver/caveman/test.
type TestCavemanRequest struct {
	Text             string `json:"text"`
	Level            string `json:"level,omitempty"`
	Language         string `json:"language,omitempty"`
	Mode             string `json:"mode,omitempty"`
	PreserveKeywords string `json:"preserveKeywords,omitempty"`
}

// TestCavemanResponse represents the output metrics for POST /api/tokensaver/caveman/test.
type TestCavemanResponse struct {
	Text             string  `json:"text"`
	OriginalTokens   int     `json:"originalTokens"`
	CompressedTokens int     `json:"compressedTokens"`
	SavedPct         float64 `json:"savedPct"`
}

// HandleTestCaveman handles POST /api/tokensaver/caveman/test.
func (h *DashboardHandler) HandleTestCaveman(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var req TestCavemanRequest
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	level := req.Level
	if level == "" {
		level = "full"
	}
	language := req.Language
	if language == "" {
		language = "en"
	}

	// If text is provided, test input prompt compression
	if strings.TrimSpace(req.Text) != "" && req.Mode != "preview" {
		origTokens := (len(req.Text) + 3) / 4
		compressed := tokensaver.CompressInputPrompt(req.Text, req.PreserveKeywords)
		compTokens := (len(compressed) + 3) / 4
		saved := origTokens - compTokens
		if saved < 0 {
			saved = 0
		}
		var pct float64
		if origTokens > 0 && saved > 0 {
			pct = math.Round((float64(saved)/float64(origTokens)*100.0)*10) / 10
		}
		handlerutil.WriteJSON(w, http.StatusOK, TestCavemanResponse{
			Text:             compressed,
			OriginalTokens:   origTokens,
			CompressedTokens: compTokens,
			SavedPct:         pct,
		})
		return
	}

	// Otherwise, return Caveman system prompt preview
	prompt := tokensaver.GetCavemanPromptWithLang(level, language)
	tokens := (len(prompt) + 3) / 4
	handlerutil.WriteJSON(w, http.StatusOK, TestCavemanResponse{
		Text:             prompt,
		OriginalTokens:   0,
		CompressedTokens: tokens,
		SavedPct:         0,
	})
}
