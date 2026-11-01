package dashboard

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net/http"
	"strings"
	"unicode"
)

// ZCode plan-lane usage — the start-plan grant buckets behind the two plan
// models, read the way the standalone zcode bridge reads them
// (zcode-bridge/quota.js). GET /api/v1/zcode-plan/billing/balance answers
// both the plan definitions and the per-entitlement balances with daily /
// one-time periods; billing/current is the definitions-only fallback.
//
// The balance endpoint refuses any request without a `x-device-mid` UUID
// (code 3001 parameter error) and the `app_version`/`platform` query —
// verified live 2026-11-01. `current` wants bare auth only: the control
// face empties its payload.

const (
	zcodeBalancePath = "/api/v1/zcode-plan/billing/balance"
	zcodeCurrentPath = "/api/v1/zcode-plan/billing/current"
)

// zcodeBillingOrigin is a var, not a const, so a test can point the fetcher at
// an httptest server. Same shape as antigravityDashboardBaseURL: the paths
// above stay constants, only the host moves.
var zcodeBillingOrigin = "https://zcode.z.ai"

// fetchZcodeUsage reads one connection's grant buckets and renders the
// normalized {plan, quotas} shape the dashboard parser consumes: one row
// per model, plans summed the way bridge quota.js byModel() does.
func fetchZcodeUsage(ctx context.Context, jwt, deviceID string) usageResult {
	jwt = strings.TrimSpace(jwt)
	if jwt == "" {
		return usageResult{message: "ZCode usage unavailable: no plan token stored", bare: true}
	}

	body, status, err := zcodeBillingGet(ctx, zcodeBalancePath+"?app_version=3.16.3&platform=linux-amd64", jwt, zcodeDeviceMid(deviceID))
	// The auth answer has to be read before the generic error: zcodeBillingGet
	// returns a non-nil error for every non-2xx, so checking err first made this
	// branch unreachable and told a user with an expired token to "retry" a
	// request that can only succeed after re-authorization.
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return usageResult{
			plan:    "ZCode",
			message: "ZCode authentication expired. Please re-authorize this connection.",
			bare:    true,
		}
	}
	// Every other non-2xx already arrives as an error carrying its status, so
	// there is no separate status branch below.
	if err != nil {
		return usageResult{message: fmt.Sprintf("ZCode connected. Unable to fetch usage: %v", err), bare: true}
	}

	plans, balances := zcodePlanRows(body)
	if len(plans) == 0 {
		// Balance answered but without plan definitions: fall back to
		// current (bare auth — the control face empties its payload).
		if cur, cstatus, cerr := zcodeBillingGet(ctx, zcodeCurrentPath, jwt, ""); cerr == nil && cstatus >= 200 && cstatus < 300 {
			curPlans, _ := zcodePlanRows(cur)
			if len(curPlans) > 0 {
				plans = curPlans
			}
		}
	}
	if len(plans) == 0 && len(balances) == 0 {
		return usageResult{plan: "ZCode", message: "ZCode connected. No plan grants reported for this account.", bare: true}
	}

	quotas := zcodeQuotaRows(plans, balances)
	if len(quotas) == 0 {
		return usageResult{plan: "ZCode", message: "ZCode connected. No usage rows reported.", bare: true}
	}
	return usageResult{plan: "ZCode", quotas: quotas}
}

// zcodeBillingGet performs one billing GET with the auth (and optional
// control) face, unwrapping the {code,msg,data} envelope. A non-zero code
// inside a 200 is an error carrying the envelope's own message.
func zcodeBillingGet(ctx context.Context, path, jwt, deviceMid string) (map[string]any, int, error) {
	headers := map[string]string{
		"Authorization": "Bearer " + jwt,
		"Accept":        "application/json",
	}
	if deviceMid != "" {
		headers["X-Device-Mid"] = deviceMid
	}
	status, _, out, err := usageGet(ctx, zcodeBillingOrigin+path, headers)
	if err != nil {
		return nil, status, err
	}
	if status < 200 || status >= 300 {
		return nil, status, fmt.Errorf("HTTP %d", status)
	}
	env := usageJSON(out)
	if env == nil {
		return nil, status, fmt.Errorf("response was not JSON")
	}
	if code := usageNum(env["code"], 0); code != 0 {
		msg := usageStr(env["msg"])
		if msg == "" {
			msg = fmt.Sprintf("code %d", int64(code))
		}
		return nil, status, fmt.Errorf("%s", msg)
	}
	data, _ := env["data"].(map[string]any)
	return data, status, nil
}

