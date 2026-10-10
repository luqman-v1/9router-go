package dashboard

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// MiniMax Code account usage — credits balance, plan tier, and M Plan rate
// windows, read the way MiniMax Code itself reads them (its signed account
// API). Port of upstream open-sse/services/minimaxCodeUsage.js
// (decolua/9router 85bc33ce); the protocol itself is the one magpie's
// opencode-minimax-auth plugin speaks.
//
// Signing (account API only — the coding_plan/remains endpoint is plain
// Bearer): the request carries mcode's query params, a `yy` md5 over
// path+query, body and now, and an `x-signature` md5 over ts + secret
// (+ body).

const (
	miniMaxCodeSignSecret  = "I*7Cf%WZ#S&%1RlZJ&C2"
	miniMaxCodeSignSalt    = "ooui"
	miniMaxCodeBrowserName = "mcode"
	miniMaxCodeVersionCode = "22201"
	miniMaxCodeBizID       = "3"
	miniMaxCodeAppID       = "3001"
)

// miniMaxCodeSite is one mcode site's account and platform hosts.
//
// The China site is the odd one: its chat host is agent.minimax.cn, but the
// account API lives on agent.minimaxi.com. Sign-ins are per site, so the two
// are never cross-felled (AGENTS.md §3.A).
type miniMaxCodeSite struct {
	agent    string
	platform string
	lang     string
}

var miniMaxCodeSites = map[string]miniMaxCodeSite{
	"minimax-code":        {agent: "https://agent.minimaxi.com", platform: "https://www.minimaxi.com", lang: "zh"},
	"minimax-code-global": {agent: "https://agent.minimax.io", platform: "https://platform.minimax.io", lang: "en"},
}

// errMiniMaxCodeRefused marks an answer that says the sign-in itself is gone
// (401/403, or statusInfo 1000048). The quota route force-refreshes and retries
// once on that, unlike any other failure.
var errMiniMaxCodeRefused = errors.New("MiniMax Code sign-in refused")

// accountRefused reports whether err means the sign-in was refused.
func accountRefused(err error) bool { return errors.Is(err, errMiniMaxCodeRefused) }

// fetchMiniMaxCodeUsage reads credits, plan and M Plan windows for one
// connection, returning the normalized {plan, quotas} shape the dashboard
// parser consumes.
func fetchMiniMaxCodeUsage(ctx context.Context, provider, accessToken, userID string) usageResult {
	site, ok := miniMaxCodeSites[provider]
	if !ok || strings.TrimSpace(accessToken) == "" {
		return usageResult{message: fmt.Sprintf("Usage API not implemented for %s", provider)}
	}

	var who miniMaxCodeIdentity
	if strings.TrimSpace(userID) == "" {
		var err error
		if who, err = miniMaxCodeIdentityOf(ctx, site, accessToken); err != nil {
			return miniMaxCodeFailure(err)
		}
		userID = who.realUserID
	}

	plan, err := miniMaxCodeReadPlan(ctx, site, accessToken, userID)
	if err != nil {
		return miniMaxCodeFailure(err)
	}

	quotas := map[string]any{}
	if plan.balance != "" {
		quotas["Credits"] = creditsQuota(plan.balance)
	}
	if plan.hasTokenPlan && plan.opGroupID != "" {
		windows, wErr := miniMaxCodePlanWindows(ctx, site, accessToken, plan.opGroupID)
		if wErr != nil {
			// The M Plan reading is additive: losing it must not lose the
			// credits balance the account actually has.
			quotas["M Plan windows"] = map[string]any{
				"name": "M Plan windows", "used": float64(0), "total": float64(0),
				"resetAt": nil, "message": wErr.Error(),
			}
		}
		for _, w := range windows {
			quotas[w.name] = w
		}
	}
	if len(quotas) == 0 {
		quotas["Plan"] = map[string]any{
			"name": "Plan", "used": float64(0), "total": float64(0), "resetAt": nil,
			"message": planMessage(plan.hasTokenPlan),
		}
	}

	result := usageResult{
		plan:   miniMaxCodePlanLabel(plan.hasTokenPlan, plan.tier),
		quotas: quotas,
	}
	if plan.expiresAt > 0 {
		result.extra = map[string]any{
			"planExpiresAt": time.UnixMilli(epochMillis(plan.expiresAt)).UTC().Format(time.RFC3339),
		}
	}
	return result
}

