package middleware

import (
	"context"
	"testing"
	"time"
)

// Provider-scope admission, exercised through the limiter's own state rather than
// through reported behaviour alone: a refusal that reported the right axis but
// leaked a concurrency slot or kept a TPM charge would look correct from the
// outside and be wrong in exactly the way that takes a node offline an hour later.

// bucketTokens reads a scope's token bucket. The bucket refills continuously, so
// every assertion below compares against a tolerance rather than an exact number.
func bucketTokens(rl *RateLimiter, scope string) float64 {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if bucket, ok := rl.tokens[scope]; ok {
		return bucket.tokens
	}
	return -1
}

func assertBucketNear(t *testing.T, rl *RateLimiter, scope string, want, tolerance float64) {
	t.Helper()
	got := bucketTokens(rl, scope)
	if got < 0 {
		t.Fatalf("no token bucket was created for %s", scope)
	}
	if diff := got - want; diff > tolerance || diff < -tolerance {
		t.Errorf("bucket tokens for %s = %.1f, want %.1f (±%.0f)", scope, got, want, tolerance)
	}
}

func TestAcquireProvider_UnlimitedProviderIsNotTracked(t *testing.T) {
	rl := NewRateLimiter(time.Minute)

	slot, refusal := rl.AcquireProvider(context.Background(), "node-nara", Limits{}, 5_000)
	if slot != nil || refusal != nil {
		t.Fatalf("a provider with no limits returned slot=%v refusal=%v, want nil/nil", slot, refusal)
	}
	if windowCount, active := rl.Snapshot(ProviderScopeKey("node-nara")); windowCount != 0 || active != 0 {
		t.Errorf("an unlimited provider was recorded (window=%d active=%d), want no trace at all", windowCount, active)
	}
}

func TestAcquireProvider_RPMRefusesOnceTheWindowIsFull(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	ctx := context.Background()
	limits := Limits{RPM: 1}

	slot, refusal := rl.AcquireProvider(ctx, "node-nara", limits, 0)
	if slot == nil || refusal != nil {
		t.Fatalf("first attempt = slot %v refusal %v, want an admitted slot", slot, refusal)
	}
	slot.Release()

	// A second provider keeps its own window: one saturated node must not refuse
	// capacity the other still has.
	other, otherRefusal := rl.AcquireProvider(ctx, "node-kopi", limits, 0)
	if other == nil || otherRefusal != nil {
		t.Fatalf("second provider = slot %v refusal %v, want an admitted slot", other, otherRefusal)
	}
	other.Release()

	refused, refusal := rl.AcquireProvider(ctx, "node-nara", limits, 0)
	if refusal == nil {
		t.Fatalf("second attempt on the same provider was admitted (%v), want a refusal", refused)
	}
	if refusal.Axis != "rpm" {
		t.Errorf("refusal axis = %q, want \"rpm\"", refusal.Axis)
	}
	if refusal.Limit != 1 {
		t.Errorf("refusal limit = %d, want the configured 1", refusal.Limit)
	}
	if refusal.RetryAfter <= 0 {
		t.Errorf("refusal RetryAfter = %v, want a positive wait", refusal.RetryAfter)
	}
	if refused != nil {
		t.Error("a refusal returned a slot to release, which would double-count concurrency")
	}
}

// TestAcquireProvider_ConcurrencySlotIsReleasedOnRelease is the D6 guarantee at
// the limiter level: failover releases the slot, so the next attempt can be
// admitted instead of the provider locking itself out.
func TestAcquireProvider_ConcurrencySlotIsReleasedOnRelease(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	ctx := context.Background()
	limits := Limits{Concurrency: 1}

	first, refusal := rl.AcquireProvider(ctx, "node-nara", limits, 0)
	if first == nil || refusal != nil {
		t.Fatalf("first attempt = slot %v refusal %v, want an admitted slot", first, refusal)
	}
	if _, active := rl.Snapshot(ProviderScopeKey("node-nara")); active != 1 {
		t.Errorf("active concurrency = %d, want 1 while the attempt is in flight", active)
	}

	_, busy := rl.AcquireProvider(ctx, "node-nara", limits, 0)
	if busy == nil || busy.Axis != "concurrency" {
		t.Fatalf("attempt past the concurrency limit = %v, want a concurrency refusal", busy)
	}

	first.Release()
	if _, active := rl.Snapshot(ProviderScopeKey("node-nara")); active != 0 {
		t.Errorf("active concurrency = %d after release, want 0", active)
	}
	second, secondRefusal := rl.AcquireProvider(ctx, "node-nara", limits, 0)
	if second == nil || secondRefusal != nil {
		t.Fatalf("attempt after release = slot %v refusal %v, want an admitted slot", second, secondRefusal)
	}
	second.Release()
}

