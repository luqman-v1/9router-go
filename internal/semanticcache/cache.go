package semanticcache

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/translator"
)

// Cache coordinates prompt/response caching with thread-safety and TTL support.
type Cache struct {
	cfg         Config
	store       Store
	enabledFn   func() bool
	hits        atomic.Int64
	misses      atomic.Int64
	tokensSaved atomic.Int64
}

// New creates a Cache instance with default config and optional enabled predicate.
func New(cfg Config, store Store, enabledFn ...func() bool) *Cache {
	if cfg.MaxEntries <= 0 {
		cfg.MaxEntries = 1000
	}
	if cfg.TTL <= 0 {
		cfg.TTL = 24 * time.Hour
	}
	if cfg.SimilarityThreshold <= 0 {
		cfg.SimilarityThreshold = 0.95
	}
	if store == nil {
		store = NewLRUStore(cfg.MaxEntries, cfg.TTL)
	}

	var fn func() bool
	if len(enabledFn) > 0 {
		fn = enabledFn[0]
	}

	return &Cache{
		cfg:       cfg,
		store:     store,
		enabledFn: fn,
	}
}

// Enabled reports whether caching is active.
func (c *Cache) Enabled() bool {
	if c == nil {
		return false
	}
	if c.enabledFn != nil {
		return c.enabledFn()
	}
	return c.cfg.Enabled
}

// Lookup queries the cache for a stored response.
func (c *Cache) Lookup(ctx context.Context, req *translator.OpenAIRequest) (*Entry, float64, bool) {
	if !c.Enabled() || req == nil || req.Stream {
		return nil, 0, false
	}

	sessionID := handlerutil.GetSessionID(ctx)
	key := BuildCacheKey(sessionID, req)
	if key == "" {
		c.misses.Add(1)
		return nil, 0, false
	}

	entry, ok := c.store.Get(ctx, key)
	if !ok {
		c.misses.Add(1)
		return nil, 0, false
	}

	// Verify model matches
	if entry.Model != "" && req.Model != "" && entry.Model != req.Model {
		c.misses.Add(1)
		return nil, 0, false
	}

	c.hits.Add(1)
	savedTokens := estimateTokens(entry.ResponseBody)
	c.tokensSaved.Add(savedTokens)
	c.store.RecordHit(ctx, key, savedTokens)

	return &entry, 1.0, true
}

// Store saves a response body into the cache.
func (c *Cache) Store(ctx context.Context, req *translator.OpenAIRequest, responseBody []byte, contentType string) error {
	if !c.Enabled() || req == nil || req.Stream || len(responseBody) == 0 {
		return nil
	}

	sessionID := handlerutil.GetSessionID(ctx)
	key := BuildCacheKey(sessionID, req)
	if key == "" {
		return nil
	}

	if contentType == "" {
		contentType = "application/json"
	}

	entry := Entry{
		Key:          key,
		Model:        req.Model,
		ResponseBody: responseBody,
		ContentType:  contentType,
		StoredAt:     time.Now(),
	}

	return c.store.Put(ctx, key, entry)
}

// Stats returns overall semantic cache metrics.
func (c *Cache) Stats() Stats {
	if c == nil {
		return Stats{HitRate: "0.0"}
	}
	hits := c.hits.Load()
	misses := c.misses.Load()
	total := hits + misses
	hitRate := "0.0"
	if total > 0 {
		hitRate = fmt.Sprintf("%.1f", float64(hits)/float64(total)*100.0)
	}

	memEntries := 0
	if c.store != nil {
		memEntries = c.store.Len()
	}

	dbEntries := 0
	if ps, ok := c.store.(interface{ DBLen() int }); ok {
		dbEntries = ps.DBLen()
	}

	return Stats{
		MemoryEntries: memEntries,
		DBEntries:     dbEntries,
		Hits:          hits,
		Misses:        misses,
		HitRate:       hitRate,
		TokensSaved:   c.tokensSaved.Load(),
	}
}

