package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func withOpenDesignUsageServer(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	prev := openDesignControlHost
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler(w, r)
	}))
	openDesignControlHost = srv.URL
	t.Cleanup(func() {
		openDesignControlHost = prev
		openDesignWorkspaceMu.Lock()
		openDesignWorkspaceCache = map[string]openDesignWorkspaceEntry{}
		openDesignWorkspaceMu.Unlock()
	})
}

func TestOpenDesignDecimal_ParsesBigIntSafe(t *testing.T) {
	tests := []struct {
		name   string
		input  any
		want   string
		wantOK bool
	}{
		{"nil", nil, "", false},
		{"empty string", "", "", false},
		{"plain string", "42", "42/1", true},
		{"decimal string", "3.5", "7/2", true},
		{"large decimal beyond float64", "99999999999999999999.5", "199999999999999999999/2", true},
		{"float64 input", float64(2.25), "9/4", true},
		{"json.Number", json.Number("1.25"), "5/4", true},
		{"garbage", "not-a-number", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := openDesignDecimal(tt.input)
			if !tt.wantOK {
				if got != nil {
					t.Errorf("openDesignDecimal(%v) = %v, want nil", tt.input, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("openDesignDecimal(%v) = nil, want %s", tt.input, tt.want)
			}
			if got.String() != tt.want {
				t.Errorf("openDesignDecimal(%v) = %s, want %s", tt.input, got.String(), tt.want)
			}
		})
	}
}

func TestOpenDesignPercent_IsExactAcrossFloat64Boundary(t *testing.T) {
	used := openDesignDecimal("99999999999999999999.5")
	limit := openDesignDecimal("100000000000000000000.0")
	percent := new(big.Rat).Mul(used, big.NewRat(1000, 1))
	percent.Quo(percent, limit)
	percent = percent.Mul(percent, big.NewRat(1, 10))
	pctF, _ := percent.Float64()
	pct := math.Max(0, math.Round(pctF*10)/10)
	if pct != 100.0 {
		t.Errorf("percent = %v, want 100 (exact big.Rat path)", pct)
	}
}

func TestOpenDesignWindowName_Boundaries(t *testing.T) {
	tests := []struct {
		name string
		sec  any
		want string
	}{
		{"nil", nil, "Allowance"},
		{"missing key", map[string]any{}, "Allowance"},
		{"zero", 0, "Allowance"},
		{"week", 7 * 24 * 3600, "7 days"},
		{"two weeks", 14 * 24 * 3600, "14 days"},
		{"day", 24 * 3600, "1 days"},
		{"hour", 3600, "1 hours"},
		{"half hour", 1800, "30 minutes"},
		{"90 seconds", 90, "2 minutes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := openDesignWindowName(tt.sec)
			if got != tt.want {
				t.Errorf("openDesignWindowName(%v) = %q, want %q", tt.sec, got, tt.want)
			}
		})
	}
}

func TestOpenDesignPlanLabel(t *testing.T) {
	tests := []struct {
		name string
		tier string
		want string
	}{
		{"empty", "", "Open Design"},
		{"free", "free", "Design Plan Free"},
		{"pro", "pro", "Design Plan Pro"},
		{"enterprise", "enterprise", "Design Plan Enterprise"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := openDesignPlanLabel(tt.tier)
			if got != tt.want {
				t.Errorf("openDesignPlanLabel(%q) = %q, want %q", tt.tier, got, tt.want)
			}
		})
	}
}

func TestControlKeyOfPsd(t *testing.T) {
	tests := []struct {
		name string
		psd  map[string]any
		key  string
		want string
	}{
		{"psd control key wins", map[string]any{"controlKey": "vela_ctrl_live_abc"}, "sk-other", "vela_ctrl_live_abc"},
		{"psd empty falls back to api key", map[string]any{"controlKey": ""}, "vela_ctrl_live_xyz", "vela_ctrl_live_xyz"},
		{"no psd, runtime key", nil, "sk-runtime", ""},
		{"no psd, control key", nil, "vela_ctrl_live_xyz", "vela_ctrl_live_xyz"},
		{"nil psd, empty api key", nil, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := controlKeyOfPsd(tt.psd, tt.key)
			if got != tt.want {
				t.Errorf("controlKeyOfPsd(%v, %q) = %q, want %q", tt.psd, tt.key, got, tt.want)
			}
		})
	}
}

func TestFetchOpenDesignUsage_RuntimeKeyReturnsMessage(t *testing.T) {
	res := fetchOpenDesignUsage(context.Background(), map[string]any{
		"apiKey": "sk-runtime-only",
	})
	if res.message == "" {
		t.Fatalf("expected a message for runtime-only key, got %+v", res)
	}
	if !strings.Contains(res.message, "sign-in") {
		t.Errorf("message should mention sign-in: %q", res.message)
	}
}