// creditsQuota renders a credits balance as the dashboard's balance row: the
// remaining amount is the total, nothing has been spent against it in this
// reading, and the isCreditBalance flag is what tells the UI to render it as
// an amount rather than a percentage.
func creditsQuota(balance string) map[string]any {
	q := usageQuota(0, usageNum(balance, 0), "")
	q["isCreditBalance"] = true
	q["currency"] = "credits"
	return q
}

// epochMillis normalizes an epoch published in seconds or milliseconds to
// milliseconds: mcode states plan expiry in seconds but M Plan windows in
// milliseconds.
func epochMillis(v float64) int64 {
	if v < 1e12 {
		return int64(v) * 1000
	}
	return int64(v)
}

// planMessage explains an account that reported no usage rows at all.
func planMessage(hasTokenPlan bool) string {
	if !hasTokenPlan {
		return "Free account (no M Plan)"
	}
	return "No usage data returned"
}

// miniMaxCodeFailure turns a failure into the shape the route's auth-expired
// check recognises, so a refused sign-in force-refreshes and retries once
// instead of being shown as a dead panel.
func miniMaxCodeFailure(err error) usageResult {
	if accountRefused(err) {
		return usageResult{message: fmt.Sprintf("MiniMax Code sign-in expired — unauthorized, please re-authorize (%v)", err)}
	}
	return usageResult{message: err.Error()}
}

// miniMaxCodePlanLabel names the plan tier the account reports.
func miniMaxCodePlanLabel(hasTokenPlan bool, tier string) string {
	if !hasTokenPlan {
		return "Free"
	}
	if tier != "" {
		return "M Plan " + tier
	}
	return "M Plan"
}

// miniMaxCodeIdentity is who the account is, best-effort.
type miniMaxCodeIdentity struct {
	realUserID string
	email      string
	name       string
}

// miniMaxCodeIdentityOf reads the account's identity from the signed API.
func miniMaxCodeIdentityOf(ctx context.Context, site miniMaxCodeSite, access string) (miniMaxCodeIdentity, error) {
	env, err := miniMaxCodeAccountCall(ctx, site, "/v1/api/user/info", access, "", nil, "account")
	if err != nil {
		return miniMaxCodeIdentity{}, err
	}
	data := nestedMap(env, "data")
	u := nestedMap(data, "userInfo")
	if u == nil {
		u = nestedMap(data, "user_info")
	}
	if u == nil {
		u = nestedMap(env, "userInfo")
	}
	if u == nil {
		u = nestedMap(env, "user_info")
	}
	id := firstNonEmptyStr(usageStr(u["realUserID"]), usageStr(u["real_user_id"]))
	if id == "" {
		return miniMaxCodeIdentity{}, fmt.Errorf("account: no user id in the reply")
	}
	return miniMaxCodeIdentity{
		realUserID: id,
		email:      firstNonEmptyStr(usageStr(u["userEmail"]), usageStr(u["email"]), usageStr(u["userMail"]), usageStr(u["user_email"])),
		name:       firstNonEmptyStr(usageStr(u["name"]), usageStr(u["userName"]), usageStr(u["user_name"])),
	}, nil
}

// miniMaxCodePlan is the account's plan and credits.
type miniMaxCodePlan struct {
	hasTokenPlan bool
	hasPlanKnown bool
	opGroupID    string
	tier         string
	balance      string
	expiresAt    float64
}

