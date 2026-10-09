//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// #222 asked for a cooldown-blocked model to stop looking like a broken one,
// and #221 then changed what a probe is allowed to see: a probe now bypasses
// the cooldown filter so it measures real upstream reachability instead of the
// state the gateway happened to be in.
//
// These cases pin that resulting contract end to end. The risk they guard is
// the one a cooldown classifier introduces: reporting an account-scoped park —
// or a provider's own quota refusal, which never clears by waiting — as
// "blocked", which tells the operator to wait for a reset that cannot come.

type probeVerdict struct {
	OK      bool   `json:"ok"`
	Status  int    `json:"status"`
	Error   string `json:"error"`
	Blocked bool   `json:"blocked"`
	ResetAt string `json:"resetAt"`
}

func runProbe(t *testing.T, env *Env, model, kind string) probeVerdict {
	t.Helper()
	body := map[string]string{"model": model}
	if kind != "" {
		body["kind"] = kind
	}
	res := env.Post(t, "/api/models/test", body)
	if res.Status != http.StatusOK {
		t.Fatalf("probe status = %d, body %s", res.Status, truncate(res.Body))
	}
	var v probeVerdict
	res.Decode(t, &v)
	return v
}

func parkAccount(t *testing.T, env *Env, until time.Time) {
	t.Helper()
	if err := env.Repo.LockConnectionRateLimit("conn-ocz", until, 2, 429, "quota exhausted"); err != nil {
		t.Fatalf("park account: %v", err)
	}
}

// TestModelTestProbesThroughAccountCooldown is the #221 contract the verdict
// must not undo: a parked account is still probed, and the probe reports what
// upstream actually did. Ammering the row here would reintroduce #222's exact
// symptom for every account that happens to be cooling down.
func TestModelTestProbesThroughAccountCooldown(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, chatCompletionResponder())
	addZenConnection(t, env, up)

	parkAccount(t, env, time.Now().UTC().Add(90*time.Minute).Truncate(time.Second))

	v := runProbe(t, env, "ocz/deepseek-v4-pro", "")
	if !v.OK {
		t.Fatalf("a parked account must still be probed and reported honestly, got status=%d error=%q", v.Status, v.Error)
	}
	if v.Blocked {
		t.Error("a probed-through account must not be reported as blocked — the upstream answered")
	}
	if v.ResetAt != "" {
		t.Errorf("a passing probe carries no reset time, got %q", v.ResetAt)
	}
	if up.Count() != 1 {
		t.Errorf("the probe should have reached upstream once, got %d request(s)", up.Count())
	}
}

// TestModelTestMediaProbesThroughAccountCooldown pins the same contract on
// every media kind. Before #221 these lanes reached the selector through
// GetBestConnection, and two of them (image, tts) discarded its error entirely
// in favour of a generic "no active connections" — so the dashboard could not
// distinguish a parked account from a broken credential.
func TestModelTestMediaProbesThroughAccountCooldown(t *testing.T) {
	for _, kind := range []string{"embedding", "image", "tts", "stt", "video", "systemone"} {
		t.Run(kind, func(t *testing.T) {
			env := newEnv(t)
			up := env.NewUpstream(t, JSONResponder(http.StatusOK, `{"data":[{"embedding":[0.1,0.2]}]}`))
			addZenConnection(t, env, up)

			parkAccount(t, env, time.Now().UTC().Add(90*time.Minute).Truncate(time.Second))

			v := runProbe(t, env, "ocz/deepseek-v4-pro", kind)
			if v.Blocked {
				t.Errorf("%s probe must not report blocked when upstream answered, error=%q", kind, v.Error)
			}
			if v.ResetAt != "" {
				t.Errorf("%s probe carries no reset time, got %q", kind, v.ResetAt)
			}
			if up.Count() != 1 {
				t.Errorf("%s probe should have reached upstream once, got %d request(s)", kind, up.Count())
			}
		})
	}
}

