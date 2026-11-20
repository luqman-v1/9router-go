package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/minimaxcheckin"
	"9router/proxy/internal/proxy/oauth"
)

func writeCheckinJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// withMiniMaxTestSite points the China lane's account host at a test server.
// The real hosts are package-level constants, so a flow test overrides the map
// entry and restores it; no test runs in parallel while it is overridden.
func withMiniMaxTestSite(t *testing.T, origin string) {
	t.Helper()
	old := miniMaxCodeSites["minimax-code"]
	miniMaxCodeSites["minimax-code"] = miniMaxCodeSite{agent: origin, platform: origin, lang: "zh"}
	t.Cleanup(func() { miniMaxCodeSites["minimax-code"] = old })
}

func checkinPanel(statuses []int, todayIdx int, points float64) map[string]any {
	days := make([]any, 7)
	for i := 0; i < 7; i++ {
		d := map[string]any{"day_no": float64(i + 1), "status": float64(statuses[i]), "points": points}
		if i == todayIdx {
			d["is_today"] = true
		}
		days[i] = d
	}
	return map[string]any{"data": map[string]any{"days": days}}
}

func TestEvaluateMiniMaxCheckinPanel(t *testing.T) {
	cases := []struct {
		name      string
		env       map[string]any
		outcome   minimaxcheckin.Outcome
		credit    float64
		streak    int
		needClaim bool
		wantErr   bool
	}{
		{"claimed_today", checkinPanel([]int{3, 3, 3, 1, 1, 1, 1}, 2, 10), minimaxcheckin.OutcomeDone, 10, 3, false, false},
		{"claimable_today", checkinPanel([]int{3, 3, 2, 1, 1, 1, 1}, 2, 10), "", 0, 2, true, false},
		{"ineligible", checkinPanel([]int{3, 3, 4, 1, 1, 1, 1}, 2, 10), minimaxcheckin.OutcomeIneligible, 0, 0, false, false},
		{"not_started", checkinPanel([]int{3, 3, 1, 1, 1, 1, 1}, 2, 10), minimaxcheckin.OutcomeInactive, 0, 0, false, false},
		{"no_today", checkinPanel([]int{3, 3, 3, 1, 1, 1, 1}, -1, 10), minimaxcheckin.OutcomeInactive, 0, 0, false, false},
		{"no_panel", map[string]any{"data": map[string]any{}}, "", 0, 0, false, true},
		{"six_days", map[string]any{"data": map[string]any{"days": []any{
			map[string]any{"day_no": float64(1), "status": float64(3)},
		}}}, "", 0, 0, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome, credit, streak, needClaim, err := evaluateMiniMaxCheckinPanel(tc.env)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if outcome != tc.outcome || credit != tc.credit || streak != tc.streak || needClaim != tc.needClaim {
				t.Errorf("got (%q,%v,%d,%v), want (%q,%v,%d,%v)", outcome, credit, streak, needClaim, tc.outcome, tc.credit, tc.streak, tc.needClaim)
			}
		})
	}
}

func TestEvaluateMiniMaxCheckinPanel_Malformed(t *testing.T) {
	// Two days marked today.
	env := checkinPanel([]int{3, 3, 2, 1, 1, 1, 1}, 2, 10)
	days := env["data"].(map[string]any)["days"].([]any)
	days[3].(map[string]any)["is_today"] = true
	if _, _, _, _, err := evaluateMiniMaxCheckinPanel(env); err == nil {
		t.Error("two is_today days should be malformed")
	}
	// Two claimable days.
	env = checkinPanel([]int{3, 2, 2, 1, 1, 1, 1}, 1, 10)
	if _, _, _, _, err := evaluateMiniMaxCheckinPanel(env); err == nil {
		t.Error("two claimable days should be malformed")
	}
	// day_no out of sequence.
	env = checkinPanel([]int{3, 3, 2, 1, 1, 1, 1}, 2, 10)
	env["data"].(map[string]any)["days"].([]any)[0].(map[string]any)["day_no"] = float64(9)
	if _, _, _, _, err := evaluateMiniMaxCheckinPanel(env); err == nil {
		t.Error("day_no 9 in slot 1 should be malformed")
	}
}