// miniMaxCodeReadPlan reads the account's own workspace (type 0) and then the
// membership API, merging the two the way MiniMax Code does.
func miniMaxCodeReadPlan(ctx context.Context, site miniMaxCodeSite, access, userID string) (miniMaxCodePlan, error) {
	ws, wsFound, err := miniMaxCodeOwnWorkspace(ctx, site, access, userID)
	if err != nil {
		return miniMaxCodePlan{}, err
	}
	if !wsFound {
		m, mErr := miniMaxCodeMembershipInfo(ctx, site, access, userID, nil)
		if mErr != nil {
			return miniMaxCodePlan{}, mErr
		}
		return m, nil
	}

	m, mErr := miniMaxCodeMembershipInfo(ctx, site, access, userID, ws.workspaceID)
	if mErr != nil {
		// The workspace row already carries a plan; the membership call only
		// enriches it, so its failure must not erase what we have.
		return ws.plan, nil
	}
	mergeMiniMaxCodePlan(&ws.plan, m)
	return ws.plan, nil
}

// ownWorkspace is the account's workspace entry.
type ownWorkspace struct {
	workspaceID any
	plan        miniMaxCodePlan
}

// miniMaxCodeOwnWorkspace finds the account's own workspace (workspace_type 0).
func miniMaxCodeOwnWorkspace(ctx context.Context, site miniMaxCodeSite, access, userID string) (ownWorkspace, bool, error) {
	env, err := miniMaxCodeAccountCall(ctx, site, "/matrix/api/v1/user/get_user_extra_info", access, userID, map[string]any{}, "workspace")
	if err != nil {
		if accountRefused(err) {
			return ownWorkspace{}, false, err
		}
		// The account has no workspace row at all; the membership API is the
		// next source, exactly as MiniMax Code falls back.
		return ownWorkspace{}, false, nil
	}
	list, _ := env["workspaces"].([]any)
	if list == nil {
		list, _ = nestedMap(env, "data")["workspaces"].([]any)
	}
	for _, raw := range list {
		w, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if usageNum(w["workspace_type"], -1) != 0 {
			continue
		}
		id, ok := w["workspace_id"]
		if !ok {
			continue
		}
		if n, isNum := id.(float64); isNum && n < 0 {
			continue
		}
		return ownWorkspace{workspaceID: id, plan: parseMiniMaxCodeMembership(w)}, true, nil
	}
	return ownWorkspace{}, false, nil
}

// miniMaxCodeMembershipInfo reads the membership API, optionally scoped to a
// workspace.
func miniMaxCodeMembershipInfo(ctx context.Context, site miniMaxCodeSite, access, userID string, workspaceID any) (miniMaxCodePlan, error) {
	body := map[string]any{}
	if workspaceID != nil {
		body["workspace_id"] = workspaceID
	}
	env, err := miniMaxCodeAccountCall(ctx, site, "/matrix/api/v1/commerce/get_membership_info", access, userID, body, "membership")
	if err != nil {
		return miniMaxCodePlan{}, err
	}
	return parseMiniMaxCodeMembership(env), nil
}

// mergeMiniMaxCodePlan folds the membership answer into the workspace row,
// letting an explicit false win over an unset value.
func mergeMiniMaxCodePlan(dst *miniMaxCodePlan, src miniMaxCodePlan) {
	if src.hasPlanKnown {
		dst.hasTokenPlan = src.hasTokenPlan
		dst.hasPlanKnown = true
	}
	if src.opGroupID != "" {
		dst.opGroupID = src.opGroupID
	}
	if src.tier != "" {
		dst.tier = src.tier
	}
	if src.balance != "" {
		dst.balance = src.balance
	}
	if src.expiresAt > 0 {
		dst.expiresAt = src.expiresAt
	}
}

