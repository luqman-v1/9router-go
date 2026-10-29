package chat

import "testing"

// A burst on one credential is the step between a per-model throttle (6004) and
// an account restriction (11140), so the ceiling has to bind per account and
// refuse rather than queue: a refused attempt must cost no upstream call.
func TestConnectionGate_RefusesOverCeilingAndFrees(t *testing.T) {
	t.Setenv(accountInFlightEnv, "2")
	gate := &connectionGate{used: map[string]int{}}

	first, busy := gate.acquire("codebuddy-intl|conn-a", 2)
	if busy {
		t.Fatal("first attempt refused")
	}
	second, busy := gate.acquire("codebuddy-intl|conn-a", 2)
	if busy {
		t.Fatal("second attempt refused at a ceiling of 2")
	}
	if _, busy := gate.acquire("codebuddy-intl|conn-a", 2); !busy {
		t.Fatal("third attempt admitted over the ceiling")
	}

	// Releasing one slot admits exactly one more, and a double release must not
	// free a slot twice.
	first()
	first()
	if got := gate.inFlight("codebuddy-intl|conn-a"); got != 1 {
		t.Fatalf("in-flight after release = %d, want 1", got)
	}
	if _, busy := gate.acquire("codebuddy-intl|conn-a", 2); busy {
		t.Fatal("attempt refused after a slot was released")
	}
	second()

	// Another account is unaffected.
	if _, busy := gate.acquire("codebuddy-intl|conn-b", 2); busy {
		t.Fatal("a second account shares the first account's ceiling")
	}
}

func TestAccountInFlightLimit_EnvAndProviderScope(t *testing.T) {
	for _, tc := range []struct {
		env      string
		provider string
		want     int
	}{
		{"", "codebuddy-intl", defaultAccountInFlight},
		{"", "cbai", defaultAccountInFlight},
		{"", "codebuddy-cn", defaultAccountInFlight},
		{"2", "codebuddy-intl", 2},
		{"0", "codebuddy-intl", 0},
		{"-1", "codebuddy-intl", defaultAccountInFlight},
		{"nonsense", "codebuddy-intl", defaultAccountInFlight},
		{"3", "openai", 0}, // not a gated lane
		{"3", "qoder", 0},
	} {
		t.Setenv(accountInFlightEnv, tc.env)
		if got := accountInFlightLimit(tc.provider); got != tc.want {
			t.Errorf("env=%q provider=%q: limit = %d, want %d", tc.env, tc.provider, got, tc.want)
		}
	}
}

// Zero means "no ceiling": every attempt is admitted, and the release is a no-op.
func TestConnectionGate_LimitZeroAdmitsEverything(t *testing.T) {
	gate := &connectionGate{used: map[string]int{}}
	for i := 0; i < 50; i++ {
		release, busy := gate.acquire("openai|conn-a", 0)
		if busy {
			t.Fatalf("attempt %d refused at limit 0", i)
		}
		release()
	}
	if got := gate.inFlight("openai|conn-a"); got != 0 {
		t.Errorf("in-flight after releases = %d, want 0", got)
	}
}