func TestEvaluateMiniMaxCheckinClaim(t *testing.T) {
	claim := func(result, points float64) map[string]any {
		return map[string]any{"data": map[string]any{"claim_result": result, "points": points}}
	}
	outcome, credit, streak, err := evaluateMiniMaxCheckinClaim(claim(1, 20), 2)
	if err != nil || outcome != minimaxcheckin.OutcomeClaimed || credit != 20 || streak != 3 {
		t.Errorf("claim_result 1: got (%q,%v,%d,%v)", outcome, credit, streak, err)
	}
	outcome, _, _, err = evaluateMiniMaxCheckinClaim(claim(2, 10), 0)
	if err != nil || outcome != minimaxcheckin.OutcomeDone {
		t.Errorf("claim_result 2: got (%q,%v)", outcome, err)
	}
	if _, _, _, err = evaluateMiniMaxCheckinClaim(claim(9, 0), 0); err == nil {
		t.Error("unknown claim_result should fail")
	}
}

func TestSignedMiniMaxCodeRequest_CheckinQuery(t *testing.T) {
	site := miniMaxCodeSites["minimax-code"]
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	rawURL, headers, method, payload := signedMiniMaxCodeRequestQuery(site, miniMaxCheckinClaimPath, "at", "42", map[string]any{}, miniMaxCheckinQuery, now)
	if method != http.MethodPost {
		t.Errorf("method = %q, want POST", method)
	}
	for _, want := range []string{"device_platform=web", "is_desktop=1", "desktop_version=0.6.12", "user_id=42"} {
		if !strings.Contains(rawURL, want) {
			t.Errorf("query missing %s: %s", want, rawURL)
		}
	}
	if strings.Contains(rawURL, "device_platform=mcode") {
		t.Errorf("check-in must ask as the desktop client, not mcode: %s", rawURL)
	}
	ts := headers["x-timestamp"]
	want := miniMaxCodeMD5(ts + miniMaxCodeSignSecret + string(payload))
	if headers["x-signature"] != want {
		t.Errorf("x-signature = %q, want %q", headers["x-signature"], want)
	}
	// The default mcode signature must differ from the desktop one.
	_, defaultHeaders, _, _ := signedMiniMaxCodeRequest(site, miniMaxCheckinClaimPath, "at", "42", map[string]any{}, now)
	if defaultHeaders["yy"] == headers["yy"] {
		t.Error("check-in query should change the yy signature")
	}
}

func TestCheckMiniMaxCode_ClaimsToday(t *testing.T) {
	var claimAuth, claimMethod, claimQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/signin/status"):
			writeCheckinJSON(w, checkinPanel([]int{3, 3, 2, 1, 1, 1, 1}, 2, 10))
		case strings.HasSuffix(r.URL.Path, "/signin/claim"):
			claimAuth = r.Header.Get("Authorization")
			claimMethod = r.Method
			claimQuery = r.URL.RawQuery
			writeCheckinJSON(w, map[string]any{"data": map[string]any{"claim_result": float64(1), "points": float64(20)}})
		default:
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	defer srv.Close()
	withMiniMaxTestSite(t, srv.URL)

	h := &DashboardHandler{}
	got := h.CheckMiniMaxCode(context.Background(), minimaxcheckin.Account{
		Provider: "minimax-code", ID: "c1", Name: "acct", Token: "at", UserID: "42",
	})
	if got.Outcome != minimaxcheckin.OutcomeClaimed || got.Credit != 20 || got.Streak != 3 {
		t.Fatalf("got %+v, want claimed credit 20 streak 3", got)
	}
	if claimAuth != "Bearer at" || claimMethod != http.MethodPost {
		t.Errorf("claim sent %q %q", claimMethod, claimAuth)
	}
	if !strings.Contains(claimQuery, "device_platform=web") {
		t.Errorf("claim query = %q", claimQuery)
	}
}

