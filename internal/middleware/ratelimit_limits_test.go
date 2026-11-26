package middleware

import (
	"context"
	"strconv"
	"testing"
	"time"
)

// The hourly and daily ceilings are the axes a reseller tier is actually sold
// on ("10k a day"). Before this they did not exist: the limiter held one
// window, so a key could sit far under its minute cap all day and still be the
// one draining the upstream account. These tests drive the windows directly
// rather than waiting an hour or a day, which is why hourWindow and dayWindow
// are constants the tests can reason about instead of fields they must outlive.

// A daily ceiling blocks the request that would exceed it, and names itself so
// a 429 tells the operator which window tripped.
func TestCheckWithWindowsBlocksDailyCeiling(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	const key = "daily"

	for i := range 3 {
		allowed, _, scope := rl.CheckWithWindows(key, 0, 0, 3, 0, 0, 0)
		if !allowed {
			t.Fatalf("request %d refused unexpectedly (scope %q)", i+1, scope)
		}
	}

	allowed, retryAfter, scope := rl.CheckWithWindows(key, 0, 0, 3, 0, 0, 0)
	if allowed {
		t.Fatal("the fourth request was admitted past a daily ceiling of 3")
	}
	if scope != "rpd" {
		t.Errorf("scope = %q, want %q", scope, "rpd")
	}
	if retryAfter < 1 {
		t.Errorf("retryAfter = %d, want at least 1 second", retryAfter)
	}
}

// The minute window still governs a burst even when the day window has room,
// so adding the longer ceilings did not weaken the short one.
func TestCheckWithWindowsKeepsMinuteWindow(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	const key = "burst"

	for range 2 {
		if allowed, _, _ := rl.CheckWithWindows(key, 2, 0, 100, 0, 0, 0); !allowed {
			t.Fatal("request refused inside the minute ceiling")
		}
	}
	allowed, _, scope := rl.CheckWithWindows(key, 2, 0, 100, 0, 0, 0)
	if allowed {
		t.Fatal("the third request was admitted past a minute ceiling of 2")
	}
	if scope != "rpm" {
		t.Errorf("scope = %q, want %q: the narrowest blocked window names itself", scope, "rpm")
	}
}

// All three ceilings share one stamp list, so a request counted by the day
// window is the same request counted by the minute window. Two lists would let
// them disagree about how many requests were admitted.
func TestWindowSpansShareOneStampList(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	const key = "shared"

	for range 5 {
		if allowed, _, _ := rl.CheckWithWindows(key, 0, 0, 10, 0, 0, 0); !allowed {
			t.Fatal("request refused inside the daily ceiling")
		}
	}

	rl.mu.Lock()
	stamps := len(rl.windows[key].stamps)
	rl.mu.Unlock()
	if stamps != 5 {
		t.Errorf("stamp list holds %d entries after 5 admitted requests, want 5", stamps)
	}
}

// A key with no request ceiling configured stores no stamps at all: an install
// that never set a limit must not grow a per-key timestamp list it never reads.
func TestNoCeilingStoresNoStamps(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	const key = "uncapped"

	if allowed, _, _ := rl.CheckWithWindows(key, 0, 0, 0, 0, 0, 0); !allowed {
		t.Fatal("an uncapped key was refused")
	}
	rl.mu.Lock()
	_, exists := rl.windows[key]
	rl.mu.Unlock()
	if exists {
		t.Error("a stamp list was created for a key with no request ceiling")
	}
}

// The per-IP ceiling is the axis that survives a caller minting fresh keys, so
// it is enforced against the address rather than the key.
func TestAcquireIPBlocksPastHostCeiling(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	limits := IPLimits{PerMinute: 2}

	for i := range 2 {
		if _, ok := rl.AcquireIP(context.Background(), "203.0.113.7", limits, 0); !ok {
			t.Fatalf("request %d refused inside the host ceiling", i+1)
		}
	}
	if _, ok := rl.AcquireIP(context.Background(), "203.0.113.7", limits, 0); ok {
		t.Fatal("the third request from one address was admitted past a ceiling of 2")
	}
	// A different address is a different budget: the ceiling is per host, not
	// global, or one noisy caller would deny the whole install.
	if _, ok := rl.AcquireIP(context.Background(), "198.51.100.9", limits, 0); !ok {
		t.Fatal("a second address was refused by the first address's budget")
	}
}