// zcodePlanRows splits a balance/current payload into its plan definitions
// and per-entitlement balance rows.
func zcodePlanRows(data map[string]any) ([]map[string]any, []map[string]any) {
	if data == nil {
		return nil, nil
	}
	plans, _ := data["plans"].([]any)
	balances, _ := data["balances"].([]any)
	toMaps := func(rows []any) []map[string]any {
		out := make([]map[string]any, 0, len(rows))
		for _, r := range rows {
			if m, ok := r.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return toMaps(plans), toMaps(balances)
}

// zcodeQuotaRows merges plan entitlements and balance rows into per-model
// dashboard rows — the Go twin of bridge quota.js mergeRows + byModel:
// every grant is kept, keyed by model id, usage summed across plans.
func zcodeQuotaRows(plans, balances []map[string]any) map[string]any {
	// Entitlement metadata keyed by entitlement_id: show_name, grant and
	// period for rows the balance list does not carry.
	type ent struct {
		planName   string
		showName   string
		models     []string
		grantUnits float64
		periodEnd  float64
	}
	ents := map[string]ent{}
	for _, plan := range plans {
		planName := usageStr(plan["name"])
		if planName == "" {
			planName = usageStr(plan["plan_id"])
		}
		endsAt := usageNum(plan["ends_at"], 0)
		list, _ := plan["entitlements"].([]any)
		for _, e := range list {
			m, ok := e.(map[string]any)
			if !ok {
				continue
			}
			id := usageStr(m["entitlement_id"])
			if id == "" {
				continue
			}
			ents[id] = ent{
				planName:   planName,
				showName:   usageStr(m["show_name"]),
				models:     zcodeCapabilityModels(m),
				grantUnits: usageNum(m["grant_units"], 0),
				periodEnd:  endsAt,
			}
		}
	}

	// One row per grant bucket — plan grants differ in kind (Start Plan's
	// daily window resets; Weekend Build is one-time and dies with the plan),
	// so summing them under one model row would average away the reset
	// semantics. Each bucket keeps its own plan name and reset.
	type bucket struct {
		display, planName string
		total, used       float64
		// resetEpoch is whatever unit upstream sent for period_end/ends_at:
		// the live endpoint answers seconds (1793548799), but the field is
		// named for the value, not a unit, and usageResetTime normalises both.
		resetEpoch float64
	}
	buckets := map[string]*bucket{}
	order := []string{}
	add := func(key, display, planName string, total, used, periodEnd float64) {
		b, ok := buckets[key]
		if !ok {
			b = &bucket{display: display, planName: planName}
			buckets[key] = b
			order = append(order, key)
		}
		b.total += total
		b.used += used
		if periodEnd > b.resetEpoch {
			b.resetEpoch = periodEnd
		}
	}

	for _, b := range balances {
		id := usageStr(b["entitlement_id"])
		meta := ents[id]
		total := usageNum(b["total_units"], meta.grantUnits)
		used := usageNum(b["used_units"], 0)
		periodEnd := usageNum(b["period_end"], 0)
		if periodEnd == 0 {
			periodEnd = meta.periodEnd
		}
		planName := meta.planName
		if planName == "" {
			planName = usageStr(b["plan_id"])
		}
		display := usageStr(b["show_name"])
		if display == "" {
			display = meta.showName
		}
		models := zcodeCapabilityModels(b)
		if len(models) == 0 {
			models = meta.models
		}
		if len(models) == 0 {
			models = []string{strings.ToLower(display)}
		}
		for _, model := range models {
			// Key per model × entitlement: two grants billing one model stay
			// two bars instead of collapsing into each other.
			add(model+"|"+id, display, planName, total, used, periodEnd)
		}
	}
	// Entitlements defined but absent from the usage list still show their grant.
	for id, meta := range ents {
		found := false
		for _, b := range balances {
			if usageStr(b["entitlement_id"]) == id {
				found = true
				break
			}
		}
		if found {
			continue
		}
		display := meta.showName
		models := meta.models
		if len(models) == 0 {
			models = []string{strings.ToLower(display)}
		}
		for _, model := range models {
			add(model+"|"+id, display, meta.planName, meta.grantUnits, 0, meta.periodEnd)
		}
	}

	quotas := map[string]any{}
	for _, key := range order {
		b := buckets[key]
		model := key[:strings.IndexByte(key, '|')]
		resetAt := usageResetTime(b.resetEpoch)
		name := b.display
		if b.planName != "" {
			// "GLM-5.3 (Start Plan)" — short plan label, drop the vendor
			// prefix noise the upstream plan names carry, and quiet a
			// plan whose own name is all-caps ("ZCODE WEEKEND BUILD").
			plan := b.planName
			for _, prefix := range []string{"ZCODE ", "ZCode "} {
				plan = strings.TrimPrefix(plan, prefix)
			}
			if plan != "" && plan == strings.ToUpper(plan) {
				plan = zcodeTitleCase(plan)
			}
			name = fmt.Sprintf("%s (%s)", b.display, plan)
		}
		// The dashboard's generic parser names each row after its map key,
		// so the pretty name is the key. The internal model id rides along
		// in the row body for anything that needs the canonical id.
		key := name
		for i := 2; quotas[key] != nil; i++ {
			key = fmt.Sprintf("%s (%d)", name, i)
		}
		// The dashboard's generic row renderer reads `remaining` as the
		// remaining PERCENT (bar width + the "%"-suffixed label); the raw
		// token counts travel as used/total. Send the percent, not the
		// token count — the raw number there is what made the bar overflow.
		pct := 0.0
		if b.total > 0 {
			pct = (b.total - b.used) / b.total * 100
			if pct < 0 {
				pct = 0
			}
			if pct > 100 {
				pct = 100
			}
		}
		quotas[key] = map[string]any{
			"total":     b.total,
			"used":      b.used,
			"remaining": int64(pct + 0.5),
			"unit":      "tokens",
			"resetAt":   resetAt,
			"model":     model,
		}
	}
	return quotas
}

// zcodeTitleCase title-cases each word of an all-caps plan label
// ("WEEKEND BUILD" -> "Weekend Build"). strings.Title is deprecated and also
// upper-cases letters after punctuation the plan name never contains.
func zcodeTitleCase(s string) string {
	lower := strings.ToLower(s)
	words := strings.Fields(lower)
	for i, w := range words {
		r := []rune(w)
		r[0] = unicode.ToUpper(r[0])
		words[i] = string(r)
	}
	return strings.Join(words, " ")
}

// zcodeCapabilityModels reads the `model:` prefixed capability entries —
// the model ids an entitlement bills.
func zcodeCapabilityModels(row map[string]any) []string {
	caps, _ := row["capabilities"].([]any)
	models := make([]string, 0, len(caps))
	for _, c := range caps {
		s, ok := c.(string)
		if !ok || !strings.HasPrefix(s, "model:") {
			continue
		}
		if id := strings.TrimPrefix(s, "model:"); id != "" {
			models = append(models, id)
		}
	}
	return models
}

// zcodeDeviceMid picks the billing device UUID: the connection's stored
// deviceId when present, else a stable per-connection fallback. The gateway
// uuid.parse's the value — it must look like a UUID, which the fallback
// guarantees by seeding a hex string from the caller's id.
func zcodeDeviceMid(deviceID string) string {
	if id := strings.TrimSpace(deviceID); id != "" {
		return id
	}
	// Deterministic UUID v4 shape from a fixed seed: same answer on every
	// panel open, no stored state.
	const seed = "9router-zcode-billing"
	sum := sha256.Sum256([]byte(seed))
	lo := binary.BigEndian.Uint64(sum[8:])
	return fmt.Sprintf("%08x-%04x-4%03x-%04x-%012x",
		binary.BigEndian.Uint32(sum[:4]), binary.BigEndian.Uint16(sum[4:6]),
		binary.BigEndian.Uint16(sum[6:8])&0x0fff,
		uint16(lo>>48)&0x3fff|0x8000, lo&0xffffffffffff)
}
