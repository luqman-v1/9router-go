package dashboard

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/config"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/minimaxcheckin"
)

// MiniMax Code daily check-in (每日签到), ported from magpie-community/plugins
// packages/minimax index.mjs:861-905 (checkinOf) and 1201-1228 (the checkin
// hook). The panel lists the week's seven days — status 1 not started, 2
// claimable, 3 claimed, 4 not available — and the claim answers 1 claimed now
// or 2 already today. Both calls use the same signed account client as the
// rest of the account API, asked as MiniMax Code's own /checkin asks them: as
// the account (its realUserID, looked up first when the sign-in didn't keep
// it), from the desktop client, on the account host. A 401 is answered as
// MiniMax Code answers it: the sign-in renewed, the user id read again for that
// token, and the check-in asked once more.

const (
	miniMaxCheckinStatusPath = "/minimax-cloud/api/v1/signin/status"
	miniMaxCheckinClaimPath  = "/minimax-cloud/api/v1/signin/claim"
)

// miniMaxCheckinQuery is magpie's CHECKIN_QUERY: the desktop client's identity
// merged over the mcode defaults in signedMiniMaxCodeRequestQuery.
var miniMaxCheckinQuery = url.Values{
	"device_platform": {"web"},
	"is_desktop":      {"1"},
	"desktop_version": {"0.6.12"},
}

// CheckMiniMaxCode implements minimaxcheckin.Checker. It reads the panel and
// claims today when claimable, refreshing the sign-in once on a refusal.
func (h *DashboardHandler) CheckMiniMaxCode(ctx context.Context, a minimaxcheckin.Account) minimaxcheckin.Result {
	now := time.Now()
	failed := func(msg string) minimaxcheckin.Result {
		return minimaxcheckin.Result{
			Outcome: minimaxcheckin.OutcomeFailed, Message: msg,
			Provider: a.Provider, Account: a.Name, AccountID: a.ID, CheckedAt: now,
		}
	}
	site, ok := miniMaxCodeSites[a.Provider]
	if !ok {
		return failed("unknown MiniMax Code site")
	}
	if strings.TrimSpace(a.Token) == "" {
		return failed("not signed in")
	}

	attempt := func(access, userID string) (minimaxcheckin.Result, error) {
		if strings.TrimSpace(userID) == "" {
			id, err := miniMaxCodeIdentityOf(ctx, site, access)
			if err != nil {
				return minimaxcheckin.Result{}, err
			}
			userID = id.realUserID
		}
		env, err := miniMaxCodeAccountCallQuery(ctx, site, miniMaxCheckinStatusPath, access, userID, nil, miniMaxCheckinQuery, "check-in")
		if err != nil {
			return minimaxcheckin.Result{}, err
		}
		outcome, credit, streak, needClaim, err := evaluateMiniMaxCheckinPanel(env)
		if err != nil {
			return minimaxcheckin.Result{}, err
		}
		if !needClaim {
			return minimaxcheckin.Result{Outcome: outcome, Credit: credit, Streak: streak}, nil
		}
		claimEnv, err := miniMaxCodeAccountCallQuery(ctx, site, miniMaxCheckinClaimPath, access, userID, map[string]any{}, miniMaxCheckinQuery, "check-in")
		if err != nil {
			return minimaxcheckin.Result{}, err
		}
		outcome, credit, streak, err = evaluateMiniMaxCheckinClaim(claimEnv, streak)
		if err != nil {
			return minimaxcheckin.Result{}, err
		}
		return minimaxcheckin.Result{Outcome: outcome, Credit: credit, Streak: streak}, nil
	}

	out, err := attempt(a.Token, a.UserID)
	if err != nil && errors.Is(err, errMiniMaxCodeUnauthorized) {
		// MiniMax Code's /checkin on a 401: renew the sign-in, read the user id
		// again for the new token, and ask once more. The shared usage refresh
		// persists the rotated single-use token and parks a dead grant. A 403
		// or business refusal is not an expired token, so it is not renewed
		// (magpie plugins#765 renews only on e.status === 401).
		newToken, refreshErr := h.refreshMiniMaxCheckinToken(ctx, a)
		if newToken != "" {
			out, err = attempt(newToken, "")
		} else if refreshErr != nil {
			err = refreshErr
		}
	}
	if err != nil {
		return failed(err.Error())
	}
	out.Provider, out.Account, out.AccountID, out.CheckedAt = a.Provider, a.Name, a.ID, now
	return out
}

// refreshMiniMaxCheckinToken spends one guarded refresh for the account and
// returns the new access token, or "" when there was nothing to refresh with
// or the grant is dead. The rotated credentials are persisted by the shared
// usage refresh, so the next read uses the token MiniMax last handed out.
func (h *DashboardHandler) refreshMiniMaxCheckinToken(ctx context.Context, a minimaxcheckin.Account) (string, error) {
	if h.Repo == nil || a.ID == "" {
		return "", nil
	}
	conn, err := h.Repo.GetProviderConnectionByID(a.ID)
	if err != nil || conn == nil {
		return "", err
	}
	var data map[string]any
	if conn.Data != "" {
		_ = json.Unmarshal([]byte(conn.Data), &data)
	}
	creds := usageCredentials{raw: data, provider: conn.Provider}
	outcome := h.runUsageRefresh(ctx, conn, creds)
	if outcome.err != nil {
		return "", outcome.err
	}
	if !outcome.refreshed {
		return "", nil
	}
	return creds.accessToken(), nil
}

