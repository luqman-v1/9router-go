package codexquota

import (
	"testing"
	"time"
)

// The endpoint answers a multi-workspace map, so the record has to be chosen by
// the caller's own account id rather than by map order.
func TestParseSubscription_PicksTheCallersWorkspace(t *testing.T) {
	body := []byte(`{
		"accounts": {
			"org-a": {"account": {"account_id": "aaaa", "plan_type": "free", "is_default": false}},
			"org-b": {"account": {"account_id": "bbbb", "plan_type": "plus", "is_default": true},
			          "entitlement": {"subscription_plan": "plus", "expires_at": "2026-12-09T15:04:05Z"}}
		}
	}`)

	sub, err := ParseSubscription(body, "bbbb")
	if err != nil {
		t.Fatalf("ParseSubscription: %v", err)
	}
	if sub.Plan != "plus" {
		t.Errorf("plan = %q, want plus", sub.Plan)
	}
	want := time.Date(2026, 12, 9, 15, 4, 5, 0, time.UTC)
	if !sub.HasExpiry() || !sub.ExpiresAt.Equal(want) {
		t.Errorf("expiry = %v, want %v", sub.ExpiresAt, want)
	}
}

// A team or enterprise seat carries no entitlement expiry: the plan must still
// resolve and HasExpiry must stay false, so the dashboard never draws year 1.
func TestParseSubscription_NoEntitlementMeansNoExpiry(t *testing.T) {
	body := []byte(`{"accounts":{"org-x":{"account":{"plan_type":"team","is_default":true}}}}`)

	sub, err := ParseSubscription(body, "")
	if err != nil {
		t.Fatalf("ParseSubscription: %v", err)
	}
	if sub.Plan != "team" {
		t.Errorf("plan = %q, want team", sub.Plan)
	}
	if sub.HasExpiry() {
		t.Errorf("expiry = %v, want none", sub.ExpiresAt)
	}
}

// An unknown account id must not pick a free seat when a paid one exists, and an
// account-less response is an error rather than an empty plan.
func TestParseSubscription_FallsBackPastFree(t *testing.T) {
	body := []byte(`{"accounts":{
		"a":{"account":{"account_id":"a","plan_type":"free"}},
		"b":{"account":{"account_id":"b","plan_type":"pro"},"entitlement":{"expires_at":"2026-12-01T00:00:00Z"}}
	}}`)

	sub, err := ParseSubscription(body, "does-not-exist")
	if err != nil {
		t.Fatalf("ParseSubscription: %v", err)
	}
	if sub.Plan != "pro" {
		t.Errorf("plan = %q, want pro", sub.Plan)
	}

	if _, err := ParseSubscription([]byte(`{"accounts":{}}`), ""); err == nil {
		t.Error("an empty accounts map must be an error")
	}
}

// camelCase is accepted for the accounts where the endpoint answers that shape.
func TestParseSubscription_AcceptsCamelCase(t *testing.T) {
	body := []byte(`{"accounts":{"o":{"account":{"planType":"plus"},"entitlement":{"expiresAt":"2026-12-01T00:00:00Z"}}}}`)

	sub, err := ParseSubscription(body, "")
	if err != nil {
		t.Fatalf("ParseSubscription: %v", err)
	}
	if sub.Plan != "plus" || !sub.HasExpiry() {
		t.Errorf("plan = %q expiry = %v", sub.Plan, sub.ExpiresAt)
	}
}

// A lapsed Plus seat whose account has already been downgraded to free must
// report free and carry no end date: ChatGPT keeps the old subscription_plan
// and its date in the envelope, and reading either drawn is a false badge.
func TestParseSubscription_ExpiredEntitlementOnFreeAccount(t *testing.T) {
	body := []byte(`{"accounts":{"org":{"account":{"account_id":"o","plan_type":"free"},
		"entitlement":{"subscription_plan":"chatgptplusplan","expires_at":"2026-11-20T19:11:44Z"}}}}`)
	now := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)

	sub, err := parseSubscriptionAt(body, "o", now)
	if err != nil {
		t.Fatalf("parseSubscriptionAt: %v", err)
	}
	if sub.Plan != "free" {
		t.Errorf("plan = %q, want free", sub.Plan)
	}
	if sub.HasExpiry() {
		t.Errorf("expiry = %v, want none for a free plan", sub.ExpiresAt)
	}
}

// The same record read before the entitlement lapses is still a live Plus seat:
// the rule must key on the date, not on the presence of the fields.
func TestParseSubscription_LiveEntitlementOnFreeAccount(t *testing.T) {
	body := []byte(`{"accounts":{"org":{"account":{"account_id":"o","plan_type":"free"},
		"entitlement":{"subscription_plan":"plus","expires_at":"2026-12-20T19:11:44Z"}}}}`)
	now := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)

	sub, err := parseSubscriptionAt(body, "o", now)
	if err != nil {
		t.Fatalf("parseSubscriptionAt: %v", err)
	}
	if sub.Plan != "plus" || !sub.HasExpiry() {
		t.Errorf("plan = %q expiry = %v, want plus with an expiry", sub.Plan, sub.ExpiresAt)
	}
}

// The compact ids the endpoint returns fold into the tier names the dashboard
// labels, so the badge never draws "chatgptplusplan".
func TestNormalizePlan(t *testing.T) {
	for in, want := range map[string]string{
		"chatgptplusplan": "plus",
		"chatgptplus":     "plus",
		"ChatGPTProPlan":  "pro",
		"Chat GPT Team":   "team",
		"plus":            "plus",
		"":                "",
	} {
		if got := normalizePlan(in); got != want {
			t.Errorf("normalizePlan(%q) = %q, want %q", in, got, want)
		}
	}
}
