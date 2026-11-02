package promptcache

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHashRequestDeterministic(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	first, ok := HashRequest("m", body)
	if !ok {
		t.Fatal("HashRequest() = false, want ok")
	}
	if first == "" {
		t.Fatal("HashRequest() returned an empty key")
	}
	// The same input must hash identically every time. This is the property
	// that breaks if the payload is decoded into a map and re-encoded: Go
	// randomises map iteration, so the same request produced two different
	// digests about 15% of the time and the cache silently never hit.
	for range 50 {
		again, ok := HashRequest("m", body)
		if !ok || again != first {
			t.Fatalf("HashRequest() is not deterministic: %q vs %q", again, first)
		}
	}
}

func TestHashRequestIgnoresIrrelevantFields(t *testing.T) {
	a := []byte(`{"messages":[{"role":"user","content":"hi"}],"stream":true,"user":"x"}`)
	b := []byte(`{"messages":[{"role":"user","content":"hi"}],"stream":false,"user":"y"}`)
	ka, _ := HashRequest("m", a)
	kb, _ := HashRequest("m", b)
	if ka != kb {
		t.Error("requests differing only in ignored fields produced different keys")
	}
}

func TestHashRequestDistinguishesRelevantFields(t *testing.T) {
	base := `{"messages":[{"role":"user","content":"hi"}]}`
	variants := map[string]string{
		"temperature": `{"messages":[{"role":"user","content":"hi"}],"temperature":0.5}`,
		"top_p":       `{"messages":[{"role":"user","content":"hi"}],"top_p":0.9}`,
		"max_tokens":  `{"messages":[{"role":"user","content":"hi"}],"max_tokens":100}`,
		"system":      `{"system":"be terse","messages":[{"role":"user","content":"hi"}]}`,
		"tools":       `{"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function"}]}`,
	}
	baseKey, _ := HashRequest("m", []byte(base))
	for name, body := range variants {
		key, ok := HashRequest("m", []byte(body))
		if !ok {
			t.Errorf("%s variant was not hashable", name)
			continue
		}
		if key == baseKey {
			t.Errorf("%s does not affect the key", name)
		}
	}
	// The model name is part of the key: the same prompt on two models is not
	// the same answer.
	otherModel, _ := HashRequest("other", []byte(base))
	if otherModel == baseKey {
		t.Error("the model name does not affect the key")
	}
}

func TestHashRequestRejectsUncacheable(t *testing.T) {
	cases := map[string][]byte{
		"invalid json":   []byte(`{not json`),
		"no messages":    []byte(`{"model":"m"}`),
		"empty messages": []byte(`{"messages":[]}`),
		"empty body":     nil,
	}
	for name, body := range cases {
		if _, ok := HashRequest("m", body); ok {
			t.Errorf("HashRequest(%s) = true, want false", name)
		}
	}
}

