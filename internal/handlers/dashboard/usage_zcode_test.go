package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// zcode quotas are built from two payloads: plan definitions (entitlements,
// grant sizes, plan end) and per-entitlement balances (used units, period end).
// The merge is what the dashboard bar width and the plan label come from, so
// the cases below pin the arithmetic rather than the wire shape.
func TestZcodeQuotaRows(t *testing.T) {
	tests := []struct {
		name     string
		plans    []map[string]any
		balances []map[string]any
		wantKey  string
		want     map[string]any
		absent   []string
	}{
		{
			name: "balance row drives used and percent for one model",
			plans: []map[string]any{{
				"name": "ZCode Start Plan",
				"entitlements": []any{map[string]any{
					"entitlement_id": "ent_start",
					"show_name":      "GLM-5.3",
					"grant_units":    float64(1000),
					"capabilities":   []any{"model:glm-5.3"},
				}},
			}},
			balances: []map[string]any{{
				"entitlement_id": "ent_start",
				"total_units":    float64(1000),
				"used_units":     float64(250),
				"period_end":     float64(1793548799),
			}},
			wantKey: "GLM-5.3 (Start Plan)",
			want: map[string]any{
				"total":     float64(1000),
				"used":      float64(250),
				"remaining": int64(75),
				"unit":      "tokens",
				"resetAt":   "2026-11-01T15:59:59Z",
				"model":     "glm-5.3",
			},
		},
		{
			name: "plans with no balance row fall back to the grant as the total",
			plans: []map[string]any{{
				"name":    "ZCODE WEEKEND BUILD",
				"ends_at": float64(1793807999),
				"entitlements": []any{map[string]any{
					"entitlement_id": "ent_weekend",
					"show_name":      "GLM-5.3-Flash",
					"grant_units":    float64(5000000),
					"capabilities":   []any{"model:glm-5.3-flash"},
				}},
			}},
			wantKey: "GLM-5.3-Flash (Weekend Build)",
			want: map[string]any{
				"total":     float64(5000000),
				"used":      float64(0),
				"remaining": int64(100),
				"resetAt":   "2026-11-04T15:59:59Z",
				"model":     "glm-5.3-flash",
			},
		},
		{
			name: "a millisecond period_end is normalised, not multiplied twice",
			plans: []map[string]any{{
				"name": "ZCode Start Plan",
				"entitlements": []any{map[string]any{
					"entitlement_id": "ent_ms",
					"show_name":      "GLM-5.3",
					"grant_units":    float64(100),
					"capabilities":   []any{"model:glm-5.3"},
				}},
			}},
			balances: []map[string]any{{
				"entitlement_id": "ent_ms",
				"total_units":    float64(100),
				"used_units":     float64(100),
				"period_end":     float64(1793548799000),
			}},
			wantKey: "GLM-5.3 (Start Plan)",
			want: map[string]any{
				"resetAt":   "2026-11-01T15:59:59Z",
				"remaining": int64(0),
			},
		},
		{
			name: "an exhausted bucket never reports a negative percentage",
			plans: []map[string]any{{
				"name": "ZCode Start Plan",
				"entitlements": []any{map[string]any{
					"entitlement_id": "ent_over",
					"show_name":      "GLM-5.3",
					"grant_units":    float64(100),
					"capabilities":   []any{"model:glm-5.3"},
				}},
			}},
			balances: []map[string]any{{
				"entitlement_id": "ent_over",
				"total_units":    float64(100),
				"used_units":     float64(180),
			}},
			wantKey: "GLM-5.3 (Start Plan)",
			want: map[string]any{
				"used":      float64(180),
				"remaining": int64(0),
			},
		},
		{
			name: "two entitlements for one model collide into distinct row keys",
			plans: []map[string]any{{
				"name": "ZCode Start Plan",
				"entitlements": []any{
					map[string]any{
						"entitlement_id": "ent_a",
						"show_name":      "GLM-5.3",
						"grant_units":    float64(100),
						"capabilities":   []any{"model:glm-5.3"},
					},
					map[string]any{
						"entitlement_id": "ent_b",
						"show_name":      "GLM-5.3",
						"grant_units":    float64(100),
						"capabilities":   []any{"model:glm-5.3"},
					},
				},
			}},
			// Both rows render the same label, so the second must be suffixed
			// rather than overwrite the first.
			wantKey: "GLM-5.3 (Start Plan) (2)",
			absent:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := zcodeQuotaRows(tt.plans, tt.balances)
			row, ok := got[tt.wantKey].(map[string]any)
			if !ok {
				keys := make([]string, 0, len(got))
				for k := range got {
					keys = append(keys, k)
				}
				t.Fatalf("zcodeQuotaRows() keys = %v, want %q", keys, tt.wantKey)
			}
			for field, want := range tt.want {
				if row[field] != want {
					t.Errorf("%s = %v, want %v", field, row[field], want)
				}
			}
		})
	}
}

// TestZcodeQuotaRowsCollisionKeepsBothRows guards the row-key suffix: two
// entitlements that bill the same model must not collapse into one bar.
func TestZcodeQuotaRowsCollisionKeepsBothRows(t *testing.T) {
	plans := []map[string]any{{
		"name": "ZCode Start Plan",
		"entitlements": []any{
			map[string]any{"entitlement_id": "ent_a", "show_name": "GLM-5.3", "grant_units": float64(100), "capabilities": []any{"model:glm-5.3"}},
			map[string]any{"entitlement_id": "ent_b", "show_name": "GLM-5.3", "grant_units": float64(100), "capabilities": []any{"model:glm-5.3"}},
		},
	}}

	got := zcodeQuotaRows(plans, nil)
	if len(got) != 2 {
		t.Fatalf("zcodeQuotaRows() produced %d rows, want 2: %v", len(got), got)
	}
	for _, key := range []string{"GLM-5.3 (Start Plan)", "GLM-5.3 (Start Plan) (2)"} {
		if _, ok := got[key].(map[string]any); !ok {
			t.Errorf("missing row %q", key)
		}
	}
}

