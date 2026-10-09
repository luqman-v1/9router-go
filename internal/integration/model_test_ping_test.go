//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/db"
)

// The dashboard's Test button pings a model through the endpoint its kind
// serves. That is the whole fix: a System One model was probed on
// /v1/chat/completions, upstream answered 500, and the button reported a broken
// model while the Run button beside it — posting to /v1/systemone — worked.
// These cases pin the lane per kind, against the production router.

// systemoneReply is a well-formed System One answer: the probe only passes when
// `answers` is a non-empty object.
const systemoneReply = `{"model":"jev-1.13","answers":{"probe":{"type":"noul","noul":0.91}}}`

// TestModelTestSystemoneProbeSkipsTheChatLane pins the reported bug: with a
// System One kind the probe must reach the provider's systemone endpoint, and
// the verdict comes from the answers it carries.
func TestModelTestSystemoneProbeSkipsTheChatLane(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, JSONResponder(http.StatusOK, systemoneReply))
	addZenConnection(t, env, up)

	res := env.Post(t, "/api/models/test", map[string]string{
		"model": "ocz/jev-1.13",
		"kind":  "systemone",
	})
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, body %s", res.Status, truncate(res.Body))
	}

	var verdict struct {
		OK        bool   `json:"ok"`
		Error     string `json:"error"`
		LatencyMs int64  `json:"latencyMs"`
	}
	res.Decode(t, &verdict)
	if !verdict.OK {
		t.Fatalf("systemone probe failed: %s", verdict.Error)
	}

	last := up.Last(t)
	if last.Path != "/zen/v1/systemone" {
		t.Errorf("upstream path = %q, want /zen/v1/systemone", last.Path)
	}
	if last.Model(t) != "jev-1.13" {
		t.Errorf("upstream model = %q, want jev-1.13", last.Model(t))
	}
}

// TestModelTestSystemoneProbeReportsUpstreamChatFailure pins that a model the
// chat lane cannot serve is reported as a failure of THAT lane — never as a
// silent pass — so the verdict stays honest when the wrong endpoint is reached.
func TestModelTestSystemoneProbeReportsUpstreamChatFailure(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, JSONResponder(http.StatusInternalServerError,
		`{"type":"error","error":{"type":"error","message":"Internal server error"}}`))
	addZenConnection(t, env, up)

	res := env.Post(t, "/api/models/test", map[string]string{
		"model": "ocz/jev-1.13",
		"kind":  "systemone",
	})

	var verdict struct {
		OK     bool   `json:"ok"`
		Status int    `json:"status"`
		Error  string `json:"error"`
	}
	res.Decode(t, &verdict)
	if verdict.OK {
		t.Fatal("probe reported ok for an upstream 500")
	}
	if verdict.Status != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", verdict.Status)
	}
	if !strings.Contains(verdict.Error, "Internal server error") {
		t.Errorf("error = %q, want the upstream message", verdict.Error)
	}
}

// TestModelTestDefaultsToTheChatLane keeps the LLM probe on the lane it always
// used: the dashboard's provider page passes no kind, and that page's models are
// chat models.
func TestModelTestDefaultsToTheChatLane(t *testing.T) {
	env, up := newProviderEnv(t)

	res := env.Post(t, "/api/models/test", map[string]string{"model": "ds/deepseek-chat"})
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, body %s", res.Status, truncate(res.Body))
	}

	var verdict struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	res.Decode(t, &verdict)
	if !verdict.OK {
		t.Fatalf("chat probe failed: %s", verdict.Error)
	}

	last := up.Last(t)
	if !strings.HasSuffix(last.Path, "/chat/completions") {
		t.Errorf("upstream path = %q, want the chat completions lane", last.Path)
	}
	if !strings.Contains(string(last.Body), `"max_tokens":1024`) {
		t.Errorf("probe body = %s, want the 1024-token reasoning budget", string(last.Body))
	}
}

// TestModelTestRejectsAnUnknownKind pins the contract the other media kinds rely
// on: an endpoint with no probe behind it is refused, not silently answered by
// the chat lane that would report a false failure.
func TestModelTestRejectsAnUnknownKind(t *testing.T) {
	env := newEnv(t)

	res := env.Post(t, "/api/models/test", map[string]string{
		"model": "ocz/jev-1.13",
		"kind":  "imageToText",
	})
	if res.Status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", res.Status, truncate(res.Body))
	}
}

// TestModelTestReachesUpstreamEvenWhenConnectionIsPreCooled asserts that
// calling /api/models/test can still probe a pre-cooled connection so operators
// can verify whether an upstream model has recovered.
func TestModelTestReachesUpstreamEvenWhenConnectionIsPreCooled(t *testing.T) {
	env, up := newProviderEnv(t)

	// Pre-cool the connection
	until := time.Now().UTC().Add(10 * time.Minute)
	if err := env.Repo.LockConnectionRateLimit("conn-deepseek", until, 1, http.StatusTooManyRequests, "rate limited"); err != nil {
		t.Fatalf("pre-cooling connection: %v", err)
	}

	res := env.Post(t, "/api/models/test", map[string]string{"model": "ds/deepseek-chat"})
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, body %s", res.Status, truncate(res.Body))
	}

	var verdict struct {
		OK bool `json:"ok"`
	}
	res.Decode(t, &verdict)
	if !verdict.OK {
		t.Fatal("probe should reach upstream even when connection is pre-cooled")
	}

	if up.Count() != 1 {
		t.Errorf("upstream count = %d, want 1", up.Count())
	}
}

