package executor

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// Qoder friendly-name parity — regression tests.
//
// Clients send marketing names ("qwen3.8-flash", "glm-5.2", "kimi-k2.7")
// while model/list publishes opaque keys (qfmodel, gm51model, kmodel). Before
// the fix every friendly name reached the catalog lookup verbatim and failed
// with "model_config … not yet known", even for models the account can use.
// These tests exercise the pre-existing entry points only, so they compile
// against the unfixed tree and fail there at runtime.

// qoderCountingDoer serves one canned model-list answer and counts calls.
type qoderCountingDoer struct {
	calls atomic.Int64
	body  string
	err   error
}

func (d *qoderCountingDoer) do(_ context.Context, _, _ string, _ map[string]string, _ []byte) (int, []byte, error) {
	d.calls.Add(1)
	if d.err != nil {
		return 0, nil, d.err
	}
	return http.StatusOK, []byte(d.body), nil
}

// qoderParityCreds is a credential no other test uses, so cache state cannot
// leak between tests.
func qoderParityCreds(suffix string) QoderCosyCreds {
	return QoderCosyCreds{UserID: "parity-user-" + suffix, AuthToken: "dt-parity-" + suffix, MachineID: "m1"}
}

func qoderParityCleanup(t *testing.T, creds QoderCosyCreds, region QoderRegion) {
	t.Helper()
	t.Cleanup(func() {
		qoderCatalogMu.Lock()
		defer qoderCatalogMu.Unlock()
		delete(qoderCatalogs, qoderCatalogKey(creds, region))
	})
}

// A friendly name that the live catalog publishes as a display_name must
// resolve to the raw key, from the cache alone — no network round trip.
func TestQoderModelConfigFriendlyNameFromLiveCatalog(t *testing.T) {
	creds := qoderParityCreds("display")
	qoderParityCleanup(t, creds, QoderRegionIntl)
	qoderSeedCatalog(t, creds, QoderRegionIntl, map[string]map[string]any{
		"qfmodel": {"key": "qfmodel", "display_name": "Qwen3.8-Flash", "max_input_tokens": 131072.0},
	})
	doer := &qoderCountingDoer{err: fmt.Errorf("network must not be touched")}

	config, err := qoderModelConfig(context.Background(), doer.do, creds, QoderRegionIntl, "qwen3.8-flash")
	if err != nil {
		t.Fatalf("qoderModelConfig(qwen3.8-flash): %v", err)
	}
	if config["key"] != "qfmodel" {
		t.Errorf("model_config.key = %v, want qfmodel", config["key"])
	}
	if doer.calls.Load() != 0 {
		t.Errorf("cached friendly name cost %d fetches, want 0", doer.calls.Load())
	}
}

// Case and surrounding whitespace must not defeat the lookup, and the
// provider qualifier is stripped before resolution.
func TestQoderModelConfigFriendlyNameCaseAndPrefix(t *testing.T) {
	creds := qoderParityCreds("case")
	qoderParityCleanup(t, creds, QoderRegionIntl)
	qoderSeedCatalog(t, creds, QoderRegionIntl, map[string]map[string]any{
		"kmodel": {"key": "kmodel", "display_name": "Kimi-K2.7"},
	})
	doer := &qoderCountingDoer{err: fmt.Errorf("network must not be touched")}

	for _, wire := range []string{"KIMI-K2.7", " kimi-k2.7 ", "qd/kimi-k2.7", "qoder/Kimi-K2.7"} {
		config, err := qoderModelConfig(context.Background(), doer.do, creds, QoderRegionIntl, wire)
		if err != nil {
			t.Fatalf("qoderModelConfig(%q): %v", wire, err)
		}
		if config["key"] != "kmodel" {
			t.Errorf("qoderModelConfig(%q).key = %v, want kmodel", wire, config["key"])
		}
	}
}

// Before any catalog has landed, the deterministic static alias table must
// carry a friendly name to its raw key once the fetch answers.
func TestQoderModelConfigStaticAliasResolvesOnFetch(t *testing.T) {
	creds := qoderParityCreds("static")
	qoderParityCleanup(t, creds, QoderRegionIntl)
	doer := &qoderCountingDoer{body: `{"chat":[{"key":"gm51model","enable":true,"display_name":"GLM 5.2 Air","max_input_tokens":131072}]}`}

	config, err := qoderModelConfig(context.Background(), doer.do, creds, QoderRegionIntl, "glm-5.2")
	if err != nil {
		t.Fatalf("qoderModelConfig(glm-5.2): %v", err)
	}
	if config["key"] != "gm51model" {
		t.Errorf("model_config.key = %v, want gm51model", config["key"])
	}
}

