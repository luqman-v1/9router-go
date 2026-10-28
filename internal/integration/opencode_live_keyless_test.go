//go:build integration

package integration

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/proxy"
	"9router/proxy/internal/translator"
)

// A keyless live call to the OpenCode free tier.
//
// Unlike every other test in this package, this one leaves the process. It
// exists because the simulation beside it proves only that the gateway shapes a
// request to my *description* of the gate — it cannot prove the real gate
// agrees. The only way to know that is to ask upstream, keylessly, with the
// public credential the free tier accepts.
//
// Two consequences:
//
//   - It is opt-in via 9ROUTER_LIVE_OPENCODE=1. CI never sets it, so a run
//     without the variable skips instead of reaching the network.
//   - A refusal from the provider is not this gateway's defect. The free tier
//     is shared and rate limited per IP, so 403/429/5xx skip with the body
//     rather than fail. Only a body the gate accepts but that produces a
//     malformed answer is a real failure.
//
// Run it with:
//
//	9ROUTER_LIVE_OPENCODE=1 go test -tags=integration ./internal/integration/ \
//	    -run TestLiveOpenCode -v
const liveOpenCodeEnv = "9ROUTER_LIVE_OPENCODE"

func liveOpenCodeEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(liveOpenCodeEnv))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// liveOpenCodeEndpoint is the keyless inference gate for chat completions.
const liveOpenCodeEndpoint = "https://opencode.ai/inference/v1/chat/completions"

// liveRequest builds the request the gateway would emit for a free model: the
// 2.x CLI identity, the mirrored session headers, the public credential, and no
// x-opencode-request (which the gate rejects).
func liveRequest(t *testing.T, model string, tools []map[string]any) *http.Request {
	t.Helper()
	headers := proxy.BuildOpenCodeHeaders(nil, "", true)
	body := map[string]any{
		"model":  model,
		"stream": true,
		"messages": []map[string]any{
			{"role": "user", "content": "Reply with the single word: ok"},
		},
	}
	if tools != nil {
		body["tools"] = tools
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	// Shape with the production shaper so the live call exercises the same code
	// path a real free-tier request takes, rather than a hand-written body that
	// could drift from it.
	shaped, _ := translator.ConcealOpenCodeV2FingerprintTools(raw, false, false)
	req, err := http.NewRequest(http.MethodPost, liveOpenCodeEndpoint, strings.NewReader(string(shaped)))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req
}

// liveStatus is a classified outcome of one live attempt.
type liveStatus struct {
	code int
	body string
}

// liveVerdict classifies a live response by what it *contains*, not by its
// status line.
//
// This matters more than it sounds: the gate answers some refusals inside an
// SSE body that still carries HTTP 200, so a check on StatusCode alone reports
// a closed tier as healthy. During the investigation that produced this file,
// twenty "200 OK" responses were in fact 175-byte FreeTierError frames. Any
// assertion here has to read the payload.
type liveVerdict int

const (
	liveOK liveVerdict = iota
	liveGateRefused
	liveOtherError
)

func classifyLive(res liveStatus) (liveVerdict, string) {
	trimmed := strings.TrimSpace(res.body)
	if strings.Contains(trimmed, "FreeTierError") {
		return liveGateRefused, "FreeTierError in body"
	}
	// A deprecation or unavailability reply is a fact about the model, not a
	// gate refusal, but neither is a success.
	if res.code != http.StatusOK {
		return liveOtherError, "non-200"
	}
	// Real traffic must carry completion frames; an empty or non-SSE 200 is a
	// truncated or proxied failure, not an answer.
	if !strings.Contains(trimmed, "data:") || strings.Contains(trimmed, "keep-alive") {
		return liveOtherError, "200 without completion frames"
	}
	return liveOK, ""
}

// doLive performs one call and returns its status and body.
func doLive(t *testing.T, req *http.Request) liveStatus {
	t.Helper()
	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("live call failed to complete: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		t.Fatalf("read live response: %v", err)
	}
	return liveStatus{code: resp.StatusCode, body: string(body)}
}

// TestLiveOpenCode_FreeTierGate reports whether the real free-tier gate
// currently serves a genuine free-tier id.
//
// It deliberately does not fail when the gate refuses. A closed or rotating free
// tier is a fact about upstream, not a defect in this gateway, and a test that
// went red every time the tier closed would be turned off within a week. What
// it must never do is call a refusal a success — hence classifyLive, which reads
// the payload rather than the status line.
func TestLiveOpenCode_FreeTierGate(t *testing.T) {
	if !liveOpenCodeEnabled() {
		t.Skipf("live OpenCode call: set %s=1 to run it against the real gate", liveOpenCodeEnv)
	}

	// A client that declared its own capitalised tools, like Claude Code does;
	// the shaper replaces them with the exact CLI fingerprint.
	res := doLive(t, liveRequest(t, "deepseek-v4-flash-free", claudeCodeToolset()))

	t.Logf("POST %s", liveOpenCodeEndpoint)
	t.Logf("status: %d", res.code)
	t.Logf("body: %s", truncate([]byte(res.body)))

	verdict, why := classifyLive(res)
	switch verdict {
	case liveOK:
		t.Logf("RESULT: the free-tier id is served on the keyless inference gate")
	case liveGateRefused:
		t.Logf("RESULT: the gate refused the free-tier id (%s). Free-tier access is not "+
			"available from this egress right now; the gateway cannot shape its way past a "+
			"closed tier.", why)
		if res.code == http.StatusUnauthorized {
			t.Skipf("public credential rejected: %s", truncate([]byte(res.body)))
		}
	case liveOtherError:
		switch {
		case res.code == http.StatusUnauthorized:
			t.Skipf("public credential rejected: %s", truncate([]byte(res.body)))
		case res.code == http.StatusTooManyRequests, res.code == http.StatusBadGateway,
			res.code == http.StatusServiceUnavailable, res.code == http.StatusGatewayTimeout:
			t.Skipf("free tier unavailable right now (%d): %s", res.code, truncate([]byte(res.body)))
		default:
			t.Errorf("%s (status %d): %s", why, res.code, truncate([]byte(res.body)))
		}
	}
}

// TestLiveOpenCode_FreeTierGate_Control_WithoutFingerprint is the control for the
// call above: the same model with no client tools declared. Comparing the two
// shows whether the shaping step is what the gate rewards on this lane.
func TestLiveOpenCode_FreeTierGate_Control_WithoutFingerprint(t *testing.T) {
	if !liveOpenCodeEnabled() {
		t.Skipf("live OpenCode call: set %s=1 to run it against the real gate", liveOpenCodeEnv)
	}

	res := doLive(t, liveRequest(t, "deepseek-v4-flash-free", nil))
	t.Logf("control (no client tools) status: %d", res.code)
	t.Logf("control (no client tools) body: %s", truncate([]byte(res.body)))

	switch verdict, why := classifyLive(res); verdict {
	case liveOK:
		t.Logf("the gate accepts a body with no client tools, so the tool fingerprint is not what earns the 200")
	case liveGateRefused:
		t.Logf("the gate refuses a body with no client tools (%s); the fingerprint may be load-bearing", why)
	case liveOtherError:
		t.Logf("control inconclusive: %s (status %d)", why, res.code)
	}
}