func TestCheckMiniMaxCode_DoneAndInactive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/signin/status") {
			writeCheckinJSON(w, checkinPanel([]int{3, 3, 3, 1, 1, 1, 1}, 2, 10))
			return
		}
		http.Error(w, "unexpected", http.StatusNotFound)
	}))
	defer srv.Close()
	withMiniMaxTestSite(t, srv.URL)
	h := &DashboardHandler{}
	got := h.CheckMiniMaxCode(context.Background(), minimaxcheckin.Account{Provider: "minimax-code", ID: "c1", Name: "a", Token: "at", UserID: "42"})
	if got.Outcome != minimaxcheckin.OutcomeDone || got.Streak != 3 {
		t.Fatalf("got %+v, want done streak 3", got)
	}
}

func TestCheckMiniMaxCode_RefreshesOn401(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	data := `{"accessToken":"old","refreshToken":"rt-unique","providerSpecificData":{"realUserID":"42"}}`
	if err := repo.CreateProviderConnectionFull("c1", "minimax-code", "oauth", "acct", nil, data); err != nil {
		t.Fatalf("create connection: %v", err)
	}

	var statusAuths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/v1/api/user/info"):
			writeCheckinJSON(w, map[string]any{"data": map[string]any{"userInfo": map[string]any{"realUserID": "42"}}})
		case strings.HasSuffix(r.URL.Path, "/signin/status"):
			auth := r.Header.Get("Authorization")
			statusAuths = append(statusAuths, auth)
			if auth == "Bearer old" {
				http.Error(w, `{"error":"invalid access token"}`, http.StatusUnauthorized)
				return
			}
			writeCheckinJSON(w, checkinPanel([]int{3, 2, 1, 1, 1, 1, 1}, 1, 10))
		case strings.HasSuffix(r.URL.Path, "/signin/claim"):
			writeCheckinJSON(w, map[string]any{"data": map[string]any{"claim_result": float64(1), "points": float64(20)}})
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	defer srv.Close()
	withMiniMaxTestSite(t, srv.URL)

	old := oauth.Get("minimax-code")
	oauth.Register("minimax-code", func(ctx context.Context, p *oauth.Params) (*oauth.TokenResult, error) {
		return &oauth.TokenResult{AccessToken: "new", RefreshToken: "rotated", ExpiresIn: 3600}, nil
	})
	t.Cleanup(func() {
		if old != nil {
			oauth.Register("minimax-code", old)
		} else {
			oauth.Unregister("minimax-code")
		}
	})

	h := &DashboardHandler{Repo: repo}
	got := h.CheckMiniMaxCode(context.Background(), minimaxcheckin.Account{
		Provider: "minimax-code", ID: "c1", Name: "acct", Token: "old", UserID: "42",
	})
	if got.Outcome != minimaxcheckin.OutcomeClaimed || got.Credit != 20 || got.Streak != 2 {
		t.Fatalf("got %+v, want claimed after refresh", got)
	}
	if len(statusAuths) != 2 || statusAuths[0] != "Bearer old" || statusAuths[1] != "Bearer new" {
		t.Errorf("status auths = %v, want old then new", statusAuths)
	}
	conn, err := repo.GetProviderConnectionByID("c1")
	if err != nil || conn == nil {
		t.Fatalf("read back connection: %v", err)
	}
	if !strings.Contains(conn.Data, `"accessToken":"new"`) || !strings.Contains(conn.Data, `"refreshToken":"rotated"`) {
		t.Errorf("rotated tokens not persisted: %s", conn.Data)
	}
}

func TestCheckMiniMaxCode_LeaseHasNoToken(t *testing.T) {
	h := &DashboardHandler{}
	got := h.CheckMiniMaxCode(context.Background(), minimaxcheckin.Account{Provider: "minimax-code", ID: "c1", Name: "a"})
	if got.Outcome != minimaxcheckin.OutcomeFailed || got.Message != "not signed in" {
		t.Fatalf("got %+v, want failed not signed in", got)
	}
}

