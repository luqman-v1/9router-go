//go:build integration

package integration

import (
	"context"
	json "encoding/json/v2"
	"strings"
	"testing"

	"9router/proxy/internal/analyticsrange"
	"9router/proxy/internal/headroom"
)

// jsonMarshal encodes a request body the same way Env.Post does, so the size
// sent can be compared with the size the upstream received.
func jsonMarshal(v any) ([]byte, error) {
	return json.Marshal(v, json.Deterministic(true))
}

// enableHeadroomBuiltin turns in-flight compression on with the in-process
// engine, which is what a fresh install gets when an operator flips the switch
// without installing headroom-ai.
//
// It goes through PUT /api/settings, the same route the dashboard uses, rather
// than a repository call: the point of the smoke test is that the operator's
// actual gesture ends up compressing the request.
func enableHeadroomBuiltin(t *testing.T, env *Env) {
	t.Helper()
	res := env.Put(t, "/api/settings", map[string]any{
		"headroomEnabled": true,
		"headroomEngine":  "builtin",
	})
	if res.Status != 200 {
		t.Fatalf("enable headroom builtin: status %d: %s", res.Status, truncate(res.Body))
	}
	var settings struct {
		HeadroomEnabled bool   `json:"headroomEnabled"`
		HeadroomEngine  string `json:"headroomEngine"`
	}
	res.Decode(t, &settings)
	if !settings.HeadroomEnabled || settings.HeadroomEngine != "builtin" {
		t.Fatalf("settings did not persist: enabled=%v engine=%q", settings.HeadroomEnabled, settings.HeadroomEngine)
	}
}

// assertCompressionRecorded proves the usage page can see the compression the
// request just received.
//
// The analytics row is written only when SavedTokens > 0, so this is the check a
// stage that rewrites the body without reporting its saving cannot pass:
// headroom-only compression rewrote the body and dropped the token counts, which
// left the Usage page reporting no compression at all.
//
// The mode is not pinned here. Which engine saves the tokens depends on what the
// body is made of — RTK folds tool output before headroom sees it, so the same
// fixture records "stacked" or "rtk" depending on the saver defaults the env was
// booted with. What must hold either way is that a compressed request leaves a
// row, attributed to a named mode, with the saving it made.
func assertCompressionRecorded(t *testing.T, env *Env) {
	t.Helper()
	summary, err := env.Repo.GetCompressionAnalyticsSummary(context.Background(), analyticsrange.Window{})
	if err != nil {
		t.Fatalf("read compression summary: %v", err)
	}
	if summary.TotalRequests == 0 {
		t.Fatalf("no compression analytics row was recorded for a compressed request")
	}
	if summary.TotalTokensSaved <= 0 {
		t.Fatalf("compression rows recorded but total tokens saved = 0")
	}
	for mode, stats := range summary.ByMode {
		if stats.Count <= 0 || stats.TokensSaved <= 0 {
			t.Fatalf("mode %q recorded count=%d tokensSaved=%d, want both > 0", mode, stats.Count, stats.TokensSaved)
		}
	}
}

// TestHeadroomBuiltinCompressesOutboundRequest is the end-to-end proof that the
// built-in engine reaches a real outbound request: the gateway boots the real
// router, the switch is on, the in-process engine is selected, and the body the
// fake upstream receives is smaller than the one the client sent.
func TestHeadroomBuiltinCompressesOutboundRequest(t *testing.T) {
	env, upstream := newProviderEnv(t)
	enableHeadroomBuiltin(t, env)

	// A tool result that the in-process pipeline folds by line deduplication.
	// The tool name must be one the compressor is cleared for, so the guard
	// does not skip it before the engine runs.
	filler := strings.Repeat("2024-01-01T00:00:00Z INFO build step completed ok\n", 200)
	body := map[string]any{
		"model": "deepseek-chat",
		"messages": []map[string]any{
			{"role": "user", "content": "run the build"},
			{"role": "assistant", "content": "", "tool_calls": []map[string]any{
				{"id": "call_1", "type": "function", "function": map[string]any{"name": "bash", "arguments": `{"command":"go build ./..."}`}},
			}},
			{"role": "tool", "tool_call_id": "call_1", "content": filler},
		},
	}
	sent, err := jsonMarshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}

	res := env.Post(t, "/v1/chat/completions", body)
	if res.Status != 200 {
		t.Fatalf("expected 200, got %d: %s", res.Status, truncate(res.Body))
	}

	last := upstream.Last(t)
	if got := last.Model(t); got != "deepseek-chat" {
		t.Errorf("upstream received model %q, want deepseek-chat", got)
	}
	if len(last.Body) >= len(sent) {
		t.Fatalf("built-in engine did not shrink the outbound body: sent %d bytes, upstream got %d",
			len(sent), len(last.Body))
	}
	// The join keys the provider depends on must survive the fold.
	for _, want := range []string{"call_1", "deepseek-chat", "go build ./..."} {
		if !strings.Contains(string(last.Body), want) {
			t.Errorf("compressed outbound body lost %q: %s", want, truncate(last.Body))
		}
	}
	// The accounting must agree with the body: a request that was compressed
	// but recorded no saving is invisible on the Usage page, because the row is
	// only written when SavedTokens > 0. Headroom-only compression did exactly
	// that — the stage rewrote the body and dropped the token counts.
	assertCompressionRecorded(t, env)
	t.Logf("built-in headroom: client sent %d bytes, upstream received %d bytes (%.1f%% smaller)",
		len(sent), len(last.Body), 100*float64(len(sent)-len(last.Body))/float64(len(sent)))
}