// parseMiniMaxCodeMembership reads a membership answer (or workspace entry).
func parseMiniMaxCodeMembership(e map[string]any) miniMaxCodePlan {
	data := nestedMap(e, "data")
	pick := func(k string) any {
		if v, ok := e[k]; ok && v != nil {
			return v
		}
		return data[k]
	}
	m := miniMaxCodePlan{}
	if has, ok := pick("has_token_plan").(bool); ok {
		m.hasTokenPlan, m.hasPlanKnown = has, true
	}
	m.opGroupID = usageStr(pick("op_group_id"))
	m.tier = usageStr(pick("token_plan_tier"))
	m.expiresAt = usageNum(pick("token_plan_expires_at"), 0)
	m.balance = miniMaxCodeBalance(e, data)
	return m
}

// miniMaxCodeBalance reads the credits balance from either shape mcode uses:
// an op_credit_summary row, or a bare opcredit_balance number/string.
func miniMaxCodeBalance(e, data map[string]any) string {
	sum := nestedMap(e, "op_credit_summary")
	if len(sum) == 0 {
		sum = nestedMap(data, "op_credit_summary")
	}
	if v := usageStr(sum["total_remaining_amount"]); v != "" {
		return strings.TrimSpace(v)
	}
	switch v := pickMiniMaxCodeField(e, data, "opcredit_balance").(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return ""
	}
}

// pickMiniMaxCodeField reads a field at either the top level or under data.
func pickMiniMaxCodeField(e, data map[string]any, key string) any {
	if v, ok := e[key]; ok && v != nil {
		return v
	}
	return data[key]
}

// miniMaxCodeAccountCall performs one signed account-API request and checks the
// answer's own status envelope.
func miniMaxCodeAccountCall(ctx context.Context, site miniMaxCodeSite, path, access, userID string, body map[string]any, what string) (map[string]any, error) {
	rawURL, headers, method, payload := signedMiniMaxCodeRequest(site, path, access, userID, body, time.Now())
	if payload != nil {
		headers["Content-Length"] = strconv.Itoa(len(payload))
	}

	status, _, out, err := usageDo(ctx, method, rawURL, headers, payload)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return nil, fmt.Errorf("%w (%s: HTTP %d)", errMiniMaxCodeRefused, what, status)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", what, status)
	}
	env := usageJSON(out)
	if env == nil {
		return nil, fmt.Errorf("%s: not JSON", what)
	}
	return checkMiniMaxCodeEnvelope(env, what)
}

// checkMiniMaxCodeEnvelope rejects the answer's own status envelope, which
// carries an error inside a 200.
func checkMiniMaxCodeEnvelope(env map[string]any, what string) (map[string]any, error) {
	if si := nestedMap(env, "statusInfo"); si != nil {
		if code := usageNum(si["code"], 0); code != 0 {
			if code == 1000048 {
				return nil, fmt.Errorf("%w (%s)", errMiniMaxCodeRefused, what)
			}
			return nil, fmt.Errorf("%s: %s", what, miniMaxCodeStatusText(si, code))
		}
	}
	if br := nestedMap(env, "base_resp"); br != nil {
		if code := usageNum(br["status_code"], 0); code != 0 {
			return nil, fmt.Errorf("%s: %s", what, miniMaxCodeStatusText(br, code))
		}
	}
	return env, nil
}

// miniMaxCodeWindow is one M Plan rate window as the dashboard renders it.
type miniMaxCodeWindow struct {
	name      string
	remaining float64
	resetAt   any
}