func TestFetchOpenDesignUsage_EndToEnd(t *testing.T) {
	var workspaceCalls int
	var preflightCalls int
	var seenAuth []string
	var seenWorkspace string

	withOpenDesignUsageServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/workspaces/current":
			workspaceCalls++
			seenAuth = append(seenAuth, r.Header.Get("Authorization"))
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"activeWorkspaceId":"ws-123","personalMembershipTier":"pro"}`)
		case "/api/v1/workspaces/ws-123/billing/preflight":
			preflightCalls++
			seenAuth = append(seenAuth, r.Header.Get("Authorization"))
			seenWorkspace = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{
				"codingPlan": {
					"eligible": true,
					"windows": [
						{"usedCredits": "12.5", "limitCredits": "50", "durationSeconds": 86400, "resetsAt": 1733000000000},
						{"usedCredits": "100", "limitCredits": "200", "durationSeconds": 604800, "resetsAt": 1733086400000}
					]
				},
				"balanceUsd": 7.25
			}`)
		default:
			http.NotFound(w, r)
		}
	})

	res := fetchOpenDesignUsage(context.Background(), map[string]any{
		"apiKey": "vela_ctrl_live_abc",
		"providerSpecificData": map[string]any{
			"controlKey": "vela_ctrl_live_abc",
		},
	})
	if workspaceCalls != 1 {
		t.Errorf("workspace calls = %d, want 1", workspaceCalls)
	}
	if preflightCalls != 1 {
		t.Errorf("preflight calls = %d, want 1", preflightCalls)
	}
	if len(seenAuth) != 2 || seenAuth[0] != "Bearer vela_ctrl_live_abc" || seenAuth[1] != "Bearer vela_ctrl_live_abc" {
		t.Errorf("unexpected auth headers: %v", seenAuth)
	}
	if seenWorkspace != "/api/v1/workspaces/ws-123/billing/preflight" {
		t.Errorf("preflight path = %q", seenWorkspace)
	}
	if res.plan != "Design Plan Pro" {
		t.Errorf("plan = %q, want Design Plan Pro", res.plan)
	}
	q, ok := res.quotas["1 days"]
	if !ok {
		t.Fatalf("expected a 1 days quota row, got %v", res.quotas)
	}
	qm := q.(map[string]any)
	if used, ok := qm["used"].(float64); !ok || used != 12.5 {
		t.Errorf("used = %v, want 12.5", qm["used"])
	}
	if total, ok := qm["total"].(float64); !ok || total != 50 {
		t.Errorf("total = %v, want 50", qm["total"])
	}
	if pct, ok := qm["remainingPercentage"].(float64); !ok || pct != 25.0 {
		t.Errorf("remainingPercentage = %v, want 25", qm["remainingPercentage"])
	}
	if _, ok := res.extra["balance"]; !ok {
		t.Errorf("expected balance extra, got %v", res.extra)
	}
	if res.extra["balance"] != "$7.25" {
		t.Errorf("balance = %v, want $7.25", res.extra["balance"])
	}
}

func TestFetchOpenDesignUsage_401_Message(t *testing.T) {
	withOpenDesignUsageServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/workspaces/current":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"activeWorkspaceId":"ws-123","personalMembershipTier":"free"}`)
		case "/api/v1/workspaces/ws-123/billing/preflight":
			w.WriteHeader(http.StatusUnauthorized)
		default:
			http.NotFound(w, r)
		}
	})

	res := fetchOpenDesignUsage(context.Background(), map[string]any{
		"providerSpecificData": map[string]any{"controlKey": "vela_ctrl_live_abc"},
	})
	if res.message == "" {
		t.Fatalf("expected a message on 401, got %+v", res)
	}
}

func TestFetchOpenDesignUsage_IneligiblePlan(t *testing.T) {
	withOpenDesignUsageServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/workspaces/current":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"activeWorkspaceId":"ws-123","personalMembershipTier":"free"}`)
		case "/api/v1/workspaces/ws-123/billing/preflight":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"codingPlan": {"eligible": false, "windows": []}, "balanceUsd": 0}`)
		default:
			http.NotFound(w, r)
		}
	})

	res := fetchOpenDesignUsage(context.Background(), map[string]any{
		"providerSpecificData": map[string]any{"controlKey": "vela_ctrl_live_abc"},
	})
	if res.message == "" {
		t.Fatalf("expected a message for ineligible plan, got %+v", res)
	}
	if !strings.Contains(res.message, "wallet balance") {
		t.Errorf("message should mention wallet balance: %q", res.message)
	}
}

func TestOpenDesignWorkspaceCacheTTL(t *testing.T) {
	var calls int
	withOpenDesignUsageServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/workspaces/current" {
			http.NotFound(w, r)
			return
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"activeWorkspaceId":"ws-cached","personalMembershipTier":"free"}`)
	})

	ctx := context.Background()
	id1, _, err1 := openDesignWorkspace(ctx, "ctrl-1")
	id2, _, err2 := openDesignWorkspace(ctx, "ctrl-1")
	if err1 != nil || err2 != nil {
		t.Fatalf("err1=%v err2=%v", err1, err2)
	}
	if id1 != "ws-cached" || id2 != "ws-cached" {
		t.Errorf("ids = %q %q", id1, id2)
	}
	if calls != 1 {
		t.Errorf("workspace calls = %d, want 1 (cached)", calls)
	}

	openDesignWorkspaceMu.Lock()
	openDesignWorkspaceCache["ctrl-1"] = openDesignWorkspaceEntry{
		id: "ws-cached", at: time.Now().Add(-openDesignWorkspaceTTL), tier: "free", expire: time.Now().Add(-time.Second),
	}
	openDesignWorkspaceMu.Unlock()

	id3, _, err3 := openDesignWorkspace(ctx, "ctrl-1")
	if err3 != nil || id3 != "ws-cached" {
		t.Errorf("id3=%q err=%v", id3, err3)
	}
	if calls != 2 {
		t.Errorf("workspace calls after expiry = %d, want 2", calls)
	}
}