// A 403 is a refusal but not an expired token: magpie's hook renews only on a
// 401, so the single-use refresh token must not be spent on a 403.
func TestCheckMiniMaxCode_DoesNotRefreshOn403(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	data := `{"accessToken":"old","refreshToken":"rt-403","providerSpecificData":{"realUserID":"42"}}`
	if err := repo.CreateProviderConnectionFull("c1", "minimax-code", "oauth", "acct", nil, data); err != nil {
		t.Fatalf("create connection: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/signin/status") {
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
			return
		}
		http.Error(w, "unexpected", http.StatusNotFound)
	}))
	defer srv.Close()
	withMiniMaxTestSite(t, srv.URL)

	refreshed := false
	old := oauth.Get("minimax-code")
	oauth.Register("minimax-code", func(ctx context.Context, p *oauth.Params) (*oauth.TokenResult, error) {
		refreshed = true
		return &oauth.TokenResult{AccessToken: "new"}, nil
	})
	t.Cleanup(func() {
		if old != nil {
			oauth.Register("minimax-code", old)
		} else {
			oauth.Unregister("minimax-code")
		}
	})

	h := &DashboardHandler{Repo: repo}
	got := h.CheckMiniMaxCode(context.Background(), minimaxcheckin.Account{
		Provider: "minimax-code", ID: "c1", Name: "acct", Token: "old", UserID: "42",
	})
	if got.Outcome != minimaxcheckin.OutcomeFailed {
		t.Fatalf("got %+v, want failed on 403", got)
	}
	if refreshed {
		t.Error("a 403 must not spend the refresh token")
	}
}

// A business refusal inside a 200 (statusInfo 1000048) is not an expired token
// either, so the check-in hook fails without renewing.
func TestCheckMiniMaxCode_DoesNotRefreshOnBusinessRefusal(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	data := `{"accessToken":"old","refreshToken":"rt-biz","providerSpecificData":{"realUserID":"42"}}`
	if err := repo.CreateProviderConnectionFull("c1", "minimax-code", "oauth", "acct", nil, data); err != nil {
		t.Fatalf("create connection: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/signin/status") {
			writeCheckinJSON(w, map[string]any{"statusInfo": map[string]any{"code": float64(1000048), "msg": "sign-in expired"}})
			return
		}
		http.Error(w, "unexpected", http.StatusNotFound)
	}))
	defer srv.Close()
	withMiniMaxTestSite(t, srv.URL)

	refreshed := false
	old := oauth.Get("minimax-code")
	oauth.Register("minimax-code", func(ctx context.Context, p *oauth.Params) (*oauth.TokenResult, error) {
		refreshed = true
		return &oauth.TokenResult{AccessToken: "new"}, nil
	})
	t.Cleanup(func() {
		if old != nil {
			oauth.Register("minimax-code", old)
		} else {
			oauth.Unregister("minimax-code")
		}
	})

	h := &DashboardHandler{Repo: repo}
	got := h.CheckMiniMaxCode(context.Background(), minimaxcheckin.Account{
		Provider: "minimax-code", ID: "c1", Name: "acct", Token: "old", UserID: "42",
	})
	if got.Outcome != minimaxcheckin.OutcomeFailed {
		t.Fatalf("got %+v, want failed on business refusal", got)
	}
	if refreshed {
		t.Error("a business refusal must not spend the refresh token")
	}
}

// The typed refusal keeps the quota route's accountRefused contract (401/403
// both refuse) while letting the check-in hook tell a 401 from a 403.
func TestMiniMaxCodeRefusalError(t *testing.T) {
	e401 := &miniMaxCodeRefusalError{status: http.StatusUnauthorized, what: "check-in"}
	e403 := &miniMaxCodeRefusalError{status: http.StatusForbidden, what: "check-in"}
	if !accountRefused(e401) || !accountRefused(e403) {
		t.Error("both 401 and 403 must satisfy accountRefused")
	}
	if !errors.Is(e401, errMiniMaxCodeUnauthorized) {
		t.Error("401 must satisfy errMiniMaxCodeUnauthorized")
	}
	if errors.Is(e403, errMiniMaxCodeUnauthorized) {
		t.Error("403 must not satisfy errMiniMaxCodeUnauthorized")
	}
}