// TestFetchZcodeUsage drives the fetcher through both payloads the panel sees:
// a healthy balance answer, the definitions-only `current` fallback when
// balance reports no plans, and the auth failure the connection has to surface.
func TestFetchZcodeUsage(t *testing.T) {
	const balanceBody = `{"code":0,"data":{"plans":[{"name":"ZCode Start Plan","entitlements":[{"entitlement_id":"ent_start","show_name":"GLM-5.3","grant_units":1000,"capabilities":["model:glm-5.3"]}]}],"balances":[{"entitlement_id":"ent_start","total_units":1000,"used_units":250,"period_end":1793548799}]}}`
	const currentOnlyBody = `{"code":0,"data":{"plans":[{"name":"ZCode Start Plan","ends_at":1793548799,"entitlements":[{"entitlement_id":"ent_start","show_name":"GLM-5.3","grant_units":1000,"capabilities":["model:glm-5.3"]}]}]}}`

	tests := []struct {
		name        string
		handler     func(t *testing.T) http.HandlerFunc
		jwt         string
		wantMessage string
		wantQuotas  bool
		wantBare    bool
		check       func(t *testing.T, res usageResult)
	}{
		{
			name: "balance answer renders one quota row per model",
			handler: func(t *testing.T) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("X-Device-Mid") == "" {
						t.Errorf("balance request carried no X-Device-Mid; the endpoint answers 3001 without it")
					}
					if got := r.URL.Query().Get("app_version"); got == "" {
						t.Errorf("balance request carried no app_version query")
					}
					_, _ = w.Write([]byte(balanceBody))
				}
			},
			jwt:        "plan.jwt.value",
			wantQuotas: true,
			check: func(t *testing.T, res usageResult) {
				if res.plan != "ZCode" {
					t.Errorf("plan = %q, want ZCode", res.plan)
				}
				row, ok := res.quotas["GLM-5.3 (Start Plan)"].(map[string]any)
				if !ok {
					t.Fatalf("quotas = %v, missing the model row", res.quotas)
				}
				if row["remaining"] != int64(75) {
					t.Errorf("remaining = %v, want 75", row["remaining"])
				}
			},
		},
		{
			name: "balance without plans falls back to the current definitions",
			handler: func(t *testing.T) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == zcodeBalancePath {
						_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
						return
					}
					if r.Header.Get("X-Device-Mid") != "" {
						t.Errorf("current fallback sent the control header face; it empties the payload")
					}
					_, _ = w.Write([]byte(currentOnlyBody))
				}
			},
			jwt:        "plan.jwt.value",
			wantQuotas: true,
			check: func(t *testing.T, res usageResult) {
				if _, ok := res.quotas["GLM-5.3 (Start Plan)"].(map[string]any); !ok {
					t.Fatalf("quotas = %v, want the row built from current", res.quotas)
				}
			},
		},
		{
			name: "expired token asks for re-authorization instead of a generic error",
			handler: func(t *testing.T) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"code":401,"msg":"unauthorized"}`))
				}
			},
			jwt:         "plan.jwt.value",
			wantMessage: "ZCode authentication expired. Please re-authorize this connection.",
			wantBare:    true,
		},
		{
			name: "a non-zero code inside HTTP 200 surfaces the envelope message",
			handler: func(t *testing.T) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					_, _ = w.Write([]byte(`{"code":3001,"msg":"parameter error"}`))
				}
			},
			jwt:         "plan.jwt.value",
			wantMessage: "ZCode connected. Unable to fetch usage: parameter error",
			wantBare:    true,
		},
		{
			name: "no stored token never reaches the network",
			handler: func(t *testing.T) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					t.Errorf("request reached upstream with an empty token")
				}
			},
			jwt:         "",
			wantMessage: "ZCode usage unavailable: no plan token stored",
			wantBare:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.handler(t))
			t.Cleanup(srv.Close)
			prev := zcodeBillingOrigin
			zcodeBillingOrigin = srv.URL
			t.Cleanup(func() { zcodeBillingOrigin = prev })

			res := fetchZcodeUsage(context.Background(), tt.jwt, "")

			if tt.wantQuotas {
				if res.message != "" {
					t.Fatalf("message = %q, want quota rows", res.message)
				}
				if len(res.quotas) == 0 {
					t.Fatalf("quotas empty, want at least one row")
				}
				if tt.check != nil {
					tt.check(t, res)
				}
				return
			}
			if res.message != tt.wantMessage {
				t.Errorf("message = %q, want %q", res.message, tt.wantMessage)
			}
			if res.bare != tt.wantBare {
				t.Errorf("bare = %v, want %v", res.bare, tt.wantBare)
			}
		})
	}
}

// TestZcodePlanRowsSplitsDefinitionsFromBalances keeps the two payload halves
// honest: a balance row must never be read as a plan definition, because the
// quota merge keys plans by entitlement_id and would drop a mistyped half.
func TestZcodePlanRowsSplitsDefinitionsFromBalances(t *testing.T) {
	var data map[string]any
	raw := `{"plans":[{"name":"p"}],"balances":[{"entitlement_id":"e"},{"entitlement_id":"f"}],"ignored":"x"}`
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}

	plans, balances := zcodePlanRows(data)
	if len(plans) != 1 {
		t.Errorf("plans = %d, want 1", len(plans))
	}
	if len(balances) != 2 {
		t.Errorf("balances = %d, want 2", len(balances))
	}
}
