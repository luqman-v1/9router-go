//go:build integration

package integration

import (
	"testing"
)

// TestQoderCheckinsEndpoint proves the read-only check-in state route is wired
// into the production router ahead of the /usage/{connectionId} catch-all and
// answers an empty ledger without touching the network.
func TestQoderCheckinsEndpoint(t *testing.T) {
	env := newEnv(t)
	r := env.Do(t, "GET", "/api/qoder/checkins", nil)
	if r.Status != 200 {
		t.Fatalf("status = %d, body = %s", r.Status, r.Body)
	}
	var body struct {
		Checkins []struct {
			Outcome  string `json:"outcome"`
			Provider string `json:"provider"`
		} `json:"checkins"`
	}
	r.Decode(t, &body)
	if len(body.Checkins) != 0 {
		t.Errorf("expected no stored check-ins on a fresh gateway, got %+v", body.Checkins)
	}
}
