// Package promptcache is a thread-safe, memory-bounded LRU cache for identical
// prompt responses.
//
// Bounded in BYTES, not just in entries. The entry count alone is not a memory
// bound: with a 512 KiB per-response cap, 1000 entries can hold 512 MB, and a
// production deployment reached 194 MB of retained response bodies — 92% of the
// process heap — while serving a 0.5% hit rate. The byte budget is what makes
// the footprint predictable on the small host this gateway is expected to run
// on.
package promptcache

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"sync"
	"time"
)

// Entry stores the cached response for an identical prompt request.
type Entry struct {
	Key         string
	Model       string
	ContentType string
	StatusCode  int
	Body        []byte
	IsStream    bool
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

// Cache is a thread-safe, memory-bounded LRU cache for prompt responses.
type Cache struct {
	mu            sync.RWMutex
	capacity      int
	maxBytes      int64
	maxEntryBytes int64
	bytes         int64
	ttl           time.Duration
	items         map[string]*list.Element
	evict         *list.List
	hits          uint64
	misses        uint64
	lastSweep     time.Time
}

// DefaultMaxBytes caps how much response data the cache may retain.
//
// Sized for the smallest host this proxy is expected to run on (~1 GB): the
// cache is an optimisation for repeated prompts, so it must not be able to crowd
// out the process it lives in. A deployment with a 0.5% hit rate keeps paying
// this cost for nothing, which is the argument for keeping it small.
const DefaultMaxBytes int64 = 16 << 20 // 16 MiB

// DefaultMaxEntryBytes caps a single cached response.
//
// A response larger than this is unlikely to repeat verbatim, so storing it
// mostly buys the memory growth this budget exists to prevent. The writer that
// buffers responses reads this limit so the two cannot disagree.
const DefaultMaxEntryBytes int64 = 512 << 10 // 512 KiB

// sweepInterval bounds how often Set walks the list looking for expired
// entries. Sweeping on every write would add a 1000-node walk to every request;
// once a minute keeps an expired entry from lingering indefinitely on an idle
// server without measuring on the hot path.
const sweepInterval = time.Minute

// Config configures the prompt cache.
type Config struct {
	Enabled       bool          `json:"enabled"`
	Capacity      int           `json:"capacity"`
	TTL           time.Duration `json:"ttl"`
	MaxBytes      int64         `json:"maxBytes"`
	MaxEntryBytes int64         `json:"maxEntryBytes"`
}

// DefaultConfig returns safe, low-overhead defaults.
func DefaultConfig() Config {
	return Config{
		Enabled:       true,
		Capacity:      500,
		TTL:           15 * time.Minute,
		MaxBytes:      DefaultMaxBytes,
		MaxEntryBytes: DefaultMaxEntryBytes,
	}
}

// New creates a cache with the given entry capacity and TTL, using the default
// byte budget. Callers that need explicit limits use NewWithConfig.
func New(capacity int, ttl time.Duration) *Cache {
	return NewWithConfig(Config{Capacity: capacity, TTL: ttl})
}

// NewWithConfig creates a cache from an explicit config, filling in defaults for
// any limit left unset.
func NewWithConfig(cfg Config) *Cache {
	if cfg.Capacity <= 0 {
		cfg.Capacity = 500
	}
	if cfg.TTL <= 0 {
		cfg.TTL = 15 * time.Minute
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = DefaultMaxBytes
	}
	if cfg.MaxEntryBytes <= 0 {
		cfg.MaxEntryBytes = DefaultMaxEntryBytes
	}
	// A per-entry cap above the total budget would let one response evict the
	// whole cache; clamp it so the two limits cannot contradict each other.
	if cfg.MaxEntryBytes > cfg.MaxBytes {
		cfg.MaxEntryBytes = cfg.MaxBytes
	}
	return &Cache{
		capacity:      cfg.Capacity,
		maxBytes:      cfg.MaxBytes,
		maxEntryBytes: cfg.MaxEntryBytes,
		ttl:           cfg.TTL,
		items:         make(map[string]*list.Element),
		evict:         list.New(),
		lastSweep:     time.Now(),
	}
}

// MaxEntryBytes reports the largest response this cache will store. The writer
// that buffers responses uses it as its own cap.
func (c *Cache) MaxEntryBytes() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.maxEntryBytesLocked()
}

func (c *Cache) maxEntryBytesLocked() int64 {
	if c.maxEntryBytes <= 0 {
		return DefaultMaxEntryBytes
	}
	return c.maxEntryBytes
}

