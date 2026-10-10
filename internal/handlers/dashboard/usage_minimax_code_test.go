package dashboard

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

// errNotMiniMaxCode is an ordinary failure, used to prove the refused-sign-in
// wording is reserved for the refusal.
var errNotMiniMaxCode = errors.New("account: HTTP 502")

// The account API rejects a request whose `yy` signature does not match the
// path, query, body and clock it was built for, so the signature is checked
// here rather than only exercised live.
func TestSignedMiniMaxCodeRequest_Signature(t *testing.T) {
	site := miniMaxCodeSites["minimax-code"]
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	access := "at"

	rawURL, headers, method, payload := signedMiniMaxCodeRequest(site, "/v1/api/user/info", access, "42", nil, now)
	if method != "GET" {
		t.Errorf("method = %q, want GET for a bodyless call", method)
	}
	if payload != nil {
		t.Errorf("payload = %q, want none for a bodyless call", payload)
	}
	if !strings.HasPrefix(rawURL, site.agent+"/v1/api/user/info?") {
		t.Errorf("url = %q, want the signed account host", rawURL)
	}
	if headers["Authorization"] != "Bearer "+access {
		t.Errorf("Authorization = %q, want the bearer token", headers["Authorization"])
	}

	// The query has to carry mcode's device fields, or the gateway treats the
	// caller as an unknown client.
	for _, field := range []string{"client", "device_platform", "browser_name", "user_id", "app_id", "biz_id", "version_code"} {
		if !strings.Contains(rawURL, field+"=") {
			t.Errorf("query is missing %s: %s", field, rawURL)
		}
	}
	if !strings.Contains(rawURL, "user_id=42") {
		t.Errorf("query does not carry the user id: %s", rawURL)
	}

	// Recompute the signature from the pieces the request itself states; a
	// mismatch means the header and the query disagree.
	ts := headers["x-timestamp"]
	if ts != strconv.FormatInt(now.Unix(), 10) {
		t.Errorf("x-timestamp = %q, want the unix second of now", ts)
	}
	wantSig := miniMaxCodeMD5(ts + miniMaxCodeSignSecret)
	if headers["x-signature"] != wantSig {
		t.Errorf("x-signature = %q, want %q for a bodyless call", headers["x-signature"], wantSig)
	}
	if len(headers["yy"]) != 32 {
		t.Errorf("yy is %d chars, want a 32-char md5 hex digest", len(headers["yy"]))
	}
}

// A POST signs its body in: dropping it from x-signature is what the gateway
// rejects with 401 on the membership call.
func TestSignedMiniMaxCodeRequest_SignsThePOSTBody(t *testing.T) {
	site := miniMaxCodeSites["minimax-code-global"]
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	_, headers, method, payload := signedMiniMaxCodeRequest(site,
		"/matrix/api/v1/commerce/get_membership_info", "at", "42", map[string]any{"workspace_id": float64(7)}, now)

	if method != "POST" {
		t.Errorf("method = %q, want POST", method)
	}
	if string(payload) != `{"workspace_id":7}` {
		t.Errorf("payload = %q, want the marshalled body", payload)
	}
	ts := headers["x-timestamp"]
	want := miniMaxCodeMD5(ts + miniMaxCodeSignSecret + string(payload))
	if headers["x-signature"] != want {
		t.Errorf("x-signature = %q, want %q (ts + secret + body)", headers["x-signature"], want)
	}
}

// The two sites must not share an account host: the China lane's chat host is
// agent.minimax.cn while its account API is agent.minimaxi.com.
func TestMiniMaxCodeSites_AreDistinct(t *testing.T) {
	cn, global := miniMaxCodeSites["minimax-code"], miniMaxCodeSites["minimax-code-global"]
	if cn.agent == global.agent {
		t.Fatal("both mcode sites share one account host; sign-ins are per site")
	}
	if cn.platform == global.platform {
		t.Fatal("both mcode sites share one platform host")
	}
	if !strings.Contains(cn.agent, "minimaxi.com") {
		t.Errorf("the China account host is %q, want the minimaxi.com variant", cn.agent)
	}
}

