package oauth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

var errTestFailure = errors.New("test failure")

func TestDedupRefresh_ConcurrentCallsCollapse(t *testing.T) {
	ResetRefreshCache()
	defer ResetRefreshCache()

	callCount := 0
	var mu sync.Mutex
	fn := func(ctx context.Context) (*TokenResult, error) {
		mu.Lock()
		callCount++
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		return &TokenResult{AccessToken: "new-token", ExpiresIn: 3600}, nil
	}

	var wg sync.WaitGroup
	results := make(chan *TokenResult, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := DedupRefresh(context.Background(), "test-provider", "old-token", false, fn)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			results <- res
		}()
	}
	wg.Wait()
	close(results)

	for res := range results {
		if res.AccessToken != "new-token" {
			t.Errorf("unexpected token: %s", res.AccessToken)
		}
	}

	mu.Lock()
	if callCount != 1 {
		t.Errorf("expected 1 call, got %d", callCount)
	}
	mu.Unlock()
}

func TestDedupRefresh_CachedResultReturned(t *testing.T) {
	ResetRefreshCache()
	defer ResetRefreshCache()

	callCount := 0
	fn := func(ctx context.Context) (*TokenResult, error) {
		callCount++
		return &TokenResult{AccessToken: "cached-token", ExpiresIn: 3600}, nil
	}

	// First call
	res1, err := DedupRefresh(context.Background(), "test-provider", "old-token", false, fn)
	if err != nil {
		t.Fatalf("first call error: %v", err)
	}
	if res1.AccessToken != "cached-token" {
		t.Errorf("unexpected token: %s", res1.AccessToken)
	}

	// Second call within TTL
	res2, err := DedupRefresh(context.Background(), "test-provider", "old-token", false, fn)
	if err != nil {
		t.Fatalf("second call error: %v", err)
	}
	if res2.AccessToken != "cached-token" {
		t.Errorf("unexpected token: %s", res2.AccessToken)
	}

	if callCount != 1 {
		t.Errorf("expected 1 call, got %d", callCount)
	}
}

func TestDedupRefresh_FailureNotCached(t *testing.T) {
	ResetRefreshCache()
	defer ResetRefreshCache()

	callCount := 0
	fn := func(ctx context.Context) (*TokenResult, error) {
		callCount++
		return nil, errTestFailure
	}

	// First call fails
	_, err := DedupRefresh(context.Background(), "test-provider", "old-token", false, fn)
	if err == nil {
		t.Fatal("expected error on first call")
	}

	// Second call should also fail (not cached)
	_, err = DedupRefresh(context.Background(), "test-provider", "old-token", false, fn)
	if err == nil {
		t.Fatal("expected error on second call")
	}

	if callCount != 2 {
		t.Errorf("expected 2 calls, got %d", callCount)
	}
}

func TestDedupRefresh_EmptyTokenBypassesCache(t *testing.T) {
	ResetRefreshCache()
	defer ResetRefreshCache()

	callCount := 0
	fn := func(ctx context.Context) (*TokenResult, error) {
		callCount++
		return &TokenResult{AccessToken: "bypass-token", ExpiresIn: 3600}, nil
	}

	// Empty provider
	res1, err := DedupRefresh(context.Background(), "", "old-token", false, fn)
	if err != nil {
		t.Fatalf("empty provider error: %v", err)
	}
	if res1.AccessToken != "bypass-token" {
		t.Errorf("unexpected token: %s", res1.AccessToken)
	}

	// Empty oldToken
	res2, err := DedupRefresh(context.Background(), "test-provider", "", false, fn)
	if err != nil {
		t.Fatalf("empty oldToken error: %v", err)
	}
	if res2.AccessToken != "bypass-token" {
		t.Errorf("unexpected token: %s", res2.AccessToken)
	}

	if callCount != 2 {
		t.Errorf("expected 2 calls, got %d", callCount)
	}
}

func TestDedupRefresh_DifferentKeysSeparateCache(t *testing.T) {
	ResetRefreshCache()
	defer ResetRefreshCache()

	callCount := 0
	fn := func(ctx context.Context) (*TokenResult, error) {
		callCount++
		return &TokenResult{AccessToken: "token", ExpiresIn: 3600}, nil
	}

	// Different providers
	_, _ = DedupRefresh(context.Background(), "provider1", "old-token", false, fn)
	_, _ = DedupRefresh(context.Background(), "provider2", "old-token", false, fn)

	// Different tokens
	_, _ = DedupRefresh(context.Background(), "provider1", "old-token-2", false, fn)

	if callCount != 3 {
		t.Errorf("expected 3 calls, got %d", callCount)
	}
}

func TestDedupRefresh_ForceBypassesCache(t *testing.T) {
	ResetRefreshCache()
	defer ResetRefreshCache()

	callCount := 0
	fn := func(ctx context.Context) (*TokenResult, error) {
		callCount++
		return &TokenResult{AccessToken: fmt.Sprintf("token-%d", callCount), ExpiresIn: 3600}, nil
	}

	res1, err := DedupRefresh(context.Background(), "test-provider", "old-token", false, fn)
	if err != nil {
		t.Fatalf("first call error: %v", err)
	}
	if res1.AccessToken != "token-1" {
		t.Errorf("got %s, want token-1", res1.AccessToken)
	}

	res2, err := DedupRefresh(context.Background(), "test-provider", "old-token", false, fn)
	if err != nil {
		t.Fatalf("second call error: %v", err)
	}
	if res2.AccessToken != "token-1" {
		t.Errorf("got %s, want token-1 from cache", res2.AccessToken)
	}
	if callCount != 1 {
		t.Errorf("expected 1 call, got %d", callCount)
	}

	res3, err := DedupRefresh(context.Background(), "test-provider", "old-token", true, fn)
	if err != nil {
		t.Fatalf("forced call error: %v", err)
	}
	if res3.AccessToken != "token-2" {
		t.Errorf("got %s, want token-2 from forced refresh", res3.AccessToken)
	}
	if callCount != 2 {
		t.Errorf("expected 2 calls after force, got %d", callCount)
	}
}

func TestDedupRefresh_AliasSharesCache(t *testing.T) {
	ResetRefreshCache()
	defer ResetRefreshCache()

	callCount := 0
	fn := func(ctx context.Context) (*TokenResult, error) {
		callCount++
		return &TokenResult{AccessToken: "shared-token", ExpiresIn: 3600}, nil
	}

	res1, err := DedupRefresh(context.Background(), "gb", "same-token", false, fn)
	if err != nil {
		t.Fatalf("gb call error: %v", err)
	}
	if res1.AccessToken != "shared-token" {
		t.Errorf("got %s, want shared-token", res1.AccessToken)
	}

	res2, err := DedupRefresh(context.Background(), "grok-cli", "same-token", false, fn)
	if err != nil {
		t.Fatalf("grok-cli call error: %v", err)
	}
	if res2.AccessToken != "shared-token" {
		t.Errorf("got %s, want shared-token", res2.AccessToken)
	}

	if callCount != 1 {
		t.Errorf("expected 1 call shared between aliases, got %d", callCount)
	}
}
