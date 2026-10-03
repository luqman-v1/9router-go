//go:build integration

package integration

import (
	"net/http"
	"testing"
	"time"
)

// TestForceFallbackUsesAccountThatFreesUpFirst pins the whole feature across
// the real router: a provider outage parks every account in cooldown, so with
// the toggle on the gateway must still serve the client turn through the
// account that resets first instead of failing the request. Accounts recovered
// on their own must go back to normal rotation, so a case here pins the toggle
// off as the default too.
func TestForceFallbackUsesAccountThatFreesUpFirst(t *testing.T) {
	t.Run("toggle off keeps failing with the real throttle", func(t *testing.T) {
		env := newEnv(t)

		throttled := env.NewUpstream(t, JSONResponder(http.StatusTooManyRequests,
			`{"error":{"message":"quota exhausted","type":"rate_limit_error"}}`))
		env.AddConnection(t, "conn-1", "deepseek", "Only Account", throttled, "sk-one")

		// Park the only account so the next request finds nothing selectable.
		until := time.Now().UTC().Add(5 * time.Minute)
		if err := env.Repo.LockConnectionRateLimit("conn-1", until, 1, http.StatusTooManyRequests, "quota exhausted"); err != nil {
			t.Fatalf("park account: %v", err)
		}

		res := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false))
		if res.Status == http.StatusOK {
			t.Fatalf("expected the request to fail with force fallback off, got 200: %s", truncate(res.Body))
		}
		if got := throttled.Count(); got != 0 {
			t.Errorf("parked upstream received %d requests, want 0", got)
		}
	})

	t.Run("toggle on serves the soonest to reset", func(t *testing.T) {
		env := newEnv(t)

		later := env.NewUpstream(t, chatCompletionResponder())
		sooner := env.NewUpstream(t, chatCompletionResponder())
		env.AddConnection(t, "conn-later", "deepseek", "Long Cooldown", later, "sk-later")
		env.AddConnection(t, "conn-sooner", "deepseek", "Short Cooldown", sooner, "sk-sooner")

		park := func(connID string, d time.Duration) {
			t.Helper()
			until := time.Now().UTC().Add(d)
			if err := env.Repo.LockConnectionRateLimit(connID, until, 1, http.StatusTooManyRequests, "quota exhausted"); err != nil {
				t.Fatalf("park %s: %v", connID, err)
			}
		}
		// Priority order puts the LONGER cooldown first, so serving the second
		// account proves the choice is by remaining cooldown, not list order.
		park("conn-later", 10*time.Minute)
		park("conn-sooner", 2*time.Minute)

		if err := env.Repo.UpdateSettingsRaw(map[string]any{"forceFallback": true}); err != nil {
			t.Fatalf("enable force fallback: %v", err)
		}

		res := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false))
		if res.Status != http.StatusOK {
			t.Fatalf("POST /v1/chat/completions = %d, want 200 with force fallback on (body: %s)",
				res.Status, truncate(res.Body))
		}
		if got := sooner.Count(); got != 1 {
			t.Errorf("soonest-to-reset upstream received %d requests, want 1", got)
		}
		if got := later.Count(); got != 0 {
			t.Errorf("longer-cooldown upstream received %d requests, want 0", got)
		}
	})
}