func TestAcquireProvider_QueueTimeoutRefusalNamesTheAxis(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	ctx := context.Background()
	limits := Limits{RPM: 1, Concurrency: 1, QueueTimeout: 30 * time.Millisecond}

	held, refusal := rl.AcquireProvider(ctx, "node-nara", limits, 0)
	if held == nil || refusal != nil {
		t.Fatalf("first attempt = slot %v refusal %v, want an admitted slot", held, refusal)
	}
	defer held.Release()

	start := time.Now()
	_, waited := rl.AcquireProvider(ctx, "node-nara", limits, 0)
	elapsed := time.Since(start)
	if waited == nil {
		t.Fatal("the second attempt was admitted while both the window and the slot were full")
	}
	if elapsed < 25*time.Millisecond {
		t.Errorf("refusal came after %v, want the attempt to wait out its 30ms queue timeout", elapsed)
	}
	if waited.Axis != "concurrency" {
		t.Errorf("refusal axis = %q, want \"concurrency\" when both axes are capped", waited.Axis)
	}
	if waited.RetryAfter < time.Second {
		t.Errorf("refusal RetryAfter = %v, want at least the timeout it just spent", waited.RetryAfter)
	}
}

func TestAcquireProvider_TPMRefusesPromptThatDoesNotFit(t *testing.T) {
	rl := NewRateLimiter(time.Minute)

	refused, refusal := rl.AcquireProvider(context.Background(), "node-nara", Limits{TPM: 1_000}, 5_000)
	if refusal == nil {
		t.Fatalf("an oversized prompt was admitted (%v), want a TPM refusal", refused)
	}
	if refusal.Axis != "tpm" {
		t.Errorf("refusal axis = %q, want \"tpm\"", refusal.Axis)
	}
	if refusal.Limit != 1_000 {
		t.Errorf("refusal limit = %d, want the configured 1000", refusal.Limit)
	}
	if refusal.RetryAfter <= 0 {
		t.Errorf("refusal RetryAfter = %v, want the bucket's refill time", refusal.RetryAfter)
	}
}

// TestAcquireProvider_RefundsTPMWhenTheQueueRefuses: the charge is taken before
// the queue, and a request the queue then refuses was never dispatched, so the
// full amount comes back. Without this a provider under a burst spends its token
// budget on attempts that produced nothing.
func TestAcquireProvider_RefundsTPMWhenTheQueueRefuses(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	ctx := context.Background()
	limits := Limits{RPM: 1, TPM: 1_000}

	// The first attempt carries a prompt so the TPM bucket exists at all: a
	// provider limit with no prompt to charge creates no bucket (nothing to
	// charge), and the assertion below needs one to look at.
	held, refusal := rl.AcquireProvider(ctx, "node-nara", limits, 100)
	if held == nil || refusal != nil {
		t.Fatalf("first attempt = slot %v refusal %v, want an admitted slot", held, refusal)
	}
	held.Release()
	assertBucketNear(t, rl, ProviderScopeKey("node-nara"), 1_000, 50)

	_, refused := rl.AcquireProvider(ctx, "node-nara", limits, 300)
	if refused == nil || refused.Axis != "rpm" {
		t.Fatalf("second attempt refusal = %v, want an rpm refusal", refused)
	}
	assertBucketNear(t, rl, ProviderScopeKey("node-nara"), 1_000, 50)
}

func TestProviderSlot_SettleCorrectsOnlyAnUnderEstimate(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	scope := ProviderScopeKey("node-nara")
	slot, refusal := rl.AcquireProvider(context.Background(), "node-nara", Limits{TPM: 1_000}, 100)
	if slot == nil || refusal != nil {
		t.Fatalf("attempt = slot %v refusal %v, want an admitted slot", slot, refusal)
	}
	assertBucketNear(t, rl, scope, 900, 50)

	// The turn really cost 300: the extra 200 comes off the bucket.
	slot.Settle(300)
	assertBucketNear(t, rl, scope, 700, 50)

	// A second settle has nothing left to reconcile, and an over-stated total
	// never banks credit back.
	slot.Settle(50)
	assertBucketNear(t, rl, scope, 700, 50)
}

func TestProviderSlot_ReleaseRefundsAnUnsettledReservation(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	scope := ProviderScopeKey("node-nara")
	slot, refusal := rl.AcquireProvider(context.Background(), "node-nara",
		Limits{TPM: 1_000, Concurrency: 1}, 400)
	if slot == nil || refusal != nil {
		t.Fatalf("attempt = slot %v refusal %v, want an admitted slot", slot, refusal)
	}
	assertBucketNear(t, rl, scope, 600, 50)

	// The attempt died before the meter ran — an upstream error, a client that
	// hung up — so the charge is not a spend and the slot is not held.
	slot.Release()
	assertBucketNear(t, rl, scope, 1_000, 50)
	if _, active := rl.Snapshot(scope); active != 0 {
		t.Errorf("active concurrency = %d after release, want 0", active)
	}
}

func TestSnapshot_ExcludesStaleStamps(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	scope := ProviderScopeKey("node-nara")
	slot, refusal := rl.AcquireProvider(context.Background(), "node-nara", Limits{RPM: 5}, 0)
	if slot == nil || refusal != nil {
		t.Fatalf("attempt = slot %v refusal %v, want an admitted slot", slot, refusal)
	}
	slot.Release()

	// A stamp from a previous window: reporting it would describe the peak of
	// every window that ever rolled, which is the number that makes an operator
	// raise a limit that is already generous.
	rl.mu.Lock()
	rl.windows[scope].stamps = append([]time.Time{time.Now().Add(-2 * time.Minute)}, rl.windows[scope].stamps...)
	rl.mu.Unlock()

	windowCount, _ := rl.Snapshot(scope)
	if windowCount != 1 {
		t.Errorf("window count = %d, want 1 (the live stamp only)", windowCount)
	}
}
