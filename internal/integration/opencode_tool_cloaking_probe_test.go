//go:build integration

package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"

	"9router/proxy/internal/proxy"
)

// The tool-cloaking probe: does the free-tier gate accept caller tools declared
// natively under an opaque name, or only the execute/CALL_TOOL prompt bridge?
//
// This is a measurement, not an assertion, because the two reference
// implementations measured opposite results and neither could be reproduced
// from the egress available when this was written:
//
//   - `9router-go` recorded foreign tool names answering 400 "Request validation
//     failed" (2026-10-25, see translator/opencode_free_harness.go);
//   - `opencode2api` measured `mcp_<12-hex>` + the official tool set answering
//     200 (2026-11-16, its #182).
//
// Measured 2026-11-18: every body past structural validation answered 429
// FreeUsageLimitError, including the tool-less control, because the probe
// egress's free allowance was spent. Body validation runs *after* the quota
// check — a missing `model` returns 400 first, but a foreign tool name returns
// the same 429 as no tools at all — so tool-strategy differences are invisible
// from a refused egress. The question is therefore still open.
//
// Run this from a clean egress:
//
//	9ROUTER_LIVE_OPENCODE=1 go test -tags=integration ./internal/integration/ \
//	    -run TestLiveOpenCode_ToolCloakingProbe -v
//
// Switch the shipped strategy only on a `200` verdict for `mcp_12hex` paired
// with a non-200 for `raw`, then update the harness comment with the date and
// the model it was measured on.
const probeCallerTool = `{"type":"function","function":{"name":"Bash","description":"Run a shell command","parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}}`

// mcpToolName is the opaque declaration name the reference uses:
// "mcp_" + 12 lowercase hex of the tool name's SHA-256.
func mcpToolName(name string) string {
	sum := sha256.Sum256([]byte(name))
	return "mcp_" + hex.EncodeToString(sum[:])[:12]
}

// toolProbeVerdict is what one probe attempt actually told us. It is deliberately
// narrower than liveVerdict: "the gate answered something" is not a result, and
// collapsing a 429 quota wall into "accepted" is the mistake this type exists to
// prevent.
type toolProbeVerdict string

const (
	toolProbeAccepted  toolProbeVerdict = "accepted"   // a real 200 with completion frames
	toolProbeQuotaWall toolProbeVerdict = "quota-wall" // 429 FreeUsageLimitError: says nothing about the body
	toolProbeRefused   toolProbeVerdict = "refused"    // a FreeTierError judgement on this body
	toolProbeInvalid   toolProbeVerdict = "invalid"    // 400 or other: the request itself was rejected
	toolProbeTransport toolProbeVerdict = "transport"  // 5xx / unparseable
)

func classifyProbe(t *testing.T, res liveStatus) toolProbeVerdict {
	t.Helper()
	switch {
	case strings.Contains(res.body, "FreeUsageLimitError"):
		// The quota check ran before whatever we were trying to measure.
		return toolProbeQuotaWall
	case strings.Contains(res.body, "FreeTierError"):
		return toolProbeRefused
	case res.code == http.StatusOK:
		if verdict, _ := classifyLive(res); verdict == liveOK {
			return toolProbeAccepted
		}
		return toolProbeTransport
	case res.code == http.StatusBadRequest:
		return toolProbeInvalid
	default:
		return toolProbeTransport
	}
}

// probeBodies builds the candidate tool declarations. Each body varies only the
// declarations, so a difference in the verdict is attributable to them.
func probeBodies(t *testing.T, model string) map[string]string {
	t.Helper()
	base := `{"model":"` + model + `","stream":true,"messages":[{"role":"user","content":"Reply with the single word: ok"}]`
	mcp := strings.ReplaceAll(probeCallerTool, `"name":"Bash"`, `"name":"`+mcpToolName("Bash")+`"`)
	return map[string]string{
		"no_tools":  base + `}`,
		"raw":       base + `,"tools":[` + probeCallerTool + `]}`,
		"mcp_12hex": base + `,"tools":[` + mcp + `]}`,
	}
}

func TestLiveOpenCode_ToolCloakingProbe(t *testing.T) {
	if !liveOpenCodeEnabled() {
		t.Skipf("live OpenCode call: set %s=1 to run it against the real gate", liveOpenCodeEnv)
	}

	models := []string{"glm-5.3-flash-free", "deepseek-v4-flash-free"}
	order := []string{"no_tools", "raw", "mcp_12hex"}

	type row struct {
		model   string
		verdict toolProbeVerdict
	}
	seen := make(map[string]row, len(models)*len(order))

	for _, model := range models {
		bodies := probeBodies(t, model)
		for _, name := range order {
			res := doLive(t, liveRawRequest(t, bodies[name]))
			verdict := classifyProbe(t, res)
			seen[model+"/"+name] = row{model: model, verdict: verdict}
			t.Logf("model=%-24s body=%-10s status=%d verdict=%s", model, name, res.code, verdict)
			t.Logf("  body: %s", truncate([]byte(res.body)))
		}
	}

	// Judge per model: a quota wall on one model must not mask a real answer on
	// another, and the tool-less control is the baseline each comparison needs.
	decided := false
	for _, model := range models {
		control := seen[model+"/no_tools"].verdict
		raw := seen[model+"/raw"].verdict
		mcp := seen[model+"/mcp_12hex"].verdict

		if control != toolProbeAccepted {
			t.Logf("%s: inconclusive — the tool-less control did not answer 200 (%s), "+
				"so nothing about tool declarations can be read from this model", model, control)
			continue
		}
		switch {
		case mcp == toolProbeAccepted && raw != toolProbeAccepted:
			decided = true
			t.Logf("%s: DECIDED — mcp_<digest> is accepted while the raw name is not. "+
				"The reference strategy is available; see OPENCODE2API_AUDIT.md §2.4.", model)
		case mcp == toolProbeAccepted && raw == toolProbeAccepted:
			decided = true
			t.Logf("%s: DECIDED — both accepted. Tool declarations are unconstrained on "+
				"this gate; no cloaking strategy is required and the CALL_TOOL bridge can go.", model)
		case mcp != toolProbeAccepted && raw != toolProbeAccepted:
			decided = true
			t.Logf("%s: DECIDED — both refused (%s / %s). The existing CALL_TOOL bridge "+
				"must stay; the mcp_ route is not available on this gate.", model, mcp, raw)
		default:
			t.Logf("%s: inconclusive — mcp=%s raw=%s", model, mcp, raw)
		}
	}
	if !decided {
		t.Logf("RESULT: inconclusive from this egress. Re-run from a clean one before " +
			"changing the shipped strategy; do not switch on a guess.")
	}
}

// liveRawRequest posts a hand-built body with the production free-tier headers.
//
// The probe must not run the body through the v2 shaper: the question is what
// the gate does with specific tool declarations, and the shaper would replace
// them before they were ever sent.
func liveRawRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, liveOpenCodeEndpoint, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	for k, v := range proxy.BuildOpenCodeHeaders(nil, "", true) {
		req.Header.Set(k, v)
	}
	return req
}
