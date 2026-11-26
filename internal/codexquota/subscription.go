package codexquota

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// SubscriptionURL is the ChatGPT account-check endpoint. wham/usage reports the
// rate-limit windows but never when the plan itself ends, so the tier and its
// expiry come from here (the same source cockpit-tools and ccLoad read).
// Settable so tests can point it at an httptest server.
var SubscriptionURL = "https://chatgpt.com/backend-api/accounts/check/v4-2023-04-27"

// Subscription is the account's plan tier and when it lapses.
//
// ExpiresAt is the zero time when the endpoint returned no parseable expiry —
// a team or enterprise seat has none, and an account whose entitlement is
// missing must not be shown as expiring.
type Subscription struct {
	Plan      string
	ExpiresAt time.Time
}

// HasExpiry reports whether the plan carries a known end date.
func (s Subscription) HasExpiry() bool { return !s.ExpiresAt.IsZero() }

// wireSubscription is the part of the accounts/check envelope this package
// reads. Both spellings are accepted: the endpoint has returned the snake_case
// form and, for some accounts, camelCase.
type wireSubscription struct {
	Accounts map[string]wireSubscriptionRecord `json:"accounts"`
}

type wireSubscriptionRecord struct {
	Account     *wireSubscriptionAccount     `json:"account"`
	Entitlement *wireSubscriptionEntitlement `json:"entitlement"`
}

type wireSubscriptionAccount struct {
	PlanType  string `json:"plan_type"`
	PlanTypeC string `json:"planType"`
	IsDefault bool   `json:"is_default"`
	AccountID string `json:"account_id"`
}

type wireSubscriptionEntitlement struct {
	ExpiresAt        string `json:"expires_at"`
	ExpiresAtC       string `json:"expiresAt"`
	SubscriptionPlan string `json:"subscription_plan"`
}

// ParseSubscription decodes an accounts/check body and picks the record that
// describes the signed-in workspace.
//
// Precedence mirrors cockpit-tools' parse_account_check_snapshot: an exact
// account-id match wins, then the record flagged is_default, then the first one
// on a paid tier, then the first record at all. The account id comes from the
// id_token claim 9router already stores, so a multi-workspace account resolves
// to its own seat rather than to whichever record happens to come first.
func ParseSubscription(body []byte, accountID string) (*Subscription, error) {
	return parseSubscriptionAt(body, accountID, time.Now())
}