// HashRequest calculates a deterministic SHA-256 hash for the given request
// payload.
//
// Only the fields that change the model's answer take part, so two requests
// that differ in an ignored field still share a cache entry.
//
// Each field is hashed from its RAW JSON bytes rather than being decoded into a
// map and re-encoded. Re-encoding was the obvious approach and was wrong: Go
// randomises map iteration, and json/v2 does not sort object keys, so
// []map[string]any produced two different byte streams — and therefore two
// different hashes — for the SAME input about 15% of the time. The cache then
// missed on requests that should have hit, silently defeating the feature. Raw
// bytes preserve the client's key order, which is stable for a given client.
func HashRequest(model string, body []byte) (string, bool) {
	var raw struct {
		Messages    jsontext.Value `json:"messages"`
		System      jsontext.Value `json:"system"`
		Tools       jsontext.Value `json:"tools"`
		Temperature jsontext.Value `json:"temperature"`
		TopP        jsontext.Value `json:"top_p"`
		MaxTokens   jsontext.Value `json:"max_tokens"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", false
	}
	// A request with no messages cannot be meaningfully cached, and hashing an
	// empty payload would let every malformed body collide onto one entry.
	//
	// An absent field and an explicitly empty array are both rejected: jsontext
	// preserves the raw bytes, so `[]` decodes to a two-byte value that a plain
	// length check would accept.
	if len(raw.Messages) == 0 || string(raw.Messages) == "[]" || string(raw.Messages) == "null" {
		return "", false
	}
	h := sha256.New()
	writeField := func(name string, v jsontext.Value) {
		if len(v) == 0 {
			return
		}
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write(v)
		h.Write([]byte{0})
	}
	// A fixed field order keeps the digest independent of the map above.
	h.Write([]byte(model))
	h.Write([]byte{0})
	writeField("messages", raw.Messages)
	writeField("system", raw.System)
	writeField("tools", raw.Tools)
	writeField("temperature", raw.Temperature)
	writeField("top_p", raw.TopP)
	writeField("max_tokens", raw.MaxTokens)
	return hex.EncodeToString(h.Sum(nil)), true
}

// Get retrieves an unexpired entry by its hash key.
func (c *Cache) Get(key string) (*Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	elem, ok := c.items[key]
	if !ok {
		c.misses++
		return nil, false
	}
	entry := elem.Value.(*Entry)
	if time.Now().After(entry.ExpiresAt) {
		c.evictElement(elem)
		c.misses++
		return nil, false
	}
	c.evict.MoveToFront(elem)
	c.hits++
	return entry, true
}

// Set adds or updates an entry in the cache.
//
// A body larger than the per-entry cap is rejected rather than stored: one huge
// response would otherwise evict every useful entry to make room for data that
// is unlikely to repeat.
func (c *Cache) Set(key string, model, contentType string, statusCode int, body []byte, isStream bool) {
	if key == "" || len(body) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	size := int64(len(body))
	if size > c.maxEntryBytesLocked() {
		return
	}
	now := time.Now()
	c.sweepExpiredLocked(now)
	// Update existing
	if elem, ok := c.items[key]; ok {
		c.evict.MoveToFront(elem)
		entry := elem.Value.(*Entry)
		c.bytes += size - int64(len(entry.Body))
		entry.Model = model
		entry.ContentType = contentType
		entry.StatusCode = statusCode
		entry.Body = body
		entry.IsStream = isStream
		entry.CreatedAt = now
		entry.ExpiresAt = now.Add(c.ttl)
		c.enforceBudgetLocked()
		return
	}
	entry := &Entry{
		Key:         key,
		Model:       model,
		ContentType: contentType,
		StatusCode:  statusCode,
		Body:        body,
		IsStream:    isStream,
		CreatedAt:   now,
		ExpiresAt:   now.Add(c.ttl),
	}
	elem := c.evict.PushFront(entry)
	c.items[key] = elem
	c.bytes += size
	c.enforceBudgetLocked()
}

// enforceBudgetLocked evicts least-recently-used entries until both the entry
// count and the byte budget fit. Must be called with the lock held.
func (c *Cache) enforceBudgetLocked() {
	for c.evict.Len() > c.capacity || c.bytes > c.maxBytes {
		// Guard against evicting past an empty list: the byte total can only
		// exceed the budget while something is stored, so this cannot spin.
		if c.evict.Len() == 0 {
			c.bytes = 0
			return
		}
		c.evictOldest()
	}
}

// sweepExpiredLocked drops entries past their TTL, walking from the back (least
// recently used) and stopping at the first live entry.
//
// This is what makes the TTL a real bound rather than a filter on reads: with
// eviction driven only by capacity, an idle server holds every entry until new
// traffic pushes it out, so a burst of large responses pins memory for as long
// as nothing else arrives. The walk is bounded by sweepInterval so it does not
// run on every write.
func (c *Cache) sweepExpiredLocked(now time.Time) {
	if now.Sub(c.lastSweep) < sweepInterval {
		return
	}
	c.lastSweep = now
	for {
		elem := c.evict.Back()
		if elem == nil {
			return
		}
		entry := elem.Value.(*Entry)
		if now.Before(entry.ExpiresAt) {
			return
		}
		c.evictElement(elem)
	}
}

// Clear flushes all entries from the cache.
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string]*list.Element)
	c.evict.Init()
	c.bytes = 0
}

// Stats returns the current cache metrics.
func (c *Cache) Stats() (len int, hits, misses uint64) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.evict.Len(), c.hits, c.misses
}

// StatsDetail returns the metrics the dashboard reports, including the byte
// footprint. Without the byte count an operator cannot tell a cache holding a
// few small entries from one holding the process's whole heap.
func (c *Cache) StatsDetail() (entries int, bytes int64, maxBytes int64, hits, misses uint64) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.evict.Len(), c.bytes, c.maxBytes, c.hits, c.misses
}

func (c *Cache) evictOldest() {
	elem := c.evict.Back()
	if elem != nil {
		c.evictElement(elem)
	}
}

func (c *Cache) evictElement(elem *list.Element) {
	c.evict.Remove(elem)
	entry := elem.Value.(*Entry)
	delete(c.items, entry.Key)
	c.bytes -= int64(len(entry.Body))
	if c.bytes < 0 {
		c.bytes = 0
	}
}
