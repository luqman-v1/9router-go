package executor

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Tests for the friendly-name resolution helpers added by the catalog parity
// fix: the static alias table, the display-name sanitizer, the deterministic
// name map, and the exported post-login refresh entry point.

func TestQoderResolveModelKey(t *testing.T) {
	creds := qoderParityCreds("resolve")
	qoderParityCleanup(t, creds, QoderRegionIntl)

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"static alias flash", "qwen3.8-flash", "qfmodel"},
		{"static alias max", "qwen3.8-max", "qmodel_38max"},
		{"static alias latest", "qwen3.7-max", "qmodel_latest"},
		{"static alias plus", "qwen3.7-plus", "qmodel"},
		{"static alias deepseek pro", "deepseek-v4-pro", "dmodel"},
		{"static alias deepseek flash", "deepseek-v4-flash", "dfmodel"},
		{"static alias glm", "glm-5.2", "gm51model"},
		{"static alias kimi", "kimi-k2.7", "kmodel"},
		{"static alias minimax", "minimax-m3", "mmodel"},
		{"case insensitive", " GLM-5.2 ", "gm51model"},
		{"qoder qualifier", "qoder/qwen3.8-flash", "qfmodel"},
		{"qd qualifier", "qd/kimi-k2.7", "kmodel"},
		{"qoder-cn qualifier", "qoder-cn/glm-5.2", "gm51model"},
		{"raw key passes through", "qfmodel", "qfmodel"},
		{"unknown id passes through", "unknown-model", "unknown-model"},
		{"empty stays empty", "  ", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := qoderResolveModelKey(creds, QoderRegionIntl, tt.input); got != tt.want {
				t.Errorf("qoderResolveModelKey(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// The live catalog outranks the static table: an account whose display name
// claims a friendly name for a different key resolves the way its own model
// list says.
func TestQoderResolveModelKeyCatalogWinsOverStaticTable(t *testing.T) {
	creds := qoderParityCreds("catalog-wins")
	qoderParityCleanup(t, creds, QoderRegionIntl)
	qoderSeedCatalog(t, creds, QoderRegionIntl, map[string]map[string]any{
		"gm51model_v2": {"key": "gm51model_v2", "display_name": "GLM-5.2"},
	})

	if got := qoderResolveModelKey(creds, QoderRegionIntl, "glm-5.2"); got != "gm51model_v2" {
		t.Errorf("live catalog name lost to the static table: got %q, want gm51model_v2", got)
	}
	// A name the catalog does not hold still falls back to the static table.
	if got := qoderResolveModelKey(creds, QoderRegionIntl, "kimi-k2.7"); got != "kmodel" {
		t.Errorf("static fallback broken: got %q, want kmodel", got)
	}
}

func TestQoderAdvertisedID(t *testing.T) {
	tests := []struct {
		name        string
		displayName string
		key         string
		want        string
	}{
		{"simple", "Qwen3.8-Flash", "qfmodel", "qwen3.8-flash"},
		{"whitespace runs collapse", "Deep Seek  V4  Pro", "dmodel", "deep-seek-v4-pro"},
		{"dashes and ends trimmed", " Qwen -- 3.8 ", "qmodel", "qwen-3.8"},
		{"dots and underscores kept", "Model_1.5 beta", "mmodel", "model_1.5-beta"},
		{"digits are alphanumeric", "123", "mmodel", "123"},
		{"punctuation rejected", "Qwen3.8-Flash!", "qfmodel", ""},
		{"non-ascii rejected", "Qwen 3.7 Édition", "qmodel", ""},
		{"separator only rejected", "--", "kmodel", ""},
		{"blank rejected", "   ", "mmodel", ""},
		{"over-long rejected", "qwen-" + strings.Repeat("x", 70), "dmodel", ""},
		{"name equal to key rejected", "Auto", "auto", ""},
		{"name equal to key case-folded", "QFMODEL", "qfmodel", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := QoderAdvertisedID(tt.displayName, tt.key); got != tt.want {
				t.Errorf("QoderAdvertisedID(%q, %q) = %q, want %q", tt.displayName, tt.key, got, tt.want)
			}
		})
	}
}

// Two models sharing one display name cannot reshuffle the mapping between
// refreshes: names are weighed in raw-key order, and a name that is another
// model's live key is never advertised twice.
func TestQoderCatalogNamesDeterministicCollisions(t *testing.T) {
	shared := qoderCatalogNames(map[string]map[string]any{
		"kmodel_latest": {"key": "kmodel_latest", "display_name": "Kimi K2"},
		"kmodel":        {"key": "kmodel", "display_name": "Kimi-K2"},
	})
	if got := shared["kimi-k2"]; got != "kmodel" {
		t.Errorf("kimi-k2 → %q, want kmodel (first key in sorted order keeps the name)", got)
	}
	if got := shared["kmodel_latest"]; got != "kmodel_latest" {
		t.Errorf("kmodel_latest → %q, want itself", got)
	}

	clashing := qoderCatalogNames(map[string]map[string]any{
		"qfmodel": {"key": "qfmodel", "display_name": "gfmodel"},
		"gfmodel": {"key": "gfmodel"},
	})
	if got := clashing["gfmodel"]; got != "gfmodel" {
		t.Errorf("a display name that is another model's key must not override it: gfmodel → %q", got)
	}

	// Every raw key maps to itself, even without a display name.
	bare := qoderCatalogNames(map[string]map[string]any{"auto": {"key": "auto"}})
	if got := bare["auto"]; got != "auto" {
		t.Errorf("auto → %q, want auto", got)
	}
}

// The exported post-login entry point fills the per-credential cache, and a
// failed refresh leaves an existing snapshot in place.
func TestRefreshQoderCatalogPopulatesCache(t *testing.T) {
	creds := qoderParityCreds("refresh")
	qoderParityCleanup(t, creds, QoderRegionIntl)
	doer := &qoderCountingDoer{body: `{"chat":[{"key":"qfmodel","enable":true,"display_name":"Qwen3.8-Flash"}]}`}

	if err := RefreshQoderCatalog(context.Background(), doer.do, creds, QoderRegionIntl); err != nil {
		t.Fatalf("RefreshQoderCatalog: %v", err)
	}
	if config := qoderCachedModelConfig(creds, QoderRegionIntl, "qfmodel"); config == nil {
		t.Fatal("refresh did not populate the cache")
	}
	if got := qoderResolveModelKey(creds, QoderRegionIntl, "qwen3.8-flash"); got != "qfmodel" {
		t.Errorf("friendly name after refresh = %q, want qfmodel", got)
	}

	// A failed forced refresh must not empty a landed snapshot.
	doer.err = fmt.Errorf("upstream unreachable")
	doer.body = ""
	if err := RefreshQoderCatalog(context.Background(), doer.do, creds, QoderRegionIntl); err == nil {
		t.Fatal("refresh against a broken upstream should report the failure")
	}
	if config := qoderCachedModelConfig(creds, QoderRegionIntl, "qfmodel"); config == nil {
		t.Error("failed refresh emptied a live snapshot")
	}
}

// The retry window gates only unfilled snapshots; the placeholder a failed
// attempt leaves must stay invisible to every config reader.
func TestQoderCatalogPlaceholderIsAMiss(t *testing.T) {
	creds := qoderParityCreds("placeholder")
	qoderParityCleanup(t, creds, QoderRegionIntl)
	doer := &qoderCountingDoer{err: fmt.Errorf("upstream unreachable")}
	if _, err := qoderFetchCatalog(context.Background(), doer.do, creds, QoderRegionIntl, false); err == nil {
		t.Fatal("fetch should fail")
	}

	if config := qoderCachedModelConfig(creds, QoderRegionIntl, "qfmodel"); config != nil {
		t.Error("placeholder served a config")
	}
	if entry := qoderCachedModelConfigAny(creds, QoderRegionIntl); entry != nil {
		t.Error("placeholder passed for a live snapshot")
	}
	if _, ok := qoderCatalogKeyForID(creds, QoderRegionIntl, "qfmodel"); ok {
		t.Error("placeholder answered a name lookup")
	}
	if !qoderCatalogRetryHoldoff(creds, QoderRegionIntl) {
		t.Error("placeholder must gate retries inside the window")
	}
}

// Once the window lapses a non-forced fetch is due again.
func TestQoderCatalogRetryHoldoffExpires(t *testing.T) {
	creds := qoderParityCreds("holdoff-expiry")
	qoderParityCleanup(t, creds, QoderRegionIntl)
	doer := &qoderCountingDoer{err: fmt.Errorf("upstream unreachable")}

	if _, err := qoderFetchCatalog(context.Background(), doer.do, creds, QoderRegionIntl, false); err == nil {
		t.Fatal("first fetch should fail")
	}
	qoderCatalogMu.Lock()
	entry := qoderCatalogs[qoderCatalogKey(creds, QoderRegionIntl)]
	if entry == nil {
		qoderCatalogMu.Unlock()
		t.Fatal("failed attempt left no cache entry to gate retries")
	}
	entry.attemptedAt = time.Now().Add(-qoderCatalogRetryWindow - time.Second)
	qoderCatalogMu.Unlock()

	if _, err := qoderFetchCatalog(context.Background(), doer.do, creds, QoderRegionIntl, false); err == nil {
		t.Fatal("fetch after the window should still fail against the broken upstream")
	}
	if got := doer.calls.Load(); got != 2 {
		t.Errorf("fetch after the retry window made %d calls, want 2", got)
	}
}

// A CN credential's catalog fetch must reach the CN gateway, never the intl
// one: the endpoint table is keyed by provider id, and handing it the raw
// region string silently fell through to the intl deployment.
func TestQoderCatalogFetchKeepsRegionIsolation(t *testing.T) {
	tests := []struct {
		region  QoderRegion
		wantURL string
	}{
		{QoderRegionIntl, "https://api3.qoder.sh/algo/api/v2/model/list"},
		{QoderRegionCN, "https://gateway.qoder.com.cn/algo/api/v2/model/list"},
	}
	for _, tt := range tests {
		t.Run(string(tt.region), func(t *testing.T) {
			creds := qoderParityCreds("region-" + string(tt.region))
			qoderParityCleanup(t, creds, tt.region)
			var gotURL string
			do := func(_ context.Context, _, rawURL string, _ map[string]string, _ []byte) (int, []byte, error) {
				gotURL = rawURL
				return 200, []byte(`{"chat":[{"key":"qfmodel","enable":true}]}`), nil
			}
			if _, err := qoderFetchCatalog(context.Background(), do, creds, tt.region, true); err != nil {
				t.Fatalf("qoderFetchCatalog: %v", err)
			}
			if gotURL != tt.wantURL {
				t.Errorf("catalog fetch hit %q, want %q", gotURL, tt.wantURL)
			}
		})
	}
}
