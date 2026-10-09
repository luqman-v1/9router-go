//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// Issue #222 asked for a cooldown-blocked model to stop looking like a broken
// one. The verdict reaches the dashboard through a probe that never touches
// the provider at all, so the only honest place to decide it is the gateway:
// it is the one that knows the account is parked and when it frees up.
//
// These cases drive the production router with a genuinely locked account — no
// synthetic error strings — and assert what the SPA receives.

func probeVerdict(t *testing.T, env *Env, kind string) struct {
	OK      bool   `json:"ok"`
	Status  int    `json:"status"`
	Error   string `json:"error"`
	Blocked bool   `json:"blocked"`
	ResetAt string `json:"resetAt"`
} {
	t.Helper()
	body := map[string]string{"model": "ocz/deepseek-v4-pro"}
	if kind != "" {
		body["kind"] = kind
	}
	res := env.Post(t, "/api/models/test", body)
	if res.Status != http.StatusOK {
		t.Fatalf("probe status = %d, body %s", res.Status, truncate(res.Body))
	}
	var verdict struct {
		OK      bool   `json:"ok"`
		Status  int    `json:"status"`
		Error   string `json:"error"`
		Blocked bool   `json:"blocked"`
		ResetAt string `json:"resetAt"`
	}
	res.Decode(t, &verdict)
	return verdict
}

func lockAccount(t *testing.T, env *Env, until time.Time) {
	t.Helper()
	if err := env.Repo.LockConnectionRateLimit("conn-ocz", until, 2, 429, "quota exhausted"); err != nil {
		t.Fatalf("lock account: %v", err)
	}
}

// TestModelTestBlockedWhileAccountInCooldown is the #222 contract on the chat
// lane: a parked account must report as blocked with the reset time attached,
// never as a failed model.
func TestModelTestBlockedWhileAccountInCooldown(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, chatCompletionResponder())
	addZenConnection(t, env, up)

	until := time.Now().UTC().Add(90 * time.Minute).Truncate(time.Second)
	lockAccount(t, env, until)

	verdict := probeVerdict(t, env, "")
	if verdict.OK {
		t.Fatal("a parked account must not report the model as passing")
	}
	if !verdict.Blocked {
		t.Fatalf("probe must report blocked while the account is parked, got status=%d error=%q", verdict.Status, verdict.Error)
	}
	if verdict.ResetAt != until.UTC().Format(time.RFC3339) {
		t.Errorf("resetAt = %q, want %q", verdict.ResetAt, until.UTC().Format(time.RFC3339))
	}
	if n := up.Count(); n != 0 {
		t.Errorf("a parked account must not be spent on a probe, upstream saw %d request(s)", n)
	}
}

// TestModelTestBlockedOnMediaLanes is the same contract on the media lanes.
// Before the fix the selector error reached these probes as a 404, and two of
// them replaced it with a generic "no active connections" that carried no reset
// time at all — so every embedding, image, tts, stt and video model was painted
// red while the account was merely cooling down.
func TestModelTestBlockedOnMediaLanes(t *testing.T) {
	for _, kind := range []string{"embedding", "image", "tts", "stt", "video", "systemone"} {
		t.Run(kind, func(t *testing.T) {
			env := newEnv(t)
			up := env.NewUpstream(t, chatCompletionResponder())
			addZenConnection(t, env, up)

			until := time.Now().UTC().Add(90 * time.Minute).Truncate(time.Second)
			lockAccount(t, env, until)

			verdict := probeVerdict(t, env, kind)
			if verdict.OK {
				t.Fatal("a parked account must not report the model as passing")
			}
			if !verdict.Blocked {
				t.Fatalf("%s probe must report blocked while the account is parked, got status=%d error=%q",
					kind, verdict.Status, verdict.Error)
			}
			if verdict.ResetAt != until.UTC().Format(time.RFC3339) {
				t.Errorf("%s resetAt = %q, want %q", kind, verdict.ResetAt, until.UTC().Format(time.RFC3339))
			}
		})
	}
}

// TestModelTestNotBlockedOnceCooldownExpires is the other half: a park that has
// run out must stop looking like a park, or the dashboard would amber a model
// forever and offer a retry that can only fail.
func TestModelTestNotBlockedOnceCooldownExpires(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, chatCompletionResponder())
	addZenConnection(t, env, up)

	lockAccount(t, env, time.Now().UTC().Add(-time.Minute).Truncate(time.Second))

	verdict := probeVerdict(t, env, "")
	if !verdict.OK {
		t.Fatalf("an expired cooldown must not block the probe, got blocked=%v status=%d error=%q",
			verdict.Blocked, verdict.Status, verdict.Error)
	}
	if verdict.Blocked {
		t.Error("an expired cooldown must not be reported as blocked")
	}
}

// TestModelTestReportsRealFailureOnceCooldownLifts proves the fix did not turn
// the probe into a rubber stamp: with the account free again and the provider
// refusing the model, the verdict must be a plain failure with no reset time.
func TestModelTestReportsRealFailureOnceCooldownLifts(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, JSONResponder(http.StatusBadRequest,
		`{"error":{"message":"model deepseek-v4-pro is not supported","type":"invalid_request_error"}}`))
	addZenConnection(t, env, up)

	lockAccount(t, env, time.Now().UTC().Add(-time.Minute).Truncate(time.Second))

	verdict := probeVerdict(t, env, "")
	if verdict.OK {
		t.Fatal("a provider refusal must not report as passing")
	}
	if verdict.Blocked {
		t.Errorf("a provider refusal must not be reported as a cooldown: %q", verdict.Error)
	}
	if verdict.ResetAt != "" {
		t.Errorf("a real failure must carry no reset time, got %q", verdict.ResetAt)
	}
	if got := up.Last(t).Model(t); got != "deepseek-v4-pro" {
		t.Errorf("the provider should have been asked, got model %q", got)
	}
}

// TestModelTestBlockedPayloadReachesTheClientAsJSON pins the wire shape the SPA
// reads: blocked and resetAt are top-level fields on the verdict, not nested in
// an error envelope the client would have to parse.
func TestModelTestBlockedPayloadReachesTheClientAsJSON(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, chatCompletionResponder())
	addZenConnection(t, env, up)
	lockAccount(t, env, time.Now().UTC().Add(45*time.Minute).Truncate(time.Second))

	res := env.Post(t, "/api/models/test", map[string]string{"model": "ocz/deepseek-v4-pro"})
	var raw map[string]any
	if err := json.Unmarshal([]byte(res.Body), &raw); err != nil {
		t.Fatalf("decode: %v (body %s)", err, truncate(res.Body))
	}
	if raw["blocked"] != true {
		t.Errorf("blocked must serialize as a top-level true, got %#v", raw["blocked"])
	}
	if _, ok := raw["resetAt"].(string); !ok {
		t.Errorf("resetAt must be a top-level string, got %#v", raw["resetAt"])
	}
}
