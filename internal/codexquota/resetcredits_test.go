package codexquota

import (
	"context"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func mustJSON(t *testing.T, raw string) any {
	t.Helper()
	var payload any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return payload
}

var refNow = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

// A credit the user cannot redeem must not be offered, and the one that frees
// the quota soonest must come first.
func TestParseAvailableResetCredits_FiltersAndOrders(t *testing.T) {
	payload := mustJSON(t, `{"credits":[
		{"credit_id":"soon","expires_at":"2026-09-28T13:00:00Z"},
		{"credit_id":"gone-expired","expires_at":"2026-09-28T11:00:00Z"},
		{"credit_id":"consumed","status":"consumed"},
		{"credit_id":"already-redeemed","redeemed":true},
		{"credit_id":"not-available","available":false},
		{"credit_id":"later","expires_at":"2026-09-29T09:00:00Z"},
		{"credit_id":"no-expiry"}
	]}`)

	list, ok := ParseAvailableResetCredits(payload, refNow)
	if !ok {
		t.Fatal("expected the payload to parse")
	}
	var got []string
	for _, credit := range list.Credits {
		got = append(got, credit.ID)
	}
	want := []string{"soon", "later", "no-expiry"}
	if len(got) != len(want) {
		t.Fatalf("credits = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("credit[%d] = %q, want %q (full order: %v)", i, got[i], want[i], got)
		}
	}
}

// Codex has returned the list under several shapes; all must be accepted
// rather than the port pinning itself to one.
func TestParseAvailableResetCredits_AcceptedPayloadShapes(t *testing.T) {
	shapes := map[string]string{
		"bare array":               `[{"credit_id":"a"}]`,
		"credits":                  `{"credits":[{"credit_id":"a"}]}`,
		"reset_credits":            `{"reset_credits":[{"credit_id":"a"}]}`,
		"resetCredits":             `{"resetCredits":[{"credit_id":"a"}]}`,
		"rate_limit_reset_credits": `{"rate_limit_reset_credits":[{"credit_id":"a"}]}`,
		"rateLimitResetCredits":    `{"rateLimitResetCredits":[{"credit_id":"a"}]}`,
		"items":                    `{"items":[{"credit_id":"a"}]}`,
		"data":                     `{"data":[{"credit_id":"a"}]}`,
	}
	for name, raw := range shapes {
		t.Run(name, func(t *testing.T) {
			list, _ := ParseAvailableResetCredits(mustJSON(t, raw), refNow)
			if len(list.Credits) != 1 || list.Credits[0].ID != "a" {
				t.Errorf("credits = %+v, want one credit with id a", list.Credits)
			}
		})
	}
}

func TestParseAvailableResetCredits_AvailableCount(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int
	}{
		{"reported as number", `{"credits":[{"credit_id":"a"}],"available_count":7}`, 7},
		{"reported as string", `{"credits":[{"credit_id":"a"}],"availableCount":"4"}`, 4},
		{"absent falls back to the credit count", `{"credits":[{"credit_id":"a"},{"credit_id":"b"}]}`, 2},
		{"negative is clamped", `{"credits":[{"credit_id":"a"}],"available_count":-3}`, 0},
		{"unusable credits are not counted", `{"credits":[{"credit_id":"a","status":"used"}],"available_count":5}`, 5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			list, _ := ParseAvailableResetCredits(mustJSON(t, tc.raw), refNow)
			if list.AvailableCount != tc.want {
				t.Errorf("AvailableCount = %d, want %d", list.AvailableCount, tc.want)
			}
		})
	}
}

// A credit with no id is unaddressable, so it must never be offered.
func TestParseAvailableResetCredits_SkipsCreditWithoutID(t *testing.T) {
	list, _ := ParseAvailableResetCredits(mustJSON(t, `[{"title":"nameless"},{"credit_id":"real"}]`), refNow)
	if len(list.Credits) != 1 || list.Credits[0].ID != "real" {
		t.Errorf("credits = %+v, want only the addressed one", list.Credits)
	}
}

func TestSelectResetCredit(t *testing.T) {
	list := ResetCreditList{
		Credits:        []ResetCredit{{ID: "soon"}, {ID: "later"}},
		AvailableCount: 2,
	}

	t.Run("defaults to the soonest expiry", func(t *testing.T) {
		credit, err := SelectResetCredit(list, "")
		if err != nil || credit.ID != "soon" {
			t.Errorf("SelectResetCredit = %q, %v; want soon", credit.ID, err)
		}
	})

	t.Run("honours an explicit choice", func(t *testing.T) {
		credit, err := SelectResetCredit(list, "later")
		if err != nil || credit.ID != "later" {
			t.Errorf("SelectResetCredit = %q, %v; want later", credit.ID, err)
		}
	})

	t.Run("refuses a credit that is no longer listed", func(t *testing.T) {
		_, err := SelectResetCredit(list, "vanished")
		assertResetError(t, err, 409, "selected_credit_unavailable")
	})

	t.Run("refuses when nothing is available", func(t *testing.T) {
		_, err := SelectResetCredit(ResetCreditList{}, "")
		assertResetError(t, err, 409, "no_credit")
	})
}