func TestGetSetRoundTrip(t *testing.T) {
	c := New(10, time.Minute)
	key, ok := HashRequest("m", []byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	if !ok {
		t.Fatal("HashRequest failed")
	}
	c.Set(key, "m", "application/json", 200, []byte("response"), false)

	entry, hit := c.Get(key)
	if !hit {
		t.Fatal("Get() missed a stored entry")
	}
	if string(entry.Body) != "response" {
		t.Errorf("Body = %q, want response", entry.Body)
	}
	if entry.StatusCode != 200 {
		t.Errorf("StatusCode = %d, want 200", entry.StatusCode)
	}
	if entry.Model != "m" {
		t.Errorf("Model = %q, want m", entry.Model)
	}
	if _, hit := c.Get("missing"); hit {
		t.Error("Get() hit on an absent key")
	}
}

func TestSetRejectsEmptyAndOversized(t *testing.T) {
	c := NewWithConfig(Config{Capacity: 10, TTL: time.Minute, MaxBytes: 1024, MaxEntryBytes: 100})
	c.Set("k-empty", "m", "application/json", 200, nil, false)
	if n, _, _ := c.Stats(); n != 0 {
		t.Errorf("entries = %d, want 0 (an empty body must not be stored)", n)
	}
	// A body above the per-entry cap is rejected rather than stored: one huge
	// response would evict every useful entry to make room for data unlikely
	// to repeat.
	c.Set("k-big", "m", "application/json", 200, make([]byte, 101), false)
	if n, _, _ := c.Stats(); n != 0 {
		t.Errorf("entries = %d, want 0 (an oversized body must not be stored)", n)
	}
	// Exactly at the cap is accepted.
	c.Set("k-exact", "m", "application/json", 200, make([]byte, 100), false)
	if n, _, _ := c.Stats(); n != 1 {
		t.Errorf("entries = %d, want 1 (a body at the cap must be stored)", n)
	}
}

func TestByteBudgetEvictsLeastRecentlyUsed(t *testing.T) {
	c := NewWithConfig(Config{Capacity: 100, TTL: time.Minute, MaxBytes: 300, MaxEntryBytes: 100})
	for i := range 5 {
		c.Set(fmt.Sprintf("k%d", i), "m", "application/json", 200, make([]byte, 100), false)
	}
	entries, bytes, max, _, _ := c.StatsDetail()
	if entries != 3 {
		t.Errorf("entries = %d, want 3 (the byte budget is 300 with 100-byte entries)", entries)
	}
	if bytes > max {
		t.Errorf("bytes = %d, want <= max %d", bytes, max)
	}
	// The three survivors must be the most recently written: k0 and k1 were
	// evicted to make room.
	if _, hit := c.Get("k0"); hit {
		t.Error("k0 survived, want it evicted as least recently used")
	}
	if _, hit := c.Get("k1"); hit {
		t.Error("k1 survived, want it evicted as least recently used")
	}
	for _, k := range []string{"k2", "k3", "k4"} {
		if _, hit := c.Get(k); !hit {
			t.Errorf("%s was evicted, want it retained", k)
		}
	}
}

func TestByteBudgetTracksUpdates(t *testing.T) {
	c := NewWithConfig(Config{Capacity: 100, TTL: time.Minute, MaxBytes: 250, MaxEntryBytes: 100})
	c.Set("k", "m", "application/json", 200, make([]byte, 100), false)
	// Replacing the body with a smaller one must release the difference rather
	// than accumulate it, or the budget drifts and evicts live entries.
	c.Set("k", "m", "application/json", 200, make([]byte, 50), false)
	_, bytes, _, _, _ := c.StatsDetail()
	if bytes != 50 {
		t.Errorf("bytes = %d, want 50 after replacing a 100-byte entry with a 50-byte one", bytes)
	}
	// The freed 150 bytes must let two more entries in.
	c.Set("a", "m", "application/json", 200, make([]byte, 100), false)
	c.Set("b", "m", "application/json", 200, make([]byte, 100), false)
	if entries, _, _, _, _ := c.StatsDetail(); entries != 3 {
		t.Errorf("entries = %d, want 3", entries)
	}
}

func TestEntryCountBudgetStillApplies(t *testing.T) {
	c := NewWithConfig(Config{Capacity: 2, TTL: time.Minute, MaxBytes: 1 << 20, MaxEntryBytes: 1024})
	for i := range 5 {
		c.Set(fmt.Sprintf("k%d", i), "m", "application/json", 200, []byte("x"), false)
	}
	if entries, _, _, _, _ := c.StatsDetail(); entries != 2 {
		t.Errorf("entries = %d, want 2 (the entry-count budget applies too)", entries)
	}
}

func TestMaxEntryBytesClampedToTotalBudget(t *testing.T) {
	// A per-entry cap above the total budget would let one response evict the
	// whole cache; the two limits must not contradict each other.
	c := NewWithConfig(Config{Capacity: 10, TTL: time.Minute, MaxBytes: 100, MaxEntryBytes: 10_000})
	if got := c.MaxEntryBytes(); got != 100 {
		t.Errorf("MaxEntryBytes() = %d, want 100 (clamped to the total budget)", got)
	}
}

func TestTTLExpiresEntry(t *testing.T) {
	c := New(10, 30*time.Millisecond)
	c.Set("k", "m", "application/json", 200, []byte("body"), false)
	if _, hit := c.Get("k"); !hit {
		t.Fatal("Get() missed before the TTL elapsed")
	}
	time.Sleep(60 * time.Millisecond)
	if _, hit := c.Get("k"); hit {
		t.Error("Get() hit after the TTL elapsed")
	}
	// The expired entry must be dropped, not merely filtered on read.
	if entries, _, _, _, _ := c.StatsDetail(); entries != 0 {
		t.Errorf("entries = %d, want 0 after the TTL elapsed", entries)
	}
}

func TestClear(t *testing.T) {
	c := New(10, time.Minute)
	c.Set("a", "m", "application/json", 200, []byte("x"), false)
	c.Set("b", "m", "application/json", 200, []byte("y"), false)
	c.Clear()
	entries, bytes, _, _, _ := c.StatsDetail()
	if entries != 0 || bytes != 0 {
		t.Errorf("after Clear: entries=%d bytes=%d, want 0 and 0", entries, bytes)
	}
}

func TestStatsCountsHitsAndMisses(t *testing.T) {
	c := New(10, time.Minute)
	c.Set("k", "m", "application/json", 200, []byte("x"), false)
	c.Get("k")
	c.Get("k")
	c.Get("absent")
	_, hits, misses := c.Stats()
	if hits != 2 {
		t.Errorf("hits = %d, want 2", hits)
	}
	if misses != 1 {
		t.Errorf("misses = %d, want 1", misses)
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.Enabled {
		t.Error("DefaultConfig().Enabled = false, want true")
	}
	if cfg.MaxBytes != DefaultMaxBytes {
		t.Errorf("MaxBytes = %d, want %d", cfg.MaxBytes, DefaultMaxBytes)
	}
	if cfg.MaxEntryBytes != DefaultMaxEntryBytes {
		t.Errorf("MaxEntryBytes = %d, want %d", cfg.MaxEntryBytes, DefaultMaxEntryBytes)
	}
}

func TestConcurrentAccess(t *testing.T) {
	c := NewWithConfig(Config{Capacity: 50, TTL: time.Minute, MaxBytes: 4096, MaxEntryBytes: 512})
	var wg sync.WaitGroup
	// Concurrent readers and writers on the same key must not race or corrupt
	// the byte accounting.
	for i := range 20 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			key := fmt.Sprintf("k%d", idx%5)
			for range 50 {
				c.Set(key, "m", "application/json", 200, []byte(strings.Repeat("x", idx%50+1)), false)
				c.Get(key)
				c.StatsDetail()
			}
		}(i)
	}
	wg.Wait()
	entries, bytes, max, _, _ := c.StatsDetail()
	if bytes < 0 || bytes > max {
		t.Errorf("bytes = %d, want within [0, %d]", bytes, max)
	}
	if entries > 50 {
		t.Errorf("entries = %d, want <= capacity 50", entries)
	}
}
