package dashboard

import (
	"math"
	"net/http"
	"strconv"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/semanticcache"
)

type CacheConfigResponse struct {
	SemanticCacheEnabled bool `json:"semanticCacheEnabled"`
}

type IdempotencyStatsResponse struct {
	ActiveKeys int `json:"activeKeys"`
	WindowMs   int `json:"windowMs"`
}

type CacheStatsResponse struct {
	SemanticCache semanticcache.Stats      `json:"semanticCache"`
	PromptCache   *db.PromptCacheMetrics   `json:"promptCache"`
	Trend         []db.CacheTrendPoint     `json:"trend"`
	Idempotency   IdempotencyStatsResponse `json:"idempotency"`
	Config        CacheConfigResponse      `json:"config"`
}

type CachePaginationMeta struct {
	Page       int `json:"page"`
	Limit      int `json:"limit"`
	Total      int `json:"total"`
	TotalPages int `json:"totalPages"`
}

type CacheEntriesResponse struct {
	Entries    []semanticcache.EntryMeta `json:"entries"`
	Pagination CachePaginationMeta       `json:"pagination"`
}

func (h *DashboardHandler) getSemanticCache() *semanticcache.Cache {
	if h.SemanticCache != nil {
		return h.SemanticCache
	}
	return semanticcache.New(semanticcache.Config{Enabled: false}, nil)
}

// HandleGetCache returns aggregated prompt cache metrics, hourly trend, and semantic cache stats.
func (h *DashboardHandler) HandleGetCache(w http.ResponseWriter, r *http.Request) {
	trendHours := 24
	if rawHours := r.URL.Query().Get("trendHours"); rawHours != "" {
		if val, err := strconv.Atoi(rawHours); err == nil && val > 0 {
			trendHours = min(720, max(1, val))
		}
	}

	sc := h.getSemanticCache()
	semanticStats := sc.Stats()

	promptMetrics, err := h.Repo.GetPromptCacheMetrics(r.Context())
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	trend, err := h.Repo.GetPromptCacheTrend(r.Context(), trendHours)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp := CacheStatsResponse{
		SemanticCache: semanticStats,
		PromptCache:   promptMetrics,
		Trend:         trend,
		Idempotency: IdempotencyStatsResponse{
			ActiveKeys: 0,
			WindowMs:   5000,
		},
		Config: CacheConfigResponse{
			SemanticCacheEnabled: sc.Enabled(),
		},
	}

	handlerutil.WriteJSON(w, http.StatusOK, resp)
}

// HandleDeleteCache clears or invalidates semantic cache entries.
func (h *DashboardHandler) HandleDeleteCache(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	model := q.Get("model")
	signature := q.Get("signature")
	staleMsStr := q.Get("staleMs")

	sc := h.getSemanticCache()
	ctx := r.Context()

	// Invalidate by model
	if model != "" {
		count := sc.InvalidateByModel(ctx, model)
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true, "count": count})
		return
	}

	// Invalidate by signature
	if signature != "" {
		ok := sc.DeleteEntry(ctx, signature)
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": ok})
		return
	}

	// Invalidate by staleMs
	if staleMsStr != "" {
		ms, err := strconv.ParseInt(staleMsStr, 10, 64)
		if err != nil || ms <= 0 {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "staleMs must be a positive integer (milliseconds)")
			return
		}
		count := sc.InvalidateOlderThan(ctx, time.Duration(ms)*time.Millisecond)
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true, "count": count})
		return
	}

	// Clear all
	sc.Clear()
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// HandleGetCacheEntries returns paginated, filtered, and sorted semantic cache entries.
func (h *DashboardHandler) HandleGetCacheEntries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit < 1 {
		limit = 20
	}
	search := q.Get("search")
	model := q.Get("model")
	sortBy := q.Get("sortBy")
	if sortBy == "" {
		sortBy = "created_at"
	}
	sortOrder := q.Get("sortOrder")
	if sortOrder == "" {
		sortOrder = "desc"
	}

	sc := h.getSemanticCache()
	entries, total := sc.ListEntries(page, limit, search, model, sortBy, sortOrder)

	totalPages := int(math.Ceil(float64(total) / float64(limit)))
	if totalPages < 1 {
		totalPages = 1
	}

	resp := CacheEntriesResponse{
		Entries: entries,
		Pagination: CachePaginationMeta{
			Page:       page,
			Limit:      limit,
			Total:      total,
			TotalPages: totalPages,
		},
	}

	handlerutil.WriteJSON(w, http.StatusOK, resp)
}

// HandleDeleteCacheEntry deletes a specific cache entry by id or signature.
func (h *DashboardHandler) HandleDeleteCacheEntry(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		id = r.URL.Query().Get("signature")
	}
	if id == "" {
		id = getURLParam(r, "id")
	}
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing entry id")
		return
	}

	sc := h.getSemanticCache()
	ok := sc.DeleteEntry(r.Context(), id)
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": ok})
}