// A display name only the live catalog knows (no static alias row) must
// resolve on the first request, after the fetch lands.
func TestQoderModelConfigDisplayNameOnlyKnownAfterFetch(t *testing.T) {
	creds := qoderParityCreds("fresh")
	qoderParityCleanup(t, creds, QoderRegionIntl)
	doer := &qoderCountingDoer{body: `{"chat":[{"key":"xmodel","enable":true,"display_name":"Special Model"}]}`}

	config, err := qoderModelConfig(context.Background(), doer.do, creds, QoderRegionIntl, "special-model")
	if err != nil {
		t.Fatalf("qoderModelConfig(special-model): %v", err)
	}
	if config["key"] != "xmodel" {
		t.Errorf("model_config.key = %v, want xmodel", config["key"])
	}
}

// The qualified form of a friendly name must survive the cold-fetch path:
// the snapshot indexes bare ids, so the lookup has to strip the qualifier
// exactly like the cache-hit path does.
func TestQoderModelConfigPrefixedFriendlyNameOnColdFetch(t *testing.T) {
	creds := qoderParityCreds("cold-prefix")
	qoderParityCleanup(t, creds, QoderRegionIntl)
	doer := &qoderCountingDoer{body: `{"chat":[{"key":"qfmodel","enable":true,"display_name":"Qwen3.8-Flash"},{"key":"gm51model","enable":true,"display_name":"GLM 5.2"}]}`}

	for _, wire := range []string{"qd/qwen3.8-flash", "qoder/Qwen3.8-Flash", " qd/glm-5.2 "} {
		config, err := qoderModelConfig(context.Background(), doer.do, creds, QoderRegionIntl, wire)
		if err != nil {
			t.Fatalf("qoderModelConfig(%q) on cold cache: %v", wire, err)
		}
		want := "qfmodel"
		if wire == " qd/glm-5.2 " {
			want = "gm51model"
		}
		if config["key"] != want {
			t.Errorf("qoderModelConfig(%q).key = %v, want %s", wire, config["key"], want)
		}
	}
}

// The built payload must carry the raw key everywhere Qoder routes by it —
// model_config.key, chat_context.extra.modelConfig.key and the built key —
// even when the client sent a friendly name.
func TestQoderBuildRequestFriendlyNameStampsRawKey(t *testing.T) {
	creds := qoderParityCreds("build")
	qoderParityCleanup(t, creds, QoderRegionIntl)
	qoderSeedCatalog(t, creds, QoderRegionIntl, map[string]map[string]any{
		"qfmodel": {"key": "qfmodel", "display_name": "Qwen3.8-Flash", "max_output_tokens": 8192.0},
	})
	config, err := qoderModelConfig(context.Background(), (&qoderCountingDoer{err: fmt.Errorf("no network")}).do,
		creds, QoderRegionIntl, "qwen3.8-flash")
	if err != nil {
		t.Fatalf("qoderModelConfig: %v", err)
	}

	body := []byte(`{"model":"qd/qwen3.8-flash","messages":[{"role":"user","content":"hi"}],"max_tokens":10}`)
	built, err := buildQoderChatRequestWithAttachments(context.Background(), body, config, creds, 1700000000000, QoderRegionIntl, nil, nil)
	if err != nil {
		t.Fatalf("buildQoderChatRequestWithAttachments: %v", err)
	}
	if built.QoderKey != "qfmodel" {
		t.Errorf("QoderKey = %q, want qfmodel", built.QoderKey)
	}
	modelConfig, _ := built.Payload["model_config"].(map[string]any)
	if modelConfig["key"] != "qfmodel" {
		t.Errorf("payload model_config.key = %v, want qfmodel", modelConfig["key"])
	}
	chatContext, _ := built.Payload["chat_context"].(map[string]any)
	extra, _ := chatContext["extra"].(map[string]any)
	extraConfig, _ := extra["modelConfig"].(map[string]any)
	if extraConfig["key"] != "qfmodel" {
		t.Errorf("chat_context.extra.modelConfig.key = %v, want qfmodel", extraConfig["key"])
	}
}