// miniMaxCodePlanWindows reads the M Plan's rate windows from
// coding_plan/remains. That endpoint is plain Bearer — unlike the account API
// it is not signed — and answers one row per model carrying a per-interval and
// a per-week window.
func miniMaxCodePlanWindows(ctx context.Context, site miniMaxCodeSite, access, group string) ([]miniMaxCodeWindow, error) {
	headers := map[string]string{
		"Accept":        "application/json",
		"Authorization": "Bearer " + access,
	}
	if group != "" {
		headers["X-Group-Id"] = group
	}
	status, _, out, err := usageDo(ctx, http.MethodGet,
		site.platform+"/v1/api/openplatform/coding_plan/remains", headers, nil)
	if err != nil {
		return nil, fmt.Errorf("M Plan: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("M Plan: HTTP %d", status)
	}
	return parseMiniMaxCodePlanWindows(usageJSON(out))
}

// miniMaxCodeWindowSpec is one raw window inside a model_remains row.
type miniMaxCodeWindowSpec struct {
	left   any
	status float64
	start  float64
	end    float64
	weekly bool
}

// parseMiniMaxCodePlanWindows turns coding_plan/remains into dashboard rows.
// A window is dropped when the model reports it as exhausted, and a model whose
// every window is spent is not reported at all — mcode hides such a row, so the
// dashboard does too.
func parseMiniMaxCodePlanWindows(body map[string]any) ([]miniMaxCodeWindow, error) {
	if br := nestedMap(body, "base_resp"); len(br) > 0 {
		if code := usageNum(br["status_code"], 0); code != 0 {
			return nil, fmt.Errorf("MiniMax said %d: %s", int(code), miniMaxCodeStatusText(br, code))
		}
	}
	rows, _ := body["model_remains"].([]any)
	if rows == nil {
		return nil, fmt.Errorf("no plan in the reply")
	}

	out := make([]miniMaxCodeWindow, 0, len(rows)*2)
	for _, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name := strings.TrimSpace(usageStr(row["model_name"]))
		if name == "" || miniMaxCodeRowSpent(row) {
			continue
		}
		general := strings.EqualFold(name, "general")
		specs := []miniMaxCodeWindowSpec{
			{left: row["current_interval_remaining_percent"], status: usageNum(row["current_interval_status"], 0), start: usageNum(row["start_time"], 0), end: usageNum(row["end_time"], 0)},
			{left: row["current_weekly_remaining_percent"], status: usageNum(row["current_weekly_status"], 0), start: usageNum(row["weekly_start_time"], 0), end: usageNum(row["weekly_end_time"], 0), weekly: true},
		}
		for _, spec := range specs {
			if w, ok := miniMaxCodeWindowOf(spec, name, general); ok {
				out = append(out, w)
			}
		}
	}
	return out, nil
}

// miniMaxCodeRowSpent reports whether a model row has nothing left in any
// window.
func miniMaxCodeRowSpent(row map[string]any) bool {
	return usageNum(row["current_interval_status"], 0) == 3 &&
		usageNum(row["current_weekly_status"], 0) == 3 &&
		usageNum(row["current_interval_total_count"], 0) == 0 &&
		usageNum(row["current_weekly_total_count"], 0) == 0
}

// miniMaxCodeWindowOf renders one window, or reports that it is not worth
// showing. Status 2 means "exhausted with no percentage published", which is
// shown as 0% left rather than dropped — the account really is out.
func miniMaxCodeWindowOf(spec miniMaxCodeWindowSpec, name string, general bool) (miniMaxCodeWindow, bool) {
	if spec.status == 3 || (spec.left == nil && spec.status != 2) {
		return miniMaxCodeWindow{}, false
	}
	left := 0.0
	if spec.status != 2 {
		left = max(0, min(100, usageNum(spec.left, 0)))
	}
	windowName := miniMaxCodeWindowLabel(spec)
	if !general {
		windowName = strings.ToUpper(name[:1]) + name[1:] + " · " + windowName
	}
	return miniMaxCodeWindow{name: windowName, remaining: left, resetAt: usageResetTime(spec.end)}, true
}