// TestModelTestFailingProbeDoesNotMutateCooldownState ensures that a failing
// test probe is read-only w.r.t. production cooldowns and strike counters.
func TestModelTestFailingProbeDoesNotMutateCooldownState(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, JSONResponder(http.StatusTooManyRequests, `{"error":{"message":"Rate limit exceeded"}}`))
	env.AddConnection(t, "conn-fail", "deepseek", "DeepSeek Test", up, "sk-test")

	res := env.Post(t, "/api/models/test", map[string]string{"model": "ds/deepseek-chat"})
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, body %s", res.Status, truncate(res.Body))
	}

	var verdict struct {
		OK bool `json:"ok"`
	}
	res.Decode(t, &verdict)
	if verdict.OK {
		t.Fatal("expected probe to fail on upstream 429")
	}

	// Verify DB cooldown state is untouched
	conn, err := env.Repo.GetProviderConnectionByID("conn-fail")
	if err != nil {
		t.Fatalf("get connection: %v", err)
	}
	if until, ok := db.ConnectionCooldownUntil(conn.Data); ok {
		t.Errorf("rateLimitedUntil should not be set by probe, got: %v", until)
	}
	if locked, _ := env.Repo.IsConnectionModelLocked("conn-fail", "deepseek-chat"); locked {
		t.Error("modelLock should not be set by probe")
	}
	if level := env.Repo.GetConnectionBackoffLevel("conn-fail"); level != 0 {
		t.Errorf("backoffLevel = %d, want 0", level)
	}
}

// TestModelTestPassingProbeDoesNotEraseProductionCooldownState ensures that
// a successful probe does not reset production backoffLevel or clear rateLimitedUntil.
func TestModelTestPassingProbeDoesNotEraseProductionCooldownState(t *testing.T) {
	env, _ := newProviderEnv(t)

	// Pre-cool connection with specific backoffLevel
	until := time.Now().UTC().Add(time.Hour)
	if err := env.Repo.LockConnectionRateLimit("conn-deepseek", until, 3, http.StatusTooManyRequests, "quota exhausted"); err != nil {
		t.Fatalf("lock connection: %v", err)
	}

	res := env.Post(t, "/api/models/test", map[string]string{"model": "ds/deepseek-chat"})
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, body %s", res.Status, truncate(res.Body))
	}

	var verdict struct {
		OK bool `json:"ok"`
	}
	res.Decode(t, &verdict)
	if !verdict.OK {
		t.Fatal("expected probe to pass")
	}

	// Verify cooldown and backoff were NOT cleared by probe success
	conn, err := env.Repo.GetProviderConnectionByID("conn-deepseek")
	if err != nil {
		t.Fatalf("get connection: %v", err)
	}
	gotUntil, ok := db.ConnectionCooldownUntil(conn.Data)
	if !ok || gotUntil.Before(time.Now()) {
		t.Error("probe success should not clear production rateLimitedUntil")
	}
	if level := env.Repo.GetConnectionBackoffLevel("conn-deepseek"); level != 3 {
		t.Errorf("backoffLevel = %d, want 3 (probe should not reset backoffLevel)", level)
	}
}

// TestModelTest410DoesNotRecordDeprecationOr24HourLock asserts that a 410 probe
// failure does not record a permanent model deprecation or write a 24h model lock.
func TestModelTest410DoesNotRecordDeprecationOr24HourLock(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, JSONResponder(http.StatusGone, `{"error":{"type":"ModelDeprecated","message":"model retired"}}`))
	env.AddConnection(t, "conn-410", "deepseek", "DeepSeek 410", up, "sk-410")

	res := env.Post(t, "/api/models/test", map[string]string{"model": "ds/deepseek-chat"})
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, body %s", res.Status, truncate(res.Body))
	}

	var verdict struct {
		OK     bool `json:"ok"`
		Status int  `json:"status"`
	}
	res.Decode(t, &verdict)
	if verdict.OK || verdict.Status != http.StatusGone {
		t.Errorf("verdict = %+v, want status 410 and not OK", verdict)
	}

	// Verify no modelLock written on conn-410
	if locked, _ := env.Repo.IsConnectionModelLocked("conn-410", "deepseek-chat"); locked {
		t.Error("probe 410 should not write modelLock")
	}

	// Verify no deprecation row in db
	deps, err := env.Repo.ListModelDeprecations("deepseek")
	if err != nil {
		t.Fatalf("get deprecations: %v", err)
	}
	if len(deps) > 0 {
		t.Errorf("expected 0 deprecation rows, got %d", len(deps))
	}
}