// parseSubscriptionAt is ParseSubscription with the clock injected so the
// expired-entitlement rule is testable.
func parseSubscriptionAt(body []byte, accountID string, now time.Time) (*Subscription, error) {
	var wire wireSubscription
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, fmt.Errorf("codexquota.ParseSubscription: decode: %w", err)
	}
	if len(wire.Accounts) == 0 {
		return nil, fmt.Errorf("codexquota.ParseSubscription: no accounts in response")
	}

	// Map order is not meaningful, so candidates are gathered then ranked. The
	// account's own tier and the entitlement's are kept apart: which one wins
	// depends on whether the entitlement has already lapsed.
	type candidate struct {
		id, accountPlan, entitlementPlan, expires string
		isDefault                                 bool
	}
	// plan resolves the record's tier the way cockpit-tools does. An expired
	// entitlement on a free account means the paid seat is over: ChatGPT has
	// already downgraded and `subscription_plan` is the stale value. Anywhere
	// else the entitlement wins over the account's own plan_type, because it is
	// the field the billing side maintains.
	planOf := func(c candidate) string {
		if !strings.EqualFold(strings.TrimSpace(c.accountPlan), "free") {
			return firstNonEmpty(c.entitlementPlan, c.accountPlan)
		}
		if t := parseResetTime(c.expires); t != nil && !t.After(now) {
			return "free"
		}
		return firstNonEmpty(c.entitlementPlan, c.accountPlan)
	}
	// ended reports an expired seat, whose date must not be drawn: a free plan
	// has no end date, and "Free · Ended 28 Oct" reads as a contradiction.
	ended := func(c candidate) bool {
		t := parseResetTime(c.expires)
		return t != nil && !t.After(now) && strings.EqualFold(strings.TrimSpace(c.accountPlan), "free")
	}
	var candidates []candidate
	for id, rec := range wire.Accounts {
		if rec.Account == nil && rec.Entitlement == nil {
			continue
		}
		c := candidate{id: id}
		if rec.Account != nil {
			c.accountPlan = firstNonEmpty(rec.Account.PlanType, rec.Account.PlanTypeC)
			c.isDefault = rec.Account.IsDefault
			if c.id == "" {
				c.id = rec.Account.AccountID
			}
		}
		if rec.Entitlement != nil {
			c.expires = firstNonEmpty(rec.Entitlement.ExpiresAt, rec.Entitlement.ExpiresAtC)
			c.entitlementPlan = strings.TrimSpace(rec.Entitlement.SubscriptionPlan)
		}
		candidates = append(candidates, c)
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("codexquota.ParseSubscription: no usable account record")
	}

	pick := candidates[0]
	for _, c := range candidates {
		if accountID != "" && strings.EqualFold(c.id, accountID) {
			pick = c
			break
		}
	}
	if accountID == "" || !strings.EqualFold(pick.id, accountID) {
		for _, c := range candidates {
			if c.isDefault {
				pick = c
				break
			}
		}
	}
	if plan := planOf(pick); strings.TrimSpace(plan) == "" || strings.EqualFold(plan, "free") {
		// Only when the caller's own record was not matched: a resolved-but-free
		// seat is the answer, not a reason to report someone else's plan.
		if accountID == "" || !strings.EqualFold(pick.id, accountID) {
			for _, c := range candidates {
				if plan := planOf(c); plan != "" && !strings.EqualFold(plan, "free") {
					pick = c
					break
				}
			}
		}
	}
	sub := &Subscription{Plan: normalizePlan(planOf(pick))}
	if !ended(pick) {
		if t := parseResetTime(pick.expires); t != nil {
			sub.ExpiresAt = *t
		}
	}
	return sub, nil
}

// normalizePlan folds the compact ids the endpoint returns into the tier names
// the dashboard labels. "chatgptplusplan" and "chatgptplus" are the same seat as
// "plus", and without this the badge draws the raw id.
func normalizePlan(plan string) string {
	lowered := strings.ToLower(strings.TrimSpace(plan))
	compact := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		}
		return -1
	}, lowered)
	switch compact {
	case "free", "freeplan", "freetier", "chatgptfree", "chatgptfreeplan", "chatgptfreetier":
		return "free"
	case "plus", "plusplan", "chatgptplus", "chatgptplusplan":
		return "plus"
	case "pro", "proplan", "chatgptpro", "chatgptproplan":
		return "pro"
	case "team", "teamplan", "chatgptteam", "chatgptteamplan":
		return "team"
	case "business", "businessplan", "chatgptbusiness", "chatgptbusinessplan":
		return "business"
	case "enterprise", "enterpriseplan", "chatgptenterprise", "chatgptenterpriseplan":
		return "enterprise"
	}
	return lowered
}

// FetchSubscription reads the plan and its expiry for a Codex access token.
//
// Header parity with Fetch: the endpoint authenticates on the bearer alone.
func FetchSubscription(ctx context.Context, client *http.Client, accessToken, accountID string) (*Subscription, error) {
	if strings.TrimSpace(accessToken) == "" {
		return nil, fmt.Errorf("codexquota.FetchSubscription: missing access token")
	}
	if client == nil {
		client = defaultClient
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, SubscriptionURL, nil)
	if err != nil {
		return nil, fmt.Errorf("codexquota.FetchSubscription: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		if proxyRefused(err) {
			return nil, &ProxyRefusedError{URL: SubscriptionURL, Err: err}
		}
		return nil, fmt.Errorf("codexquota.FetchSubscription: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("codexquota.FetchSubscription: read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &StatusError{Status: resp.StatusCode}
	}
	// The account id is a query, not a header: the response is a multi-workspace
	// map, so the caller's own id has to choose the record after decoding.
	return ParseSubscription(body, accountID)
}

// firstNonEmpty returns the first value that is not blank after trimming, so a
// record carrying both spellings of a field reads as one value.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
