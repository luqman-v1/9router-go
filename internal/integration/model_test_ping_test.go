//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"
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