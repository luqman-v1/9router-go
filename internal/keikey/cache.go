package keikey

import (
	"sync"
	"time"

	"9router/proxy/internal/models"
)

// Auth cache tuning (F-6). Argon2id at 64 MiB is far too expensive to run on
// every single request, so verified keys are cached for a short window.
const (
	// CacheCapacity bounds the number of cached verifications.
	CacheCapacity = 256
	// CacheTTL is how long a verification stays valid.
	CacheTTL = 5 * time.Second
)

// entry is one cached verification result.
type entry struct {
	key     any
	expires time.Time
}

// Cache is a bounded, TTL'd verification cache keyed by LookupHash.
// It is safe for concurrent use.
type Cache struct {
	mu      sync.Mutex
	entries map[string]entry
	ttl     time.Duration
	max     int
	// order preserves insertion order so eviction drops the oldest entry.
	order []string
}

// NewCache returns a cache with the given TTL and capacity. Non-positive
// arguments fall back to the package defaults.
func NewCache(ttl time.Duration, capacity int) *Cache {
	if ttl <= 0 {
		ttl = CacheTTL
	}
	if capacity <= 0 {
		capacity = CacheCapacity
	}
	return &Cache{
		entries: make(map[string]entry, capacity),
		ttl:     ttl,
		max:     capacity,
		order:   make([]string, 0, capacity),
	}
}

// Get returns the cached value for lookupHash when present and unexpired.
func (c *Cache) Get(lookupHash string) (any, bool) {
	if c == nil || lookupHash == "" {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[lookupHash]
	if !ok {
		return nil, false
	}
	if time.Now().After(e.expires) {
		c.removeLocked(lookupHash)
		return nil, false
	}
	return e.key, true
}

// Set stores key under lookupHash, dropping expired entries and, when still
// over capacity, the oldest entries.
func (c *Cache) Set(lookupHash string, key any) {
	if c == nil || lookupHash == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[lookupHash]; !exists {
		c.order = append(c.order, lookupHash)
	}
	c.entries[lookupHash] = entry{key: key, expires: time.Now().Add(c.ttl)}
	c.evictLocked()
}

// Len returns the number of entries currently held, expired ones included.
func (c *Cache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// evictLocked drops expired entries first, then the oldest ones until the
// cache is back within capacity.
func (c *Cache) evictLocked() {
	now := time.Now()
	live := c.order[:0]
	for _, k := range c.order {
		e, ok := c.entries[k]
		if !ok {
			continue
		}
		if now.After(e.expires) {
			delete(c.entries, k)
			continue
		}
		live = append(live, k)
	}
	c.order = live
	for len(c.entries) > c.max && len(c.order) > 0 {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
}

// removeLocked deletes a single entry.
func (c *Cache) removeLocked(lookupHash string) {
	delete(c.entries, lookupHash)
	for i, k := range c.order {
		if k == lookupHash {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
}

// defaultAuthCache backs RequireApiKey. A package-level cache avoids
// threading another constructor parameter through every middleware caller;
// its TTL bounds how long a revoked key can still authenticate.
var defaultAuthCache = NewCache(CacheTTL, CacheCapacity)

// DefaultAuthCache returns the process-wide verification cache.
func DefaultAuthCache() *Cache { return defaultAuthCache }

// InvalidateByID drops every cached row belonging to one API key id.
//
// The cache is keyed by the secret's lookup hash, which the dashboard cannot
// compute, so invalidation matches on the row id instead. A policy write calls
// this because a stale cached row would keep an expired or newly limited key
// authenticating for the rest of its TTL — a governance change has to take
// effect now, not in a few seconds.
func (c *Cache) InvalidateByID(apiKeyID string) {
	if c == nil || apiKeyID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for lookup, e := range c.entries {
		row, ok := e.key.(*models.APIKey)
		if ok && row.ID == apiKeyID {
			c.removeLocked(lookup)
		}
	}
}