// An unconfigured IP ceiling admits everything and records nothing, which is
// what every install that never set one must pay.
func TestAcquireIPWithoutCeilingIsFree(t *testing.T) {
	rl := NewRateLimiter(time.Minute)

	if _, ok := rl.AcquireIP(context.Background(), "203.0.113.7", IPLimits{}, 0); !ok {
		t.Fatal("an unconfigured IP ceiling refused a request")
	}
	rl.mu.Lock()
	count := len(rl.windows)
	rl.mu.Unlock()
	if count != 0 {
		t.Errorf("limiter holds %d windows after an unconfigured IP admit, want 0", count)
	}
}

// The IP slot is namespaced, so an address can never collide with a key id or a
// provider id that happens to spell the same string.
func TestIPScopeKeyIsNamespaced(t *testing.T) {
	if got, want := IPScopeKey("10.0.0.1"), "ip:10.0.0.1"; got != want {
		t.Errorf("IPScopeKey = %q, want %q", got, want)
	}
	if IPScopeKey("") == "" {
		t.Error("IPScopeKey of an empty address must still be a non-empty, non-colliding scope")
	}
}

// The retry hint is computed against the window that refused, not always the
// minute window. A daily refusal that answered "retry in 60s" would have a
// client hammer the endpoint all day.
func TestWindowRetryAfterUsesTheBlockingWindow(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	const key = "hint"

	// One request per day: the day window is what refuses the next one.
	if allowed, _, _ := rl.CheckWithWindows(key, 0, 0, 1, 0, 0, 0); !allowed {
		t.Fatal("first request refused")
	}
	allowed, retryAfter, scope := rl.CheckWithWindows(key, 0, 0, 1, 0, 0, 0)
	if allowed {
		t.Fatal("second request admitted past a daily ceiling of 1")
	}
	if scope != "rpd" {
		t.Fatalf("scope = %q, want rpd", scope)
	}
	// A day window's hint is measured in hours, so it must be far above the
	// 61 seconds a minute window could ever report.
	if retryAfter <= 61 {
		t.Errorf("retryAfter = %ds for a daily refusal, want a hint measured against the day window", retryAfter)
	}
	if _, err := strconv.Atoi(strconv.Itoa(retryAfter)); err != nil {
		t.Errorf("retryAfter is not an integer: %v", err)
	}
}

// The per-client-IP ceiling is concurrency-only work when an operator sets just
// `perIpConcurrency`: AcquireIP -> acquireScoped -> tryAdmitSpans ->
// activeCounterLocked creates an active[ip] entry and returns WITHOUT recording
// a stamp, so the sweep in recordStampLocked never ran for that path. A probe
// with 5000 distinct addresses and maxKeys=20 left 5000 live entries, which is
// the unbounded client-keyed map AGENTS.md §4.G forbids.
func TestAcquireIPConcurrencyMapStaysBounded(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	rl.maxKeys = 20
	limits := IPLimits{Concurrency: 5}

	for i := range 5000 {
		ip := "10." + strconv.Itoa(i/65536%256) + "." + strconv.Itoa(i/256%256) + "." + strconv.Itoa(i%256)
		if _, ok := rl.AcquireIP(context.Background(), ip, limits, 0); !ok {
			t.Fatalf("address %s refused under a ceiling of 5", ip)
		}
		// Release immediately: this test is about the map's bound, not the slot.
		rl.ReleaseConcurrency(IPScopeKey(ip))
	}

	rl.mu.Lock()
	live := len(rl.active)
	rl.mu.Unlock()
	// maxKeys is the bound; the sweep targets 90% but one admit may push it to
	// the trigger before the next sweep fires, so allow the ceiling itself.
	if live > rl.maxKeys {
		t.Errorf("active map holds %d entries after 5000 distinct addresses, want <= %d", live, rl.maxKeys)
	}
}