// TestModelTestComboModelLockIsBlocked pins the one cooldown verdict that is
// still reachable after #221 made probes bypass account-level cooldown.
//
// The combo lane still consults the production health filter, so a member
// whose model is locked short-circuits to "all connections for this provider
// are rate-limited" without reaching upstream. That is a park with an expiry
// — the lock lifts on its own — so "blocked" is the honest verdict and the
// "Retry blocked" button is the right next action for it.
func TestModelTestComboModelLockIsBlocked(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, chatCompletionResponder())
	addZenConnection(t, env, up)
	if err := env.Repo.LockConnectionModel("conn-ocz", "deepseek-v4-pro", 600, 1); err != nil {
		t.Fatalf("lock model: %v", err)
	}
	env.AddCombo(t, "combo-1", "resilient", []string{"ocz/deepseek-v4-pro"})

	v := runProbe(t, env, "resilient", "")
	if v.OK {
		t.Fatal("a locked combo member must not report as passing")
	}
	if !v.Blocked {
		t.Errorf("a model lock is a park with an expiry, expected blocked, got status=%d error=%q", v.Status, v.Error)
	}
	if up.Count() != 0 {
		t.Errorf("a locked member must not be spent on, upstream saw %d request(s)", up.Count())
	}
}

// TestModelTestProviderQuota429IsNotBlocked is the classifier's real risk. A
// provider quota refusal on a single model is recorded per-model, not
// account-wide, and no amount of waiting fixes it — labelling it "blocked,
// retry when the cooldown lifts" would promise a reset that cannot come.
func TestModelTestProviderQuota429IsNotBlocked(t *testing.T) {
	for _, msg := range []string{
		"rate limit reached for this account",
		"Rate limit reached for deepseek-v4-pro",
		"account is in cooldown at the provider",
		"quota exceeded for this organization",
	} {
		t.Run(msg, func(t *testing.T) {
			env := newEnv(t)
			up := env.NewUpstream(t, JSONResponder(http.StatusTooManyRequests,
				`{"error":{"message":"`+msg+`","type":"rate_limit_error"}}`))
			addZenConnection(t, env, up)

			v := runProbe(t, env, "ocz/deepseek-v4-pro", "")
			if v.OK {
				t.Fatal("a provider refusal must not report as passing")
			}
			if v.Blocked {
				t.Errorf("a provider quota refusal must not be reported as a gateway cooldown: %q", v.Error)
			}
			if v.ResetAt != "" {
				t.Errorf("a provider refusal carries no gateway reset time, got %q", v.ResetAt)
			}
		})
	}
}

// TestModelTestReportsRealProviderFailure pins that the cooldown classifier did
// not turn the probe into a rubber stamp: an account that is free and a
// provider that refuses the model must still come back as a plain failure.
func TestModelTestReportsRealProviderFailure(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, JSONResponder(http.StatusBadRequest,
		`{"error":{"message":"model deepseek-v4-pro is not supported","type":"invalid_request_error"}}`))
	addZenConnection(t, env, up)

	v := runProbe(t, env, "ocz/deepseek-v4-pro", "")
	if v.OK {
		t.Fatal("a provider refusal must not report as passing")
	}
	if v.Blocked {
		t.Errorf("a provider refusal must not be reported as a cooldown: %q", v.Error)
	}
	if got := up.Last(t).Model(t); got != "deepseek-v4-pro" {
		t.Errorf("the provider should have been asked, got model %q", got)
	}
}

// TestModelTestVerdictWireShape pins the shape the SPA reads: `blocked` and
// `resetAt` are top-level fields on the verdict, and an absent resetAt is
// omitted rather than sent as an empty string the client would have to
// distinguish from a real one.
func TestModelTestVerdictWireShape(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, JSONResponder(http.StatusTooManyRequests,
		`{"error":{"message":"quota exceeded","type":"rate_limit_error"}}`))
	addZenConnection(t, env, up)

	res := env.Post(t, "/api/models/test", map[string]string{"model": "ocz/deepseek-v4-pro"})
	var raw map[string]any
	if err := json.Unmarshal([]byte(res.Body), &raw); err != nil {
		t.Fatalf("decode: %v (body %s)", err, truncate(res.Body))
	}
	if _, ok := raw["ok"]; !ok {
		t.Error("ok must always be present")
	}
	if _, ok := raw["blocked"]; ok && raw["blocked"] == true {
		t.Error("a provider refusal must never serialize blocked: true")
	}
	if _, ok := raw["resetAt"]; ok {
		t.Errorf("resetAt must be omitted when there is no reset, got %#v", raw["resetAt"])
	}
}