func TestOpenDesignWorkspace_DifferentKeysAreIsolated(t *testing.T) {
	var calls int
	withOpenDesignUsageServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/workspaces/current" {
			http.NotFound(w, r)
			return
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"activeWorkspaceId":"ws-shared","personalMembershipTier":"free"}`)
	})

	ctx := context.Background()
	idA, _, errA := openDesignWorkspace(ctx, "ctrl-a")
	idB, _, errB := openDesignWorkspace(ctx, "ctrl-b")
	if errA != nil || errB != nil {
		t.Fatalf("errA=%v errB=%v", errA, errB)
	}
	if idA != "ws-shared" || idB != "ws-shared" {
		t.Errorf("ids = %q %q", idA, idB)
	}
	if calls != 2 {
		t.Errorf("expected 2 workspace reads (one per key), got %d", calls)
	}
}

func TestStoreOpenDesignWorkspace_IsBounded(t *testing.T) {
	openDesignWorkspaceMu.Lock()
	defer openDesignWorkspaceMu.Unlock()
	openDesignWorkspaceCache = map[string]openDesignWorkspaceEntry{}

	base := time.Now()
	for i := range openDesignWorkspaceMax + 10 {
		at := base.Add(time.Duration(i) * time.Second)
		storeOpenDesignWorkspace(fmt.Sprintf("ctrl-%d", i), openDesignWorkspaceEntry{
			id: "ws", at: at, tier: "free", expire: at.Add(openDesignWorkspaceTTL),
		})
	}
	if len(openDesignWorkspaceCache) > openDesignWorkspaceMax {
		t.Fatalf("cache = %d entries, want <= %d", len(openDesignWorkspaceCache), openDesignWorkspaceMax)
	}
	// The oldest insertions were the first evicted, so the newest max survive.
	if _, ok := openDesignWorkspaceCache["ctrl-0"]; ok {
		t.Error("ctrl-0 still cached: oldest entry was not evicted")
	}
	if _, ok := openDesignWorkspaceCache["ctrl-9"]; ok {
		t.Error("ctrl-9 still cached: it is the last evicted insertion")
	}
	newest := openDesignWorkspaceMax - 1
	if _, ok := openDesignWorkspaceCache[fmt.Sprintf("ctrl-%d", newest)]; !ok {
		t.Errorf("ctrl-%d evicted: newest entry must survive", newest)
	}

	// An expired entry is purged even when the map is not yet at the cap.
	openDesignWorkspaceCache["ctrl-expired"] = openDesignWorkspaceEntry{at: base, expire: base.Add(-time.Second)}
	storeOpenDesignWorkspace("ctrl-new", openDesignWorkspaceEntry{at: base, expire: base.Add(openDesignWorkspaceTTL)})
	if _, ok := openDesignWorkspaceCache["ctrl-expired"]; ok {
		t.Error("ctrl-expired still cached after a store")
	}
}

func TestStoreOpenDesignWorkspace_RefreshDoesNotEvict(t *testing.T) {
	openDesignWorkspaceMu.Lock()
	defer openDesignWorkspaceMu.Unlock()
	openDesignWorkspaceCache = map[string]openDesignWorkspaceEntry{}

	base := time.Now()
	for i := range openDesignWorkspaceMax {
		at := base.Add(time.Duration(i) * time.Second)
		storeOpenDesignWorkspace(fmt.Sprintf("ctrl-%d", i), openDesignWorkspaceEntry{
			id: "ws", at: at, tier: "free", expire: at.Add(openDesignWorkspaceTTL),
		})
	}
	// Refreshing a live key must not evict a sibling.
	storeOpenDesignWorkspace("ctrl-0", openDesignWorkspaceEntry{
		id: "ws", at: base, tier: "free", expire: base.Add(openDesignWorkspaceTTL),
	})
	if len(openDesignWorkspaceCache) != openDesignWorkspaceMax {
		t.Fatalf("cache = %d entries, want %d", len(openDesignWorkspaceCache), openDesignWorkspaceMax)
	}
	if _, ok := openDesignWorkspaceCache["ctrl-1"]; !ok {
		t.Error("ctrl-1 evicted by a refresh of ctrl-0")
	}
}