func assertResetError(t *testing.T, err error, status int, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a %s error, got nil", code)
	}
	typed, ok := err.(*ResetCreditError)
	if !ok {
		t.Fatalf("error = %T(%v), want *ResetCreditError", err, err)
	}
	if typed.Status != status || typed.Code != code {
		t.Errorf("error = %d/%s, want %d/%s", typed.Status, typed.Code, status, code)
	}
}

// "already redeemed" is the state the user was trying to reach, so it is a
// success. "no credit" and "nothing to reset" must stay distinguishable.
func TestParseConsumeOutcome(t *testing.T) {
	tests := []struct {
		name    string
		payload any
		want    string
		errCode string
		errStat int
	}{
		{name: "reset", payload: mustJSON(t, `{"outcome":"reset"}`), want: OutcomeReset},
		{name: "upper case", payload: mustJSON(t, `{"outcome":"RESET"}`), want: OutcomeReset},
		{name: "bare string", payload: "reset", want: OutcomeReset},
		{name: "outcome under code", payload: mustJSON(t, `{"code":"reset"}`), want: OutcomeReset},
		{name: "already redeemed snake", payload: mustJSON(t, `{"outcome":"already_redeemed"}`), want: OutcomeAlreadyRedeemed},
		{name: "already redeemed camel", payload: mustJSON(t, `{"outcome":"alreadyRedeemed"}`), want: OutcomeAlreadyRedeemed},
		{name: "no credit", payload: mustJSON(t, `{"outcome":"no_credit"}`), errCode: "no_credit", errStat: 409},
		{name: "no credits plural", payload: mustJSON(t, `{"outcome":"no_credits"}`), errCode: "no_credit", errStat: 409},
		{name: "nothing to reset", payload: mustJSON(t, `{"outcome":"nothing_to_reset"}`), errCode: "nothing_to_reset", errStat: 409},
		{name: "unrecognised", payload: mustJSON(t, `{"outcome":"weird"}`), errCode: "unknown_reset_credit_response", errStat: 502},
		{name: "empty", payload: map[string]any{}, errCode: "unknown_reset_credit_response", errStat: 502},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseConsumeOutcome(tc.payload)
			if tc.errCode != "" {
				assertResetError(t, err, tc.errStat, tc.errCode)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("outcome = %q, want %q", got, tc.want)
			}
		})
	}
}

// A generic upstream failure must not be relabelled as "no credit": the
// dashboard would then tell the user to do something that cannot help.
func TestKnownConsumeError_IgnoresUnrelatedFailures(t *testing.T) {
	if err := knownConsumeError(mustJSON(t, `{"message":"upstream exploded"}`)); err != nil {
		t.Errorf("knownConsumeError = %v, want nil", err)
	}
	assertResetError(t, knownConsumeError(mustJSON(t, `{"outcome":"no_credit"}`)), 409, "no_credit")
	assertResetError(t, knownConsumeError(mustJSON(t, `{"outcome":"nothing_to_reset"}`)), 409, "nothing_to_reset")
}

// A missing token is refused before any network call, so the handler can
// answer with the right code instead of a confusing 401 from the wire.
func TestResetCredits_MissingTokenIsRefusedLocally(t *testing.T) {
	_, err := ListResetCredits(t.Context(), nil, "", "acct", nil, refNow)
	assertResetError(t, err, 401, "codex_access_token_missing")

	_, _, err = ConsumeResetCredit(t.Context(), nil, "", "acct", "idem-1", "", nil, refNow)
	assertResetError(t, err, 401, "codex_access_token_missing")
}

// The idempotency key is what makes a retried consume safe, so it is required
// rather than defaulted.
func TestConsumeResetCredit_RequiresIdempotencyKey(t *testing.T) {
	_, _, err := ConsumeResetCredit(t.Context(), nil, "token", "acct", "", "", nil, refNow)
	assertResetError(t, err, 400, "idempotency_key_required")
}

// pointResetCreditURLs redirects the wham endpoints at a test server.
func pointResetCreditURLs(t *testing.T, base string) {
	t.Helper()
	oldCredits, oldConsume := ResetCreditsURL, ConsumeResetCreditsURL
	ResetCreditsURL = base + "/credits"
	ConsumeResetCreditsURL = base + "/credits/consume"
	t.Cleanup(func() {
		ResetCreditsURL, ConsumeResetCreditsURL = oldCredits, oldConsume
	})
}

func TestListResetCredits_SendsAccountIdentity(t *testing.T) {
	var seenAuth, seenAccount string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		seenAccount = r.Header.Get("chatgpt-account-id")
		w.Write([]byte(`{"credits":[{"credit_id":"c1","expires_at":"2026-09-30T00:00:00Z"}]}`))
	}))
	defer srv.Close()
	pointResetCreditURLs(t, srv.URL)

	list, err := ListResetCredits(t.Context(), srv.Client(), "tok-abc", "acct-xyz", nil, refNow)
	if err != nil {
		t.Fatalf("ListResetCredits: %v", err)
	}
	if len(list.Credits) != 1 || list.Credits[0].ID != "c1" {
		t.Errorf("credits = %+v, want one credit c1", list.Credits)
	}
	if seenAuth != "Bearer tok-abc" {
		t.Errorf("Authorization = %q, want Bearer tok-abc", seenAuth)
	}
	// Without the account id the wham endpoint scopes the credits to the
	// wrong Codex account.
	if seenAccount != "acct-xyz" {
		t.Errorf("chatgpt-account-id = %q, want acct-xyz", seenAccount)
	}
}