// TestHeadroomBuiltinLeavesProtectedToolAlone is the negative control for the
// same path: a tool the compressor was never cleared for must reach upstream
// byte for byte, so the switch cannot cost correctness for a saving.
func TestHeadroomBuiltinLeavesProtectedToolAlone(t *testing.T) {
	env, upstream := newProviderEnv(t)
	enableHeadroomBuiltin(t, env)

	filler := strings.Repeat("func Foo() { println(\"source line padding\") }\n", 200)
	body := map[string]any{
		"model": "deepseek-chat",
		"messages": []map[string]any{
			{"role": "assistant", "content": "", "tool_calls": []map[string]any{
				{"id": "call_1", "type": "function", "function": map[string]any{"name": "read", "arguments": `{"path":"a.go"}`}},
			}},
			{"role": "tool", "tool_call_id": "call_1", "content": filler},
		},
	}
	sent, err := jsonMarshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}

	res := env.Post(t, "/v1/chat/completions", body)
	if res.Status != 200 {
		t.Fatalf("expected 200, got %d: %s", res.Status, truncate(res.Body))
	}

	last := upstream.Last(t)
	if !strings.Contains(string(last.Body), "func Foo()") {
		t.Error("a protected tool's source output must reach the provider unchanged")
	}
	if len(last.Body) < len(sent) {
		t.Errorf("a protected tool's body must not be shrunk: sent %d, upstream got %d", len(sent), len(last.Body))
	}
}

// TestHeadroomBuiltinOffForwardsUnchanged is the control that makes the two
// tests above meaningful: with the switch off, the same body is relayed as-is,
// so any shrinkage above is the engine's and not the harness's.
func TestHeadroomBuiltinOffForwardsUnchanged(t *testing.T) {
	env, upstream := newProviderEnv(t)

	filler := strings.Repeat("2024-01-01T00:00:00Z INFO build step completed ok\n", 200)
	body := map[string]any{
		"model": "deepseek-chat",
		"messages": []map[string]any{
			{"role": "assistant", "content": "", "tool_calls": []map[string]any{
				{"id": "call_1", "type": "function", "function": map[string]any{"name": "bash", "arguments": `{"command":"go build ./..."}`}},
			}},
			{"role": "tool", "tool_call_id": "call_1", "content": filler},
		},
	}

	res := env.Post(t, "/v1/chat/completions", body)
	if res.Status != 200 {
		t.Fatalf("expected 200, got %d: %s", res.Status, truncate(res.Body))
	}

	last := upstream.Last(t)
	if len(last.Body) < 3000 {
		t.Errorf("with compression off the body must be relayed in full, got %d bytes", len(last.Body))
	}
}

// TestHeadroomStatusReportsEngine verifies the status endpoint reports which
// engine compression will actually use, read from settings rather than from
// install state: an operator with no Python proxy installed must be able to see
// that compression is still working.
func TestHeadroomStatusReportsEngine(t *testing.T) {
	env, _ := newProviderEnv(t)
	enableHeadroomBuiltin(t, env)

	res := env.Get(t, "/api/headroom/status")
	if res.Status != 200 {
		t.Fatalf("expected 200, got %d: %s", res.Status, truncate(res.Body))
	}
	var status struct {
		Engine string `json:"engine"`
	}
	res.Decode(t, &status)
	if status.Engine != "builtin" {
		t.Errorf("status engine = %q, want builtin", status.Engine)
	}
}

// TestHeadroomSettingsEngineRoundTrip verifies the engine field survives the
// dashboard's own write path.
//
// Normalization is asserted where it actually happens — at read time, by
// NormalizeEngine — rather than at the write path. The write is a raw merge
// (UpdateSettingsRaw) that stores what the client sent, which is deliberate: a
// typo must not be silently rewritten in storage, and the reader decides what
// an unrecognized value means.
func TestHeadroomSettingsEngineRoundTrip(t *testing.T) {
	env := newEnv(t)

	readEngine := func() string {
		t.Helper()
		res := env.Get(t, "/api/settings")
		if res.Status != 200 {
			t.Fatalf("get settings: status %d: %s", res.Status, truncate(res.Body))
		}
		var settings struct {
			HeadroomEngine string `json:"headroomEngine"`
		}
		res.Decode(t, &settings)
		return settings.HeadroomEngine
	}

	// A row that never set the field has nothing stored, which is not the same
	// as "no engine": the runtime resolves the absent value to the built-in
	// engine, so the dashboard must not treat an empty string as a broken
	// setting.
	putSettings(t, env, map[string]any{"headroomEngine": "builtin"})
	if got := readEngine(); got != "builtin" {
		t.Errorf("engine after update = %q, want builtin", got)
	}
	putSettings(t, env, map[string]any{"headroomEngine": "external"})
	if got := readEngine(); got != "external" {
		t.Errorf("engine after update = %q, want external", got)
	}
	// A stored typo reads back verbatim, and the runtime normalizes it to the
	// built-in engine — which is what makes it a typo rather than a way to
	// select a proxy that is not running.
	putSettings(t, env, map[string]any{"headroomEngine": "typo"})
	if got := readEngine(); got != "typo" {
		t.Errorf("engine after typo = %q, want the stored value verbatim", got)
	}
	if got := headroom.NormalizeEngine("typo"); got != headroom.EngineBuiltin {
		t.Errorf("NormalizeEngine(typo) = %q, want builtin", got)
	}
	if got := headroom.NormalizeEngine(""); got != headroom.EngineBuiltin {
		t.Errorf("NormalizeEngine(\"\") = %q, want builtin", got)
	}
}