// ListEntries returns a paginated, filtered, and sorted list of entry metadata.
func (c *Cache) ListEntries(page, limit int, search, model, sortBy, sortOrder string) ([]EntryMeta, int) {
	if c == nil || c.store == nil {
		return nil, 0
	}
	entries := c.store.Entries()
	filtered := make([]EntryMeta, 0, len(entries))
	searchLower := strings.ToLower(search)
	modelLower := strings.ToLower(model)

	for _, e := range entries {
		if modelLower != "" && strings.ToLower(e.Model) != modelLower {
			continue
		}
		if searchLower != "" && !strings.Contains(strings.ToLower(e.Key), searchLower) {
			continue
		}

		expiresAt := ""
		if c.cfg.TTL > 0 {
			expiresAt = e.StoredAt.Add(c.cfg.TTL).UTC().Format(time.RFC3339)
		}

		filtered = append(filtered, EntryMeta{
			ID:          e.Key,
			Signature:   e.Key,
			Model:       e.Model,
			HitCount:    e.HitCount,
			TokensSaved: e.TokensSaved,
			CreatedAt:   e.StoredAt.UTC().Format(time.RFC3339),
			ExpiresAt:   expiresAt,
		})
	}

	sort.Slice(filtered, func(i, j int) bool {
		switch sortBy {
		case "hits", "hit_count":
			if sortOrder == "asc" {
				return filtered[i].HitCount < filtered[j].HitCount
			}
			return filtered[i].HitCount > filtered[j].HitCount
		case "tokens_saved":
			if sortOrder == "asc" {
				return filtered[i].TokensSaved < filtered[j].TokensSaved
			}
			return filtered[i].TokensSaved > filtered[j].TokensSaved
		case "model":
			if sortOrder == "asc" {
				return filtered[i].Model < filtered[j].Model
			}
			return filtered[i].Model > filtered[j].Model
		default: // created_at
			if sortOrder == "asc" {
				return filtered[i].CreatedAt < filtered[j].CreatedAt
			}
			return filtered[i].CreatedAt > filtered[j].CreatedAt
		}
	})

	total := len(filtered)
	if limit <= 0 {
		limit = 20
	}
	if page <= 0 {
		page = 1
	}
	start := (page - 1) * limit
	if start >= total {
		return []EntryMeta{}, total
	}
	end := start + limit
	if end > total {
		end = total
	}

	return filtered[start:end], total
}

// DeleteEntry deletes an entry by key or signature.
func (c *Cache) DeleteEntry(ctx context.Context, key string) bool {
	if c == nil || c.store == nil {
		return false
	}
	return c.store.Delete(ctx, key)
}

// InvalidateByModel deletes all entries matching the given model.
func (c *Cache) InvalidateByModel(ctx context.Context, model string) int {
	if c == nil || c.store == nil {
		return 0
	}
	return c.store.InvalidateByModel(ctx, model)
}

// InvalidateOlderThan deletes all entries older than the given duration.
func (c *Cache) InvalidateOlderThan(ctx context.Context, d time.Duration) int {
	if c == nil || c.store == nil {
		return 0
	}
	return c.store.InvalidateOlderThan(ctx, d)
}

// Clear flushes all cached entries.
func (c *Cache) Clear() {
	if c != nil && c.store != nil {
		c.store.Clear()
	}
}

// Len returns the number of active cached entries.
func (c *Cache) Len() int {
	if c == nil || c.store == nil {
		return 0
	}
	return c.store.Len()
}

// Close releases background resources such as the persistence janitor.
func (c *Cache) Close() {
	if c == nil || c.store == nil {
		return
	}
	if closer, ok := c.store.(interface{ Close() }); ok {
		closer.Close()
	}
}

func estimateTokens(body []byte) int64 {
	if len(body) == 0 {
		return 0
	}
	var parsed struct {
		Usage struct {
			CompletionTokens int64 `json:"completion_tokens"`
			TotalTokens      int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil && parsed.Usage.TotalTokens > 0 {
		return parsed.Usage.TotalTokens
	}
	tokens := int64(len(body) / 4)
	if tokens < 1 {
		tokens = 1
	}
	return tokens
}