// miniMaxCodeWindowLabel names a window by its actual span, so a "5 hours"
// window is not shown as a vague allowance.
func miniMaxCodeWindowLabel(spec miniMaxCodeWindowSpec) string {
	if spec.weekly {
		return "7 days"
	}
	var span time.Duration
	if spec.start > 0 && spec.end > spec.start {
		span = time.Duration(epochMillis(spec.end)-epochMillis(spec.start)) * time.Millisecond
	}
	switch {
	case span > 0 && span%(24*time.Hour) == 0:
		return fmt.Sprintf("%d days", int(span/(24*time.Hour)))
	case span > 0 && span%time.Hour == 0:
		return fmt.Sprintf("%d hours", int(span/time.Hour))
	case span > 0:
		return fmt.Sprintf("%d minutes", int(span/time.Minute))
	default:
		return "Allowance"
	}
}

// miniMaxCodeStatusText names the failure an envelope reported. MiniMax states
// it under status_msg in a base_resp and under message/msg in a statusInfo, so
// every name is read before falling back to the bare code.
func miniMaxCodeStatusText(env map[string]any, code float64) string {
	if msg := firstNonEmptyStr(
		usageStr(env["status_msg"]),
		usageStr(env["message"]),
		usageStr(env["msg"]),
	); msg != "" {
		return msg
	}
	return "status " + strconv.FormatFloat(code, 'f', -1, 64)
}

// signedMiniMaxCodeRequest builds a request of the account API as mcode signs
// one: `yy` over the encoded path+query, the JSON body ("{}" for a GET) and
// md5(now); `x-signature` over ts (and the body of a POST).
func signedMiniMaxCodeRequest(site miniMaxCodeSite, path, access, userID string, body map[string]any, now time.Time) (string, map[string]string, string, []byte) {
	_, offsetSeconds := now.Zone()
	q := url.Values{
		"device_platform": {miniMaxCodeBrowserName},
		"biz_id":          {miniMaxCodeBizID},
		"app_id":          {miniMaxCodeAppID},
		"version_code":    {miniMaxCodeVersionCode},
		"unix":            {strconv.FormatInt(now.UnixMilli(), 10)},
		"timezone_offset": {strconv.Itoa(-offsetSeconds)},
		"sys_language":    {site.lang},
		"lang":            {site.lang},
		"device_id":       {"0"},
		"os_name":         {runtime.GOOS},
		"browser_name":    {miniMaxCodeBrowserName},
		"user_id":         {firstNonEmptyStr(strings.TrimSpace(userID), "0")},
		"client":          {miniMaxCodeBrowserName},
	}
	rawURL := site.agent + path + "?" + q.Encode()
	at := path + "?" + q.Encode()

	ts := strconv.FormatInt(now.Unix(), 10)
	var payload []byte
	method := http.MethodGet
	signedPath := encodeURIComponent(at) + "_"
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			encoded = []byte("{}")
		}
		payload = encoded
		method = http.MethodPost
		signedPath += string(encoded)
	}
	signedPath += miniMaxCodeMD5(strconv.FormatInt(now.UnixMilli(), 10)) + miniMaxCodeSignSalt

	headers := map[string]string{
		"Accept":        "application/json",
		"Content-Type":  "application/json",
		"User-Agent":    "MiniMaxCode",
		"Authorization": "Bearer " + access,
		"yy":            miniMaxCodeMD5(signedPath),
		"x-timestamp":   ts,
		"x-signature":   miniMaxCodeMD5(ts + miniMaxCodeSignSecret + string(payload)),
	}
	return rawURL, headers, method, payload
}

// miniMaxCodeMD5 hex-encodes an md5 digest.
func miniMaxCodeMD5(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// encodeURIComponent percent-encodes every character outside JS's
// encodeURIComponent safe set, which is what the signature is computed over.
func encodeURIComponent(s string) string {
	const safe = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.!~*'()"
	var b strings.Builder
	b.Grow(len(s) * 3)
	for i := range len(s) {
		if strings.IndexByte(safe, s[i]) >= 0 {
			b.WriteByte(s[i])
			continue
		}
		fmt.Fprintf(&b, "%%%02X", s[i])
	}
	return b.String()
}
