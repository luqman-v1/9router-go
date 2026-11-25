package dashboard

import (
	json "encoding/json/v2"
	"testing"
)

// kiroPoolingResponse is the shape a KIRO_POOLING_SEAT answers: two CREDIT
// rows, the NON_POOLED one carrying usage but no limit and the POOLED one
// carrying the shared pool.
const kiroPoolingResponse = `{
  "nextDateReset": 1796083200,
  "subscriptionInfo": {"subscriptionTitle": "KIRO CREDIT POOLING", "type": "KIRO_POOLING_SEAT"},
  "usageBreakdownList": [
    {"resourceType": "CREDIT", "dimensionType": "NON_POOLED", "currentUsageWithPrecision": 0.59, "usageLimitWithPrecision": null},
    {"resourceType": "CREDIT", "dimensionType": "POOLED", "currentUsageWithPrecision": 101.51, "usageLimitWithPrecision": 84717}
  ]
}`

// Two rows sharing a resourceType used to collide on one key, so the dashboard
// showed whichever AWS sent last — and a limit-less row read as
// "0 total, -0.59 remaining", i.e. an exhausted pool that was never granted.
func TestParseKiroQuotaData_SeparatesPooledFromNonPooled(t *testing.T) {
	parse := func(t *testing.T, raw string) usageResult {
		t.Helper()
		var m map[string]any
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return parseKiroQuotaData(m)
	}

	res := parse(t, kiroPoolingResponse)
	pooled, ok := res.quotas["credit_pooled"].(map[string]any)
	if !ok {
		t.Fatalf("pooled credit missing: %v", res.quotas)
	}
	if pooled["total"] != 84717.0 || pooled["remaining"] != 84615.49 {
		t.Errorf("pooled quota wrong: %v", pooled)
	}
	if pooled["unlimited"] != false {
		t.Errorf("a limited pool must not report unlimited: %v", pooled)
	}

	nonPooled, ok := res.quotas["credit_non_pooled"].(map[string]any)
	if !ok {
		t.Fatalf("non-pooled credit missing: %v", res.quotas)
	}
	if nonPooled["unlimited"] != true || nonPooled["remaining"] != 0.0 {
		t.Errorf("a limit-less row must read unlimited with no negative remaining: %v", nonPooled)
	}

	// The same rows in the other order must classify identically: keying on
	// position is exactly the bug this guards.
	res2 := parse(t, `{
	  "nextDateReset": 1796083200,
	  "usageBreakdownList": [
	    {"resourceType": "CREDIT", "dimensionType": "POOLED", "currentUsageWithPrecision": 101.51, "usageLimitWithPrecision": 84717},
	    {"resourceType": "CREDIT", "dimensionType": "NON_POOLED", "currentUsageWithPrecision": 0.59, "usageLimitWithPrecision": null}
	  ]
	}`)
	if len(res2.quotas) != len(res.quotas) {
		t.Fatalf("row order changed the quota set: %v", res2.quotas)
	}
	for name, want := range res.quotas {
		got, ok := res2.quotas[name].(map[string]any)
		if !ok {
			t.Errorf("reversed order lost %q: %v", name, res2.quotas)
			continue
		}
		w := want.(map[string]any)
		if got["total"] != w["total"] || got["remaining"] != w["remaining"] || got["unlimited"] != w["unlimited"] {
			t.Errorf("%q differs by row order: %v vs %v", name, got, w)
		}
	}
}
