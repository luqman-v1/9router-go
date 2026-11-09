package oauth

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunRefresh_CollapsesConcurrentCallers(t *testing.T) {
	var calls atomic.Int32
	previous := Get("test-collapse")
	Register("test-collapse", func(_ context.Context, _ *Params) (*TokenResult, error) {
		calls.Add(1)
		time.Sleep(30 * time.Millisecond)
		return &TokenResult{AccessToken: "fresh-token", ExpiresIn: 3600}, nil
	})
	t.Cleanup(func() { Register("test-collapse", previous) })

	const n = 10
	got := make([]string, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(idx int) {
			defer wg.Done()
			out := RunRefresh("conn-collapse",
				func() (bool, error) { return true, nil },
				func() (string, error) {
					res, err := Refresh(context.Background(), &Params{Provider: "test-collapse", RefreshToken: "rt"})
					if err != nil {
						return "", err
					}
					return res.AccessToken, nil
				},
			)
			got[idx] = out.Token
		}(i)
	}
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Fatalf("refresher called %d times, want exactly 1", got)
	}
	for i, tok := range got {
		if tok != "fresh-token" {
			t.Errorf("caller %d got %q, want fresh-token", i, tok)
		}
	}
}

func TestRunRefresh_FailureKeepsEachCallersCredential(t *testing.T) {
	var calls atomic.Int32
	previous := Get("test-fail")
	Register("test-fail", func(_ context.Context, _ *Params) (*TokenResult, error) {
		calls.Add(1)
		time.Sleep(30 * time.Millisecond)
		return nil, errors.New("invalid_grant")
	})
	t.Cleanup(func() { Register("test-fail", previous) })

	const n = 10
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(idx int) {
			defer wg.Done()
			out := RunRefresh("conn-fail",
				func() (bool, error) { return true, nil },
				func() (string, error) {
					_, err := Refresh(context.Background(), &Params{Provider: "test-fail", RefreshToken: "rt"})
					return "", err
				},
			)
			errs[idx] = out.Err
		}(i)
	}
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Fatalf("refresher called %d times, want exactly 1", got)
	}
	for i, err := range errs {
		if err == nil {
			t.Errorf("caller %d got nil error, want the refresh failure", i)
		}
	}
}

func TestRunRefresh_NoExchangeWhenNotNeeded(t *testing.T) {
	var calls atomic.Int32
	out := RunRefresh("conn-healthy",
		func() (bool, error) { return false, nil },
		func() (string, error) {
			calls.Add(1)
			return "unused", nil
		},
	)
	if out.Err != nil {
		t.Fatalf("unexpected error: %v", out.Err)
	}
	if out.Token != "" {
		t.Errorf("got token %q, want empty when no refresh is needed", out.Token)
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("refreshFn called %d times, want 0", got)
	}
}

func TestRunRefresh_DistinctConnectionsDoNotBlock(t *testing.T) {
	// Two different connections must not serialise against each other: the
	// lock is per connection, not global.
	start := time.Now()
	var wg sync.WaitGroup
	wg.Add(2)
	for _, id := range []string{"conn-a", "conn-b"} {
		go func(connectionID string) {
			defer wg.Done()
			RunRefresh(connectionID,
				func() (bool, error) { return true, nil },
				func() (string, error) {
					time.Sleep(100 * time.Millisecond)
					return "tok", nil
				},
			)
		}(id)
	}
	wg.Wait()
	if elapsed := time.Since(start); elapsed > 180*time.Millisecond {
		t.Errorf("two connections took %v, want them to run concurrently", elapsed)
	}
}
