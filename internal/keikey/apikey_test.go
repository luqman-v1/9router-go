package keikey

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLookupHash(t *testing.T) {
	got := LookupHash("sk-abc123")
	sum := sha256.Sum256([]byte("sk-abc123"))
	want := hex.EncodeToString(sum[:])
	if got != want {
		t.Fatalf("LookupHash = %q, want %q", got, want)
	}
	if len(got) != 64 {
		t.Fatalf("LookupHash length = %d, want 64", len(got))
	}
	if LookupHash("sk-abc123") == LookupHash("sk-abc124") {
		t.Fatal("distinct keys produced the same lookup hash")
	}
}

func TestHashVerifyRoundTrip(t *testing.T) {
	const secret = "sk-test-0123456789abcdef"
	encoded, err := Hash(secret)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	for _, part := range []string{"$argon2id$", "v=19", "m=65536", "t=3", "p=4"} {
		if !strings.Contains(encoded, part) {
			t.Fatalf("verifier %q missing %q", encoded, part)
		}
	}
	if strings.Contains(encoded, secret) {
		t.Fatal("verifier leaks the plaintext secret")
	}
	if !Verify(secret, encoded) {
		t.Fatal("Verify rejected the correct plaintext")
	}
	if Verify("sk-test-0123456789abcdeee", encoded) {
		t.Fatal("Verify accepted a wrong plaintext")
	}
}

func TestHashIsSalted(t *testing.T) {
	a, err := Hash("sk-same")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	b, err := Hash("sk-same")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if a == b {
		t.Fatal("two hashes of the same secret are identical; salt is not random")
	}
	if !Verify("sk-same", a) || !Verify("sk-same", b) {
		t.Fatal("both verifiers should accept the plaintext")
	}
}

func TestVerifyMalformed(t *testing.T) {
	cases := []struct {
		name      string
		encoded   string
		plaintext string
	}{
		{"empty", "", "sk-x"},
		{"not a phc string", "deadbeef", "sk-x"},
		{"unknown algorithm", "$argon2i$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA", "sk-x"},
		{"missing key segment", "$argon2id$v=19$m=65536,t=3,p=4$c2FsdA", "sk-x"},
		{"bad base64 salt", "$argon2id$v=19$m=65536,t=3,p=4$!!!$aGFzaA", "sk-x"},
		{"bad params", "$argon2id$v=19$m=abc$c2FsdA$aGFzaA", "sk-x"},
		{"no params", "$argon2id$$c2FsdA$aGFzaA", "sk-x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if Verify(tc.plaintext, tc.encoded) {
				t.Fatal("malformed verifier accepted")
			}
		})
	}
}

func TestMask(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "***"},
		{"short", "***"},
		{"sk-abcdefghijkl", "sk-a…ijkl"},
		{"sk-0123456789abcdef", "sk-0…cdef"},
	}
	for _, tc := range cases {
		if got := Mask(tc.in); got != tc.want {
			t.Fatalf("Mask(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if strings.Contains(Mask("sk-0123456789abcdef"), "12345678") {
		t.Fatal("mask leaks the secret body")
	}
}

func TestCacheGetSetAndTTL(t *testing.T) {
	c := NewCache(30*time.Millisecond, 4)
	if _, ok := c.Get("missing"); ok {
		t.Fatal("empty cache returned a hit")
	}
	c.Set("a", "valueA")
	got, ok := c.Get("a")
	if !ok || got.(string) != "valueA" {
		t.Fatalf("Get = %v, %v; want valueA, true", got, ok)
	}
	time.Sleep(60 * time.Millisecond)
	if _, ok := c.Get("a"); ok {
		t.Fatal("expired entry still returned")
	}
	if c.Len() != 0 {
		t.Fatalf("expired entry not evicted, len = %d", c.Len())
	}
}

func TestCacheCapacityEvictsOldest(t *testing.T) {
	c := NewCache(time.Minute, 2)
	c.Set("first", 1)
	c.Set("second", 2)
	c.Set("third", 3)
	if _, ok := c.Get("first"); ok {
		t.Fatal("oldest entry survived past capacity")
	}
	if _, ok := c.Get("third"); !ok {
		t.Fatal("newest entry missing")
	}
	if c.Len() > 2 {
		t.Fatalf("cache grew past capacity: %d", c.Len())
	}
}

func TestCacheZeroArgsUseDefaults(t *testing.T) {
	c := NewCache(0, 0)
	if c.ttl != CacheTTL || c.max != CacheCapacity {
		t.Fatalf("NewCache(0,0) = ttl %v max %d; want %v / %d", c.ttl, c.max, CacheTTL, CacheCapacity)
	}
}

func TestCacheOverwriteDoesNotGrowOrder(t *testing.T) {
	c := NewCache(time.Minute, 4)
	for i := 0; i < 10; i++ {
		c.Set("same", i)
	}
	if c.Len() != 1 {
		t.Fatalf("overwrites stored duplicates: len = %d", c.Len())
	}
	got, ok := c.Get("same")
	if !ok || got.(int) != 9 {
		t.Fatalf("Get = %v, %v; want 9, true", got, ok)
	}
}

func TestCacheConcurrentAccess(t *testing.T) {
	c := NewCache(time.Minute, 64)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := LookupHash(strings.Repeat("k", i+1))
			for j := 0; j < 50; j++ {
				c.Set(key, i)
				c.Get(key)
			}
		}(i)
	}
	wg.Wait()
	if c.Len() > 64 {
		t.Fatalf("cache exceeded capacity under concurrency: %d", c.Len())
	}
}

func TestNilCacheIsSafe(t *testing.T) {
	var c *Cache
	c.Set("a", 1)
	if _, ok := c.Get("a"); ok {
		t.Fatal("nil cache returned a hit")
	}
	if c.Len() != 0 {
		t.Fatal("nil cache reported entries")
	}
}