// A failed fetch on a credential with no live snapshot must hold non-forced
// fetches off for the retry window, while a forced fetch still goes through.
// Without the window every request on an unreachable catalog pays for a fresh
// fetch and its timeout.
func TestQoderCatalogRetryHoldoffAfterFailure(t *testing.T) {
	creds := qoderParityCreds("holdoff")
	qoderParityCleanup(t, creds, QoderRegionIntl)
	doer := &qoderCountingDoer{err: fmt.Errorf("upstream unreachable")}

	if _, err := qoderFetchCatalog(context.Background(), doer.do, creds, QoderRegionIntl, false); err == nil {
		t.Fatal("first fetch should fail")
	}
	if got := doer.calls.Load(); got != 1 {
		t.Fatalf("first fetch made %d calls, want 1", got)
	}

	if _, err := qoderFetchCatalog(context.Background(), doer.do, creds, QoderRegionIntl, false); err == nil {
		t.Fatal("second fetch should fail inside the retry window")
	}
	if got := doer.calls.Load(); got != 1 {
		t.Errorf("non-forced fetch inside the retry window made %d calls, want 1 (held off)", got)
	}

	if _, err := qoderFetchCatalog(context.Background(), doer.do, creds, QoderRegionIntl, true); err == nil {
		t.Fatal("forced fetch should still fail against the broken upstream")
	}
	if got := doer.calls.Load(); got != 2 {
		t.Errorf("forced fetch must bypass the retry window: %d calls, want 2", got)
	}
}

// A filled snapshot is governed by the TTL, not the retry window: an expired
// entry refetches even right after a failed attempt, and a failed refresh
// leaves the stale configs readable.
func TestQoderCatalogExpiredFilledEntryStillRefetches(t *testing.T) {
	creds := qoderParityCreds("expired")
	qoderParityCleanup(t, creds, QoderRegionIntl)
	qoderSeedCatalog(t, creds, QoderRegionIntl, map[string]map[string]any{
		"qfmodel": {"key": "qfmodel"},
	})
	qoderCatalogMu.Lock()
	qoderCatalogs[qoderCatalogKey(creds, QoderRegionIntl)].expiresAt = time.Now().Add(-time.Minute)
	qoderCatalogMu.Unlock()
	doer := &qoderCountingDoer{err: fmt.Errorf("upstream unreachable")}

	if _, err := qoderFetchCatalog(context.Background(), doer.do, creds, QoderRegionIntl, false); err == nil {
		t.Fatal("fetch should fail against the broken upstream")
	}
	if got := doer.calls.Load(); got != 1 {
		t.Errorf("expired filled entry must refetch without holdoff: %d calls, want 1", got)
	}
}

// A successful fetch after a failure replaces the placeholder and serves the
// cache without further network traffic.
func TestQoderCatalogSuccessAfterFailureServesCache(t *testing.T) {
	creds := qoderParityCreds("recover")
	qoderParityCleanup(t, creds, QoderRegionIntl)
	doer := &qoderCountingDoer{err: fmt.Errorf("upstream unreachable")}
	if _, err := qoderFetchCatalog(context.Background(), doer.do, creds, QoderRegionIntl, false); err == nil {
		t.Fatal("first fetch should fail")
	}

	doer.err = nil
	doer.body = `{"chat":[{"key":"qfmodel","enable":true,"display_name":"Qwen3.8-Flash"}]}`
	if _, err := qoderFetchCatalog(context.Background(), doer.do, creds, QoderRegionIntl, true); err != nil {
		t.Fatalf("forced recovery fetch: %v", err)
	}
	calls := doer.calls.Load()

	config, err := qoderModelConfig(context.Background(), doer.do, creds, QoderRegionIntl, "qwen3.8-flash")
	if err != nil {
		t.Fatalf("qoderModelConfig after recovery: %v", err)
	}
	if config["key"] != "qfmodel" {
		t.Errorf("model_config.key = %v, want qfmodel", config["key"])
	}
	if doer.calls.Load() != calls {
		t.Errorf("recovered catalog served from network again: %d extra calls", doer.calls.Load()-calls)
	}
}