// Codex rotates access tokens, so a 401 must trigger one refresh and one
// retry rather than surfacing as "reset credits are broken".
func TestListResetCredits_RefreshesOnceOn401(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") == "Bearer stale" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"credits":[{"credit_id":"c1"}]}`))
	}))
	defer srv.Close()
	pointResetCreditURLs(t, srv.URL)

	refreshed := false
	refresh := func(context.Context) (string, error) {
		refreshed = true
		return "fresh", nil
	}
	list, err := ListResetCredits(t.Context(), srv.Client(), "stale", "", refresh, refNow)
	if err != nil {
		t.Fatalf("ListResetCredits: %v", err)
	}
	if !refreshed {
		t.Error("the token should have been refreshed after a 401")
	}
	if calls != 2 {
		t.Errorf("upstream calls = %d, want 2", calls)
	}
	if len(list.Credits) != 1 {
		t.Errorf("credits = %+v, want the refreshed list", list.Credits)
	}
}

func TestListResetCredits_DoesNotRetryWithoutARefresher(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	pointResetCreditURLs(t, srv.URL)

	_, err := ListResetCredits(t.Context(), srv.Client(), "tok", "", nil, refNow)
	assertResetError(t, err, http.StatusForbidden, "codex_reset_credit_upstream_error")
	if calls != 1 {
		t.Errorf("upstream calls = %d, want 1 (no refresher to retry with)", calls)
	}
}

// The consume must carry the chosen credit id and the idempotency key: the
// key is what makes the post-retry call safe, and the id is what decides
// which credit the user actually spent.
func TestConsumeResetCredit_SendsChosenCreditAndIdempotencyKey(t *testing.T) {
	var consumeBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/credits/consume" {
			raw, _ := io.ReadAll(r.Body)
			json.Unmarshal(raw, &consumeBody)
			w.Write([]byte(`{"outcome":"reset"}`))
			return
		}
		w.Write([]byte(`{"credits":[{"credit_id":"soon"},{"credit_id":"later"}]}`))
	}))
	defer srv.Close()
	pointResetCreditURLs(t, srv.URL)

	outcome, credit, err := ConsumeResetCredit(t.Context(), srv.Client(), "tok", "acct", "idem-1", "later", nil, refNow)
	if err != nil {
		t.Fatalf("ConsumeResetCredit: %v", err)
	}
	if outcome != OutcomeReset {
		t.Errorf("outcome = %q, want %q", outcome, OutcomeReset)
	}
	if credit.ID != "later" {
		t.Errorf("redeemed credit = %q, want the requested one (later)", credit.ID)
	}
	if consumeBody["credit_id"] != "later" {
		t.Errorf("credit_id = %q, want later", consumeBody["credit_id"])
	}
	if consumeBody["redeem_request_id"] != "idem-1" {
		t.Errorf("redeem_request_id = %q, want idem-1", consumeBody["redeem_request_id"])
	}
}

func TestConsumeResetCredit_AlreadyRedeemedIsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/credits/consume" {
			w.Write([]byte(`{"outcome":"already_redeemed"}`))
			return
		}
		w.Write([]byte(`{"credits":[{"credit_id":"c1"}]}`))
	}))
	defer srv.Close()
	pointResetCreditURLs(t, srv.URL)

	outcome, _, err := ConsumeResetCredit(t.Context(), srv.Client(), "tok", "", "idem-1", "", nil, refNow)
	if err != nil {
		t.Fatalf("already_redeemed should be a success, got %v", err)
	}
	if outcome != OutcomeAlreadyRedeemed {
		t.Errorf("outcome = %q, want %q", outcome, OutcomeAlreadyRedeemed)
	}
}

// With nothing to redeem the user must be told that, not get a generic
// upstream failure.
func TestConsumeResetCredit_NoCreditsIsTyped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"credits":[]}`))
	}))
	defer srv.Close()
	pointResetCreditURLs(t, srv.URL)

	_, _, err := ConsumeResetCredit(t.Context(), srv.Client(), "tok", "", "idem-1", "", nil, refNow)
	assertResetError(t, err, 409, "no_credit")
}

// An upstream 409 body still carries the meaningful code, so it wins over the
// generic status mapping.
func TestConsumeResetCredit_SurfacesTypedUpstreamRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/credits/consume" {
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"outcome":"nothing_to_reset"}`))
			return
		}
		w.Write([]byte(`{"credits":[{"credit_id":"c1"}]}`))
	}))
	defer srv.Close()
	pointResetCreditURLs(t, srv.URL)

	_, _, err := ConsumeResetCredit(t.Context(), srv.Client(), "tok", "", "idem-1", "", nil, refNow)
	assertResetError(t, err, 409, "nothing_to_reset")
}