// The plan windows are what turns "Free" into an actual allowance reading.
func TestParseMiniMaxCodePlanWindows(t *testing.T) {
	body := map[string]any{
		"base_resp": map[string]any{"status_code": float64(0)},
		"model_remains": []any{
			map[string]any{
				"model_name":                         "MiniMax-M3",
				"current_interval_remaining_percent": float64(80),
				"current_interval_status":            float64(1),
				"start_time":                         float64(1767322445),
				"end_time":                           float64(1767326045), // +1h
				"current_weekly_remaining_percent":   float64(0),
				"current_weekly_status":              float64(2), // exhausted, no percentage
				"weekly_start_time":                  float64(1767322445),
				"weekly_end_time":                    float64(1767927245),
			},
			// Fully spent: mcode hides such a row, so it is not reported.
			map[string]any{
				"model_name":                   "MiniMax-M2.7",
				"current_interval_status":      float64(3),
				"current_weekly_status":        float64(3),
				"current_interval_total_count": float64(0),
				"current_weekly_total_count":   float64(0),
			},
		},
	}

	windows, err := parseMiniMaxCodePlanWindows(body)
	if err != nil {
		t.Fatalf("parseMiniMaxCodePlanWindows: %v", err)
	}
	if len(windows) != 2 {
		t.Fatalf("got %d windows, want 2 (the spent row is hidden): %+v", len(windows), windows)
	}
	if windows[0].name != "MiniMax-M3 · 1 hours" {
		t.Errorf("interval window name = %q", windows[0].name)
	}
	if windows[0].remaining != 80 {
		t.Errorf("interval window remaining = %v, want 80", windows[0].remaining)
	}
	if windows[1].name != "MiniMax-M3 · 7 days" {
		t.Errorf("weekly window name = %q", windows[1].name)
	}
	// Status 2 means exhausted with no percentage published: 0% left, not a hole.
	if windows[1].remaining != 0 {
		t.Errorf("exhausted weekly window remaining = %v, want 0", windows[1].remaining)
	}
}

// An answer that carries no plan at all must say so rather than render an empty
// table the user reads as "unlimited".
func TestParseMiniMaxCodePlanWindows_RejectsEmptyAnswer(t *testing.T) {
	if _, err := parseMiniMaxCodePlanWindows(map[string]any{"base_resp": map[string]any{"status_code": float64(0)}}); err == nil {
		t.Fatal("a reply with no plan produced no error")
	}
	if _, err := parseMiniMaxCodePlanWindows(map[string]any{
		"base_resp": map[string]any{"status_code": float64(1001), "status_msg": "not signed in"},
	}); err == nil || !strings.Contains(err.Error(), "not signed in") {
		t.Fatalf("error = %v, want the envelope's own message", err)
	}
}

// The credits row is a balance, not a percentage: nothing is spent against it
// in this reading, and the flag is what tells the UI to render an amount.
func TestCreditsQuota(t *testing.T) {
	q := creditsQuota("1234.5")
	if q["isCreditBalance"] != true {
		t.Errorf("isCreditBalance = %v, want true", q["isCreditBalance"])
	}
	if q["currency"] != "credits" {
		t.Errorf("currency = %v, want credits", q["currency"])
	}
	if q["used"] != float64(0) {
		t.Errorf("used = %v, want 0 (a balance has nothing spent against it here)", q["used"])
	}
	if q["total"] != 1234.5 {
		t.Errorf("total = %v, want the balance 1234.5", q["total"])
	}
}

// A refused sign-in has to reach the route in the wording its auth-expired
// check recognises, or the panel never retries with a fresh token.
func TestMiniMaxCodeFailure_ReportsRefusedSignIn(t *testing.T) {
	res := miniMaxCodeFailure(errMiniMaxCodeRefused)
	if !strings.Contains(res.message, "sign-in expired") {
		t.Errorf("message = %q, want the auth-expired wording", res.message)
	}
	if len(res.quotas) != 0 {
		t.Errorf("quotas = %v, want none on a failed read", res.quotas)
	}

	other := miniMaxCodeFailure(errNotMiniMaxCode)
	if strings.Contains(other.message, "sign-in expired") {
		t.Errorf("message = %q, want an ordinary failure", other.message)
	}
	if other.message != errNotMiniMaxCode.Error() {
		t.Errorf("message = %q, want the underlying error text", other.message)
	}
}

// Plan labels must distinguish a free account from an M Plan subscriber, since
// that is the only thing the header shows when no quota row is available.
func TestMiniMaxCodePlanLabel(t *testing.T) {
	tests := []struct {
		hasPlan bool
		tier    string
		want    string
	}{
		{false, "", "Free"},
		{true, "", "M Plan"},
		{true, "Pro", "M Plan Pro"},
	}
	for _, tt := range tests {
		if got := miniMaxCodePlanLabel(tt.hasPlan, tt.tier); got != tt.want {
			t.Errorf("miniMaxCodePlanLabel(%v, %q) = %q, want %q", tt.hasPlan, tt.tier, got, tt.want)
		}
	}
}
