package semanticcache

import (
	"bytes"
	"container/list"
	"context"
	"sync"
	"time"
)

// Store is the storage interface for cache entries.
type Store interface {
	Get(ctx context.Context, key string) (Entry, bool)
	Put(ctx context.Context, key string, e Entry) error
	Delete(ctx context.Context, key string) bool
	Len() int
	Clear()
	Entries() []Entry
	InvalidateByModel(ctx context.Context, model string) int
	InvalidateOlderThan(ctx context.Context, d time.Duration) int
	RecordHit(ctx context.Context, key string, tokensSaved int64)
}

type lruItem struct {
	key   string
	entry Entry
}

// LRUStore is a thread-safe, bounded in-memory LRU cache store.
type LRUStore struct {
	mu         sync.RWMutex
	items      map[string]*list.Element
	evictList  *list.List
	maxEntries int
	ttl        time.Duration
}

// NewLRUStore creates a thread-safe LRUStore bounded by maxEntries and TTL.
func NewLRUStore(maxEntries int, ttl time.Duration) *LRUStore {
	if maxEntries <= 0 {
		maxEntries = 1000
	}
	return &LRUStore{
		items:      make(map[string]*list.Element, min(maxEntries, 128)),
		evictList:  list.New(),
		maxEntries: maxEntries,
		ttl:        ttl,
	}
}

// Get returns the cached entry for key, or false if missing or expired.
// Clones ResponseBody to prevent data races across goroutines.
func (s *LRUStore) Get(_ context.Context, key string) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	elem, ok := s.items[key]
	if !ok {
		return Entry{}, false
	}

	item := elem.Value.(*lruItem)
	if s.ttl > 0 && time.Since(item.entry.StoredAt) > s.ttl {
		s.removeElement(elem)
		return Entry{}, false
	}

	s.evictList.MoveToFront(elem)
	clone := item.entry
	clone.ResponseBody = bytes.Clone(item.entry.ResponseBody)
	return clone, true
}

// Put adds or updates an entry for key, evicting the least recently used if full.
func (s *LRUStore) Put(_ context.Context, key string, e Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	e.ResponseBody = bytes.Clone(e.ResponseBody)
	if e.StoredAt.IsZero() {
		e.StoredAt = time.Now()
	}

	// Update existing entry
	if elem, ok := s.items[key]; ok {
		s.evictList.MoveToFront(elem)
		elem.Value.(*lruItem).entry = e
		return nil
	}

	// Evict oldest if capacity reached
	for s.maxEntries > 0 && s.evictList.Len() >= s.maxEntries {
		s.removeOldest()
	}

	item := &lruItem{key: key, entry: e}
	elem := s.evictList.PushFront(item)
	s.items[key] = elem
	return nil
}

// Delete removes an entry by key.
func (s *LRUStore) Delete(_ context.Context, key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	elem, ok := s.items[key]
	if !ok {
		return false
	}
	s.removeElement(elem)
	return true
}

// RecordHit increments hit counter and tokens saved for a key.
func (s *LRUStore) RecordHit(_ context.Context, key string, tokensSaved int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if elem, ok := s.items[key]; ok {
		item := elem.Value.(*lruItem)
		item.entry.HitCount++
		item.entry.TokensSaved += tokensSaved
	}
}

// Entries returns active (non-expired) entries in the store.
func (s *LRUStore) Entries() []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := time.Now()
	res := make([]Entry, 0, len(s.items))
	for _, elem := range s.items {
		item := elem.Value.(*lruItem)
		if s.ttl > 0 && now.Sub(item.entry.StoredAt) > s.ttl {
			continue
		}
		clone := item.entry
		clone.ResponseBody = bytes.Clone(item.entry.ResponseBody)
		res = append(res, clone)
	}
	return res
}

// InvalidateByModel removes all entries matching the given model name.
func (s *LRUStore) InvalidateByModel(_ context.Context, model string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	count := 0
	var toRemove []*list.Element
	for _, elem := range s.items {
		item := elem.Value.(*lruItem)
		if item.entry.Model == model {
			toRemove = append(toRemove, elem)
		}
	}
	for _, elem := range toRemove {
		s.removeElement(elem)
		count++
	}
	return count
}

// InvalidateOlderThan removes entries stored longer than duration d ago.
func (s *LRUStore) InvalidateOlderThan(_ context.Context, d time.Duration) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	count := 0
	now := time.Now()
	var toRemove []*list.Element
	for _, elem := range s.items {
		item := elem.Value.(*lruItem)
		if now.Sub(item.entry.StoredAt) > d {
			toRemove = append(toRemove, elem)
		}
	}
	for _, elem := range toRemove {
		s.removeElement(elem)
		count++
	}
	return count
}

func (s *LRUStore) removeElement(elem *list.Element) {
	s.evictList.Remove(elem)
	item := elem.Value.(*lruItem)
	delete(s.items, item.key)
}

func (s *LRUStore) removeOldest() {
	elem := s.evictList.Back()
	if elem != nil {
		s.removeElement(elem)
	}
}

// Len returns the current number of cached entries.
func (s *LRUStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.items)
}

// Clear flushes all cached entries.
func (s *LRUStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = make(map[string]*list.Element)
	s.evictList.Init()
}