// evaluateMiniMaxCheckinPanel reads the seven-day panel as magpie's checkinOf
// does. It returns the outcome for a day that needs no claim, or needClaim=true
// with the streak of claimed days before today when today is claimable.
func evaluateMiniMaxCheckinPanel(env map[string]any) (outcome minimaxcheckin.Outcome, credit float64, streak int, needClaim bool, err error) {
	data := nestedMap(env, "data")
	rawDays, _ := data["days"].([]any)
	if len(rawDays) == 0 {
		return "", 0, 0, false, fmt.Errorf("check-in: no panel in the reply")
	}
	days := make([]map[string]any, 0, len(rawDays))
	for _, raw := range rawDays {
		d, ok := raw.(map[string]any)
		if !ok {
			return "", 0, 0, false, fmt.Errorf("check-in: malformed panel")
		}
		days = append(days, d)
	}
	// Sort by day_no and require exactly 1..7.
	for i := 0; i < len(days); i++ {
		for j := i + 1; j < len(days); j++ {
			if usageNum(days[j]["day_no"], 0) < usageNum(days[i]["day_no"], 0) {
				days[i], days[j] = days[j], days[i]
			}
		}
	}
	if len(days) != 7 {
		return "", 0, 0, false, fmt.Errorf("check-in: malformed panel")
	}
	todayIdx := -1
	todayCount, claimableCount := 0, 0
	for i, d := range days {
		if usageNum(d["day_no"], 0) != float64(i+1) {
			return "", 0, 0, false, fmt.Errorf("check-in: malformed panel")
		}
		if isToday, _ := d["is_today"].(bool); isToday {
			todayCount++
			todayIdx = i
		}
		if usageNum(d["status"], 0) == 2 {
			claimableCount++
		}
	}
	if todayCount > 1 || claimableCount > 1 {
		return "", 0, 0, false, fmt.Errorf("check-in: malformed panel")
	}
	if todayIdx < 0 {
		return minimaxcheckin.OutcomeInactive, 0, 0, false, nil
	}
	streakFrom := func(i int) int {
		n := 0
		for j := i; j >= 0 && usageNum(days[j]["status"], 0) == 3; j-- {
			n++
		}
		return n
	}
	today := days[todayIdx]
	switch usageNum(today["status"], 0) {
	case 3:
		credit = positiveMiniMaxCheckinPoints(today["points"])
		return minimaxcheckin.OutcomeDone, credit, streakFrom(todayIdx), false, nil
	case 2:
		return "", 0, streakFrom(todayIdx - 1), true, nil
	case 4:
		return minimaxcheckin.OutcomeIneligible, 0, 0, false, nil
	default:
		return minimaxcheckin.OutcomeInactive, 0, 0, false, nil
	}
}

// evaluateMiniMaxCheckinClaim reads the claim answer: claim_result 1 claimed
// now, 2 already today. The streak counts the claimed days before today plus
// the day just claimed.
func evaluateMiniMaxCheckinClaim(env map[string]any, streakBefore int) (minimaxcheckin.Outcome, float64, int, error) {
	data := nestedMap(env, "data")
	r := usageNum(data["claim_result"], 0)
	credit := positiveMiniMaxCheckinPoints(data["points"])
	streak := streakBefore + 1
	switch r {
	case 1:
		return minimaxcheckin.OutcomeClaimed, credit, streak, nil
	case 2:
		return minimaxcheckin.OutcomeDone, credit, streak, nil
	default:
		return "", 0, 0, fmt.Errorf("check-in: unknown claim_result %v", data["claim_result"])
	}
}

func positiveMiniMaxCheckinPoints(v any) float64 {
	if n := usageNum(v, 0); n > 0 {
		return n
	}
	return 0
}

// HandleGetMiniMaxCheckins returns the last daily check-in outcome per MiniMax
// Code account, keyed by connection id so the quota tracker can look up each
// row directly. The loop writes one file under DATA_DIR; reading it here keeps
// the dashboard in sync with the background worker without a network call.
func (h *DashboardHandler) HandleGetMiniMaxCheckins(w http.ResponseWriter, r *http.Request) {
	ledger, err := minimaxcheckin.NewLedger(filepath.Join(config.ResolveDataDir(), "minimax-checkin.json"))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	byAccount := make(map[string]minimaxcheckin.Result)
	for _, r := range ledger.Results() {
		key := r.AccountID
		if key == "" {
			key = r.Account
		}
		byAccount[key] = r
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"checkins": byAccount})
}

// HandleMiniMaxCheckinNow claims today's check-in for one connection right away
// (magpie's "Check in now" button), records the outcome and returns it.
func (h *DashboardHandler) HandleMiniMaxCheckinNow(w http.ResponseWriter, r *http.Request) {
	connID := getURLParam(r, "connectionId")
	if connID == "" {
		connID = getURLParam(r, "id")
	}
	if connID == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing connectionId")
		return
	}
	conn, err := h.Repo.GetProviderConnectionByID(connID)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if conn == nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "connection not found")
		return
	}
	if conn.Provider != "minimax-code" && conn.Provider != "minimax-code-global" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "not a MiniMax Code connection")
		return
	}
	if conn.AuthType != "oauth" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "check-in needs an OAuth sign-in; a desktop lease has no stored token")
		return
	}
	a := minimaxcheckin.AccountFromConnection(conn.Provider, conn)
	if a.Token == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "not signed in")
		return
	}
	result := h.CheckMiniMaxCode(r.Context(), a)
	if ledger, lerr := minimaxcheckin.NewLedger(filepath.Join(config.ResolveDataDir(), "minimax-checkin.json")); lerr == nil {
		_ = ledger.Record(result)
	}
	handlerutil.WriteJSON(w, http.StatusOK, result)
}
