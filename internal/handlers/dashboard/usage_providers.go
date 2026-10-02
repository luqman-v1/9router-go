package dashboard

import (
	"bytes"
	"context"
	"encoding/base64"
	json "encoding/json/v2"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
)

// Provider usage fetchers for GET /api/usage/{connectionId}.
// Ports of open-sse/services/usage/*.js, each returning the normalized
// {plan, quotas} shape the dashboard parser consumes. Only live provider
// data — no secrets leave this file except as Bearer headers upstream.

const usageHTTPTimeout = 15 * time.Second

// usageResult is a normalized provider usage response.
type usageResult struct {
	plan    string
	quotas  map[string]any
	message string
	extra   map[string]any
	// bare mirrors upstream handlers (e.g. qoder) that return {message}
	// with NO quotas key on error. Most handlers include quotas:{}.
	bare bool
}

func (r usageResult) toResponse() map[string]any {
	// Mirror upstream: the dashboard hides the quota table whenever a message
	// is set, so only attach a message when there are no quota rows.
	if len(r.quotas) > 0 {
		out := map[string]any{"plan": r.plan, "quotas": r.quotas}
		for k, v := range r.extra {
			out[k] = v
		}
		return out
	}
	if r.message != "" {
		out := map[string]any{"message": r.message}
		if !r.bare {
			out["quotas"] = map[string]any{}
		}
		if r.plan != "" {
			out["plan"] = r.plan
		}
		if len(r.quotas) > 0 {
			out["quotas"] = r.quotas
		}
		return out
	}
	return map[string]any{"plan": r.plan, "quotas": map[string]any{}}
}

// fetchProviderUsage dispatches a live quota fetch for providers with a
// ported usage handler. Returns ok=false for unhandled providers (caller
// falls back to locks/antigravity paths).
func fetchProviderUsage(ctx context.Context, provider string, data map[string]any) (usageResult, bool) {
	accessToken, apiKey, psd := usageCreds(data)
	switch provider {
	case "deepseek":
		return fetchDeepseekUsage(ctx, apiKey), true
	case "groq":
		return fetchGroqUsage(ctx, apiKey), true
	case "commandcode":
		return fetchCommandCodeUsage(ctx, apiKey), true
	case "ollama":
		return fetchOllamaUsage(ctx, apiKey), true
	case "qoder":
		return fetchQoderUsage(ctx, firstNonEmptyStr(accessToken, apiKey)), true
	case "qoder-cn":
		return fetchQoderUsageAt(ctx, firstNonEmptyStr(accessToken, apiKey), "https://openapi.qoder.com.cn/api/v2/quota/usage"), true
	case "codebuddy-intl":
		return fetchCodeBuddyIntlUsage(ctx, accessToken, apiKey), true
	case "kiro":
		return fetchKiroUsage(ctx, kiroUsageToken(accessToken, apiKey, psd), psd), true
	case "grok-cli":
		return fetchGrokCliUsage(ctx, accessToken, psd), true
	case "codex":
		return fetchCodexUsage(ctx, firstNonEmptyStr(accessToken, apiKey)), true
	case "opencode-zen":
		return fetchOpenCodeZenUsage(ctx, apiKey, connectionBaseURL(data)), true
	default:
		return usageResult{}, false
	}
}
func usageCreds(data map[string]any) (accessToken, apiKey string, psd map[string]any) {
	if data == nil {
		return "", "", nil
	}
	accessToken, _ = data["accessToken"].(string)
	apiKey, _ = data["apiKey"].(string)
	if psd, ok := data["providerSpecificData"].(map[string]any); ok {
		return accessToken, apiKey, psd
	}
	return accessToken, apiKey, map[string]any{}
}

// kiroUsageToken mirrors resolveProviderAuthToken in internal/handlers/chat, which
// picks the credential the chat path sends. Keeping the same precedence here is
// what stops the quota tracker from sending an empty bearer for a connection whose
// chat traffic works: a Kiro connection stores its credential in apiKey, and
// passing only accessToken produced "Kiro quota API rejected the current token"
// while chat succeeded with the very credential the quota read never looked at.
func kiroUsageToken(accessToken, apiKey string, psd map[string]any) string {
	authMethod, _ := psd["authMethod"].(string)
	if authMethod == "api_key" && apiKey != "" {
		return apiKey
	}
	if accessToken != "" {
		return accessToken
	}
	return apiKey
}

func psdStr(psd map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := psd[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// connectionBaseURL reports the endpoint a connection actually dials. The chat
// path reads it from ConnectionData.BaseURL, which is hydrated from either the
// top-level baseUrl or providerSpecificData.baseUrl depending on how the
// connection was written.
func connectionBaseURL(data map[string]any) string {
	if data == nil {
		return ""
	}
	if v := usageStr(data["baseUrl"]); v != "" {
		return v
	}
	if psd, ok := data["providerSpecificData"].(map[string]any); ok {
		return usageStr(psd["baseUrl"])
	}
	return ""
}

func usageGet(ctx context.Context, rawURL string, headers map[string]string) (int, http.Header, []byte, error) {
	return usageDo(ctx, http.MethodGet, rawURL, headers, nil)
}

func usagePost(ctx context.Context, rawURL string, payload map[string]any, headers map[string]string) (int, map[string]any, error) {
	var body []byte
	if payload != nil {
		body, _ = json.Marshal(payload)
	}
	status, _, out, err := usageDo(ctx, http.MethodPost, rawURL, headers, body)
	if err != nil {
		return 0, nil, err
	}
	return status, usageJSON(out), nil
}

// usageHTTPClient is the shared sender for every dashboard usage fetch.
//
// It wraps the environment transport in proxy.FallbackTransport for the same
// reason the chat path does (internal/proxy/fallback_transport.go): a sandbox
// or corporate HTTP(S)_PROXY refuses the CONNECT tunnel to several provider
// hosts with 403 Forbidden, and Go renders that refusal as a transport error
// (`Get "https://…": Forbidden`) rather than a status code. Bare
// http.DefaultClient has no direct-connection fallback, so those fetchers failed
// on hosts that chat traffic reaches fine. Sharing one client fixes them all at
// the sender instead of one provider at a time.
var usageHTTPClient = &http.Client{Transport: proxy.NewFallbackTransport(http.DefaultTransport)}

// usageUserAgent is sent when a fetcher does not set its own, so a request does
// not announce Go's default "Go-http-client/1.1" to WAF-fronted hosts. Fetchers
// that already set one (grok-cli, antigravity) keep theirs.
const usageUserAgent = "9router-go"

func usageDo(ctx context.Context, method, rawURL string, headers map[string]string, body []byte) (int, http.Header, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, usageHTTPTimeout)
	defer cancel()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return 0, nil, nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", usageUserAgent)
	}
	resp, err := usageHTTPClient.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, resp.Header, out, err
}

func usageJSON(out []byte) map[string]any {
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil || m == nil {
		return nil
	}
	return m
}

func usageNum(v any, fallback float64) float64 {
	if f, ok := usageFiniteNum(v); ok {
		return f
	}
	return fallback
}

func usageFiniteNum(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		if !math.IsNaN(n) && !math.IsInf(n, 0) {
			return n, true
		}
	case float32:
		f := float64(n)
		if !math.IsNaN(f) && !math.IsInf(f, 0) {
			return f, true
		}
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case string:
		if s := strings.TrimSpace(n); s != "" {
			if f, err := strconv.ParseFloat(s, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
				return f, true
			}
		}
	case map[string]any:
		// protobuf-json {val: n} envelope.
		if _, ok := n["val"]; ok {
			return usageFiniteNum(n["val"])
		}
	}
	return 0, false
}

func usageStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// usageResetTime mirrors upstream parseResetTime: unix s/ms, numeric strings,
// ISO strings → RFC3339, else "".
func usageResetTime(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return ""
		}
		ms := t
		if ms < 1e12 {
			ms *= 1000
		}
		return time.UnixMilli(int64(ms)).UTC().Format(time.RFC3339)
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return ""
		}
		if matched, _ := regexp.MatchString(`^\d+$`, s); matched {
			if n, err := strconv.ParseFloat(s, 64); err == nil {
				return usageResetTime(n)
			}
			return ""
		}
		if tm, err := time.Parse(time.RFC3339, s); err == nil {
			return tm.UTC().Format(time.RFC3339)
		}
		if tm, err := time.Parse("2006-01-02T15:04:05.999999999Z07:00", s); err == nil {
			return tm.UTC().Format(time.RFC3339)
		}
		// Naive datetimes (e.g. CodeBuddy "2026-09-30 23:59:59") parse in the
		// server-local zone — mirroring JS new Date(str) in parseResetTime.
		for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02"} {
			if tm, err := time.ParseInLocation(layout, s, time.Local); err == nil {
				return tm.UTC().Format(time.RFC3339)
			}
		}
		return ""
	default:
		return ""
	}
}

func usageQuota(used, total float64, resetAt string) map[string]any {
	used = math.Max(0, used)
	total = math.Max(0, total)
	q := map[string]any{"used": used, "total": total, "resetAt": nil, "unlimited": false}
	if resetAt != "" {
		q["resetAt"] = resetAt
	}
	if total > 0 {
		q["remainingPercentage"] = math.Max(0, total-used) / total * 100
	} else {
		q["remainingPercentage"] = 0
	}
	return q
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// ---------- deepseek: GET /user/balance ----------

func fetchDeepseekUsage(ctx context.Context, apiKey string) usageResult {
	if strings.TrimSpace(apiKey) == "" {
		return usageResult{message: "DeepSeek API key not available. Add a key to view usage."}
	}
	status, _, out, err := usageGet(ctx, "https://api.deepseek.com/user/balance", map[string]string{
		"Authorization": "Bearer " + strings.TrimSpace(apiKey),
		"Accept":        "application/json",
	})
	if err != nil {
		return usageResult{message: fmt.Sprintf("DeepSeek error: %v", err)}
	}
	if status == 401 || status == 403 {
		return usageResult{plan: "DeepSeek", message: "DeepSeek authentication failed. Check the API key."}
	}
	if status < 200 || status >= 300 {
		msg := fmt.Sprintf("DeepSeek balance API error (%d)", status)
		if t := strings.TrimSpace(string(out)); t != "" {
			if len(t) > 120 {
				t = t[:120]
			}
			msg += ": " + t
		}
		return usageResult{plan: "DeepSeek", message: msg}
	}
	data := usageJSON(out)
	if data == nil {
		return usageResult{message: "DeepSeek balance response was not JSON."}
	}
	var list []any
	if l, ok := data["balance_infos"].([]any); ok {
		list = l
	}
	quotas := map[string]any{}
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		cur, _ := m["currency"].(string)
		cur = strings.ToUpper(strings.TrimSpace(cur))
		if cur == "" {
			continue
		}
		total := math.Max(0, usageNum(firstNonEmptyAny(m["total_balance"], m["totalBalance"]), 0))
		quotas[fmt.Sprintf("Balance (%s)", cur)] = map[string]any{
			"used": 0, "total": total,
			"remainingPercentage": func() float64 {
				if total > 0 {
					return 100
				}
				return 0
			}(),
			"resetAt": nil, "unlimited": false, "isCreditBalance": true, "currency": cur,
		}
	}
	if len(quotas) == 0 {
		return usageResult{plan: "DeepSeek", message: "DeepSeek connected. No balance data returned."}
	}
	plan := "DeepSeek"
	if data["is_available"] != true && data["isAvailable"] != true {
		plan = "DeepSeek (Insufficient Balance)"
	}
	return usageResult{plan: plan, quotas: quotas}
}

func firstNonEmptyAny(vals ...any) any {
	for _, v := range vals {
		if v != nil {
			return v
		}
	}
	return nil
}

// ---------- groq: rate-limit headers on GET /openai/v1/models ----------

var groqDurationRe = regexp.MustCompile(`(\d+(?:\.\d+)?)(ms|s|m|h)`)

func parseGroqDurationMs(value string) (int64, bool) {
	matches := groqDurationRe.FindAllStringSubmatch(value, -1)
	if len(matches) == 0 {
		return 0, false
	}
	var total float64
	for _, m := range matches {
		amt, _ := strconv.ParseFloat(m[1], 64)
		switch m[2] {
		case "h":
			total += amt * 3600000
		case "m":
			total += amt * 60000
		case "ms":
			total += amt
		default:
			total += amt * 1000
		}
	}
	return int64(total), true
}

func groqRateLimitQuota(h http.Header, limitKey, remainingKey, resetKey string) map[string]any {
	limitRaw, remainingRaw := h.Get(limitKey), h.Get(remainingKey)
	if limitRaw == "" || remainingRaw == "" {
		return nil
	}
	limit, err1 := strconv.ParseFloat(strings.TrimSpace(limitRaw), 64)
	remaining, err2 := strconv.ParseFloat(strings.TrimSpace(remainingRaw), 64)
	if err1 != nil || err2 != nil || math.IsNaN(limit) || math.IsNaN(remaining) {
		return nil
	}
	resetAt := ""
	if ms, ok := parseGroqDurationMs(h.Get(resetKey)); ok {
		resetAt = time.Now().Add(time.Duration(ms) * time.Millisecond).UTC().Format(time.RFC3339)
	}
	return usageQuota(limit-remaining, limit, resetAt)
}

func fetchGroqUsage(ctx context.Context, apiKey string) usageResult {
	if strings.TrimSpace(apiKey) == "" {
		return usageResult{message: "Groq API key not available. Add a key to view usage."}
	}
	status, headers, out, err := usageGet(ctx, "https://api.groq.com/openai/v1/models", map[string]string{
		"Authorization": "Bearer " + strings.TrimSpace(apiKey),
		"Accept":        "application/json",
	})
	if err != nil {
		return usageResult{message: fmt.Sprintf("Groq error: %v", err)}
	}
	if status == 401 || status == 403 {
		return usageResult{plan: "Groq", message: "Groq authentication failed. Check the API key."}
	}
	if status < 200 || status >= 300 {
		msg := fmt.Sprintf("Groq usage API error (%d)", status)
		if t := strings.TrimSpace(string(out)); t != "" {
			if len(t) > 120 {
				t = t[:120]
			}
			msg += ": " + t
		}
		return usageResult{plan: "Groq", message: msg}
	}
	requests := groqRateLimitQuota(headers, "x-ratelimit-limit-requests", "x-ratelimit-remaining-requests", "x-ratelimit-reset-requests")
	tokens := groqRateLimitQuota(headers, "x-ratelimit-limit-tokens", "x-ratelimit-remaining-tokens", "x-ratelimit-reset-tokens")
	if requests == nil && tokens == nil {
		return usageResult{plan: "Groq", message: "Groq connected. No rate-limit data reported for this key yet.", quotas: map[string]any{}}
	}
	quotas := map[string]any{}
	if requests != nil {
		quotas["Requests"] = requests
	}
	if tokens != nil {
		quotas["Tokens"] = tokens
	}
	return usageResult{plan: "Groq", quotas: quotas}
}

// ---------- commandcode: whoami → credits + subscriptions ----------

var commandCodePlanNames = map[string]string{
	"individual-go": "Go", "individual-goat": "GOAT", "individual-pro": "Pro",
	"individual-pro-v1": "Pro", "individual-provider": "Provider",
	"individual-max": "Max", "individual-ultra": "Ultra", "teams-pro": "Teams Pro",
}

var commandCodePlanCaps = map[string]float64{
	"individual-go": 10, "individual-goat": 70, "individual-pro": 30,
	"individual-pro-v1": 80, "individual-provider": 15,
	"individual-max": 150, "individual-ultra": 300, "teams-pro": 40,
}

func commandCodeWindow(win any) map[string]any {
	m, ok := win.(map[string]any)
	if !ok {
		return nil
	}
	used := usageNum(m["used"], 0)
	total := usageNum(m["cap"], 0)
	if total <= 0 && used <= 0 {
		return nil
	}
	total = math.Max(0, total)
	used = math.Max(0, used)
	return map[string]any{
		"used": used, "total": total, "remaining": math.Max(0, total-used),
		"unlimited": false, "resetAt": usageResetTimeToNil(m["resetAt"]),
	}
}

func usageResetTimeToNil(v any) any {
	if s := usageResetTime(v); s != "" {
		return s
	}
	return nil
}

func fetchCommandCodeUsage(ctx context.Context, apiKey string) usageResult {
	if strings.TrimSpace(apiKey) == "" {
		return usageResult{message: "Command Code API key not available. Add a key to view usage."}
	}
	base := strings.TrimSuffix("https://api.commandcode.ai", "/")
	headers := map[string]string{
		"Authorization": "Bearer " + strings.TrimSpace(apiKey),
		"Accept":        "application/json",
	}
	authErr := usageResult{plan: "Command Code", message: "Command Code authentication failed. Check the API key."}
	status, _, out, err := usageGet(ctx, base+"/alpha/whoami?limits=1", headers)
	if err != nil {
		return usageResult{message: fmt.Sprintf("Command Code error: %v", err)}
	}
	if status == 401 || status == 403 {
		return authErr
	}
	if status < 200 || status >= 300 {
		return usageResult{plan: "Command Code", message: fmt.Sprintf("Command Code usage API error (%d)", status)}
	}
	whoami := usageJSON(out)
	var orgID string
	if org, ok := whoami["org"].(map[string]any); ok {
		orgID, _ = org["id"].(string)
	}
	q := ""
	if orgID != "" {
		q = "?orgId=" + url.QueryEscape(orgID)
	}
	type res struct {
		status int
		out    []byte
		err    error
	}
	creditsCh := make(chan res, 1)
	subsCh := make(chan res, 1)
	go func() {
		s, _, o, e := usageGet(ctx, base+"/alpha/billing/credits"+q, headers)
		creditsCh <- res{s, o, e}
	}()
	go func() {
		s, _, o, e := usageGet(ctx, base+"/alpha/billing/subscriptions"+q, headers)
		subsCh <- res{s, o, e}
	}()
	creditsRes, subsRes := <-creditsCh, <-subsCh
	if creditsRes.err != nil || subsRes.err != nil {
		first := creditsRes.err
		if first == nil {
			first = subsRes.err
		}
		return usageResult{message: fmt.Sprintf("Command Code error: %v", first)}
	}
	for _, s := range []int{creditsRes.status, subsRes.status} {
		if s == 401 || s == 403 {
			return authErr
		}
	}
	if creditsRes.status < 200 || creditsRes.status >= 300 {
		return usageResult{plan: "Command Code", message: fmt.Sprintf("Command Code credits API error (%d)", creditsRes.status)}
	}
	if subsRes.status < 200 || subsRes.status >= 300 {
		return usageResult{plan: "Command Code", message: fmt.Sprintf("Command Code subscriptions API error (%d)", subsRes.status)}
	}
	creditsBody := usageJSON(creditsRes.out)
	subsBody := usageJSON(subsRes.out)
	var planID string
	if d, ok := subsBody["data"].(map[string]any); ok {
		planID, _ = d["planId"].(string)
	}
	plan := commandCodePlanNames[planID]
	if plan == "" {
		plan = firstNonEmptyStr(planID, "Command Code")
	}
	capVal := commandCodePlanCaps[planID]
	var credits map[string]any
	if c, ok := creditsBody["credits"].(map[string]any); ok {
		credits = c
	}
	remaining := usageNum(credits["monthlyCredits"], 0) + usageNum(credits["purchasedCredits"], 0) + usageNum(credits["freeCredits"], 0)
	used, total := 0.0, remaining
	if capVal > 0 {
		used = math.Max(0, capVal-remaining)
		total = capVal
	}
	var periodEnd any
	if d, ok := subsBody["data"].(map[string]any); ok {
		periodEnd = d["currentPeriodEnd"]
	}
	quotas := map[string]any{
		"Credits": map[string]any{
			"used": used, "total": total, "remaining": remaining,
			"unlimited": capVal <= 0, "resetAt": usageResetTimeToNil(periodEnd),
		},
	}
	var windows map[string]any
	if w, ok := creditsBody["windowLimits"].(map[string]any); ok {
		windows = w
	}
	if w := commandCodeWindow(windows["fiveHour"]); w != nil {
		quotas["Session (5h)"] = w
	}
	if w := commandCodeWindow(windows["weekly"]); w != nil {
		quotas["Weekly"] = w
	}
	return usageResult{plan: plan, quotas: quotas}
}

// ---------- ollama cloud: GET /api/usage + POST /api/me ----------

// ollamaRatioQuota mirrors upstream ratioQuota: `usage` is a 0..1 ratio, and
// no absolute `remaining` is set because the UI reads remainingPercentage.
func ollamaRatioQuota(ratio float64, resetAt string) map[string]any {
	used := math.Round(math.Max(0, math.Min(1, ratio)) * 100)
	quota := map[string]any{
		"used": used, "total": float64(100), "remainingPercentage": 100 - used,
		"resetAt": nil, "unlimited": false,
	}
	if resetAt != "" {
		quota["resetAt"] = resetAt
	}
	return quota
}

// ollamaLimitWindows mirrors upstream OLLAMA_LIMIT_WINDOWS. The monthly entry
// is not optional: a free Ollama account reports `limits.monthly` and nothing
// else, so a port that only reads session/weekly reports "no usage limits" for
// exactly the accounts that do have a quota.
var ollamaLimitWindows = []struct{ Key, Label string }{
	{"session", "Session (5h)"},
	{"weekly", "Weekly (7d)"},
	{"monthly", "Monthly"},
}

// nextMonthlyResetFromSignup mirrors upstream nextMonthlyResetFromSignup:
// "usage resets monthly from the date you signed up" (ollama.com/pricing).
func nextMonthlyResetFromSignup(createdAt string, now time.Time) string {
	anchor, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return ""
	}
	anchor = anchor.UTC()
	elapsed := (now.Year()-anchor.Year())*12 + int(now.Month()) - int(anchor.Month())
	for i := max(elapsed, 0); i <= elapsed+1; i++ {
		candidate := addUTCMonths(anchor, i)
		if candidate.After(now) {
			return candidate.Format(time.RFC3339)
		}
	}
	return ""
}

// addUTCMonths clamps the day to the target month's length (Jan 31 + 1 month
// is Feb 28/29), matching upstream addUtcMonths.
func addUTCMonths(t time.Time, months int) time.Time {
	total := int(t.Month()) - 1 + months
	year := t.Year() + total/12
	month := time.Month(total%12 + 1)
	if total%12 < 0 {
		year = t.Year() + (total-11)/12
		month = time.Month(total%12 + 13)
	}
	lastDay := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	return time.Date(year, month, min(t.Day(), lastDay), t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
}

// Ollama Cloud endpoints. Vars, not consts, so tests can point them at a
// local server.
var (
	ollamaUsageURL = "https://ollama.com/api/usage"
	ollamaMeURL    = "https://ollama.com/api/me"
)

func fetchOllamaUsage(ctx context.Context, apiKey string) usageResult {
	if strings.TrimSpace(apiKey) == "" {
		return usageResult{message: "Ollama Cloud API key not available."}
	}
	headers := map[string]string{
		"Authorization": "Bearer " + strings.TrimSpace(apiKey),
		"Accept":        "application/json",
	}
	status, _, out, err := usageGet(ctx, ollamaUsageURL, headers)
	if err != nil {
		return usageResult{message: fmt.Sprintf("Ollama Cloud error: %v", err)}
	}
	if status == 401 || status == 403 {
		return usageResult{message: "Ollama Cloud API key invalid or expired."}
	}
	if status < 200 || status >= 300 {
		return usageResult{message: fmt.Sprintf("Ollama Cloud usage API error (%d).", status)}
	}
	data := usageJSON(out)
	if data == nil {
		return usageResult{message: "Ollama Cloud usage response was not JSON."}
	}
	plan := "Ollama Cloud"
	meHeaders := map[string]string{
		"Authorization":  "Bearer " + strings.TrimSpace(apiKey),
		"Accept":         "application/json",
		"Content-Length": "0",
	}
	// /api/me also carries the raw plan name and signup date. The free plan
	// resets monthly from signup, and Ollama exposes no reset timestamp, so this
	// is the only source for the monthly row's reset.
	mePlanRaw, meCreatedAt := "", ""
	if s, _, meOut, meErr := usageDo(ctx, http.MethodPost, ollamaMeURL, meHeaders, nil); meErr == nil && s >= 200 && s < 300 {
		if me := usageJSON(meOut); me != nil {
			mePlanRaw = strings.TrimSpace(usageStr(me["Plan"]))
			meCreatedAt = usageStr(me["CreatedAt"])
			if mePlanRaw != "" {
				plan = strings.ToUpper(mePlanRaw[:1]) + strings.ToLower(mePlanRaw[1:])
			}
		}
	}
	monthlyResetAt := ""
	if strings.EqualFold(mePlanRaw, "free") && meCreatedAt != "" {
		monthlyResetAt = nextMonthlyResetFromSignup(meCreatedAt, time.Now().UTC())
	}
	var limits map[string]any
	if l, ok := data["limits"].(map[string]any); ok {
		limits = l
	}
	quotas := make(map[string]any, len(ollamaLimitWindows))
	for _, window := range ollamaLimitWindows {
		entry, ok := limits[window.Key].(map[string]any)
		if !ok {
			continue
		}
		raw, present := entry["usage"]
		if !present || raw == nil {
			continue
		}
		ratio, ok := usageNumOK(raw)
		if !ok {
			continue
		}
		resetAt := ""
		if window.Key == "monthly" {
			resetAt = monthlyResetAt
		}
		quotas[window.Label] = ollamaRatioQuota(ratio, resetAt)
	}
	if len(quotas) == 0 {
		return usageResult{plan: plan, message: "Ollama Cloud connected. No usage limits reported.", quotas: map[string]any{}}
	}
	return usageResult{plan: plan, quotas: quotas}
}

func usageNumOK(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		if !math.IsNaN(n) && !math.IsInf(n, 0) {
			return n, true
		}
	case string:
		if s := strings.TrimSpace(n); s != "" {
			if f, err := strconv.ParseFloat(s, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
				return f, true
			}
		}
	}
	return 0, false
}

// ---------- qoder: GET openapi quota/usage ----------

func fetchQoderUsage(ctx context.Context, accessToken string) usageResult {
	return fetchQoderUsageAt(ctx, accessToken, "https://openapi.qoder.sh/api/v2/quota/usage")
}

// fetchQoderUsageAt hits the provider's own openapi host, so Qoder and
// Qoder CN each read their own quota instead of sharing one endpoint.
func fetchQoderUsageAt(ctx context.Context, accessToken, usageURL string) usageResult {
	if strings.TrimSpace(accessToken) == "" {
		return usageResult{message: "Qoder usage unavailable: no access token", bare: true}
	}
	status, _, out, err := usageGet(ctx, usageURL, map[string]string{
		"Authorization": "Bearer " + strings.TrimSpace(accessToken),
		"Accept":        "application/json",
	})
	if err != nil {
		return usageResult{message: fmt.Sprintf("Qoder connected. Unable to fetch usage: %v", err), bare: true}
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		// The token is dead, not rate-limited. Qoder device tokens cannot be
		// refreshed — center.qoder.sh answers 403 for device tokens, which
		// upstream's own shared/qoder/constants.js documents — so re-authorizing
		// is the only way back. Say that instead of leaving a bare status code.
		return usageResult{
			plan:    "Qoder",
			message: "Qoder authentication expired. Please re-authorize this connection.",
			bare:    true,
		}
	}
	if status < 200 || status >= 300 {
		return usageResult{message: fmt.Sprintf("Qoder connected. Usage fetch returned %d.", status), bare: true}
	}
	body := usageJSON(out)
	if body == nil {
		return usageResult{message: "Qoder connected. Usage response was not JSON.", bare: true}
	}
	var expiresMs float64
	if e := usageNum(body["expiresAt"], 0); e > 0 {
		expiresMs = e
	}
	resetAt := ""
	if expiresMs > 0 {
		resetAt = time.UnixMilli(int64(expiresMs)).UTC().Format(time.RFC3339)
	}
	userQuota, _ := body["userQuota"].(map[string]any)
	orgQuota, _ := body["orgResourcePackage"].(map[string]any)
	strOr := func(m map[string]any, k, fallback string) string {
		if s, _ := m[k].(string); s != "" {
			return s
		}
		return fallback
	}
	quotas := map[string]any{
		"user": map[string]any{
			"total": usageNum(userQuota["total"], 0), "used": usageNum(userQuota["used"], 0),
			"remaining": usageNum(userQuota["remaining"], 0), "unit": strOr(userQuota, "unit", "credits"),
			"resetAt": resetAt,
		},
		"organization": map[string]any{
			"total": usageNum(orgQuota["total"], 0), "used": usageNum(orgQuota["used"], 0),
			"remaining": usageNum(orgQuota["remaining"], 0), "unit": strOr(orgQuota, "unit", "credits"),
			"resetAt": resetAt,
		},
	}
	extra := map[string]any{
		"totalUsagePercentage": usageNum(body["totalUsagePercentage"], 0),
	}
	if b, ok := body["isQuotaExceeded"].(bool); ok {
		extra["isQuotaExceeded"] = b
	}
	if expiresMs > 0 {
		extra["expiresAt"] = expiresMs
	}
	return usageResult{quotas: quotas, extra: extra}
}

// ---------- codebuddy-intl: POST billing meter ----------

var codebuddyIntlHeaders = map[string]string{
	"User-Agent":          "IDE/2.108.1 CodeBuddy/2.108.1",
	"X-Product":           "SaaS",
	"X-IDE-Type":          "IDE",
	"X-IDE-Name":          "IDE",
	"X-Requested-With":    "XMLHttpRequest",
	"X-Codebuddy-Request": "1",
	"Content-Type":        "application/json",
	"Accept":              "application/json",
}

func codebuddyNum(precise, plain any) float64 {
	if s, ok := precise.(string); ok && strings.TrimSpace(s) != "" {
		if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
			return f
		}
	}
	return usageNum(plain, 0)
}

func codebuddyCycleEndMs(acc map[string]any) float64 {
	if s := usageResetTime(acc["CycleEndTime"]); s != "" {
		if tm, err := time.Parse(time.RFC3339, s); err == nil {
			return float64(tm.UnixMilli())
		}
	}
	return math.Inf(1)
}

func codebuddyIsRefill(acc map[string]any) bool {
	ce := codebuddyCycleEndMs(acc)
	// DeductionEndTime arrives in unix MILLISECONDS (e.g. 1790429257000);
	// upstream compares it directly against the cycle-end ms.
	de, ok := usageFiniteNum(acc["DeductionEndTime"])
	if math.IsInf(ce, 1) || !ok {
		return false
	}
	return de-ce > float64(2*24*60*60*1000)
}

func codebuddyCadence(acc map[string]any) string {
	start, end := usageResetTime(acc["CycleStartTime"]), usageResetTime(acc["CycleEndTime"])
	if start != "" && end != "" {
		if ts, err1 := time.Parse(time.RFC3339, start); err1 == nil {
			if te, err2 := time.Parse(time.RFC3339, end); err2 == nil {
				days := te.Sub(ts).Hours() / 24
				if days <= 1.5 {
					return "Daily"
				}
				if days <= 10 {
					return "Weekly"
				}
			}
		}
	}
	return "Monthly"
}

func fetchCodeBuddyIntlUsage(ctx context.Context, accessToken, apiKey string) usageResult {
	token := firstNonEmptyStr(accessToken, apiKey)
	if token == "" {
		return usageResult{message: "CodeBuddy (codebuddy-intl) credential not available."}
	}
	headers := map[string]string{"Authorization": "Bearer " + token}
	for k, v := range codebuddyIntlHeaders {
		headers[k] = v
	}
	status, _, out, err := usageDo(ctx, http.MethodPost, "https://www.codebuddy.ai/v2/billing/meter/get-user-resource", headers, []byte("{}"))
	if err != nil {
		return usageResult{message: fmt.Sprintf("CodeBuddy (codebuddy-intl) error: %v", err)}
	}
	if status == 401 || status == 403 {
		return usageResult{message: "CodeBuddy CN credential invalid or expired."}
	}
	if status < 200 || status >= 300 {
		return usageResult{message: fmt.Sprintf("CodeBuddy CN quota API error (%d).", status)}
	}
	body := usageJSON(out)
	if body == nil {
		return usageResult{message: "CodeBuddy (codebuddy-intl) error: invalid JSON"}
	}
	if code := usageNum(body["code"], -1); code != 0 {
		msg, _ := body["msg"].(string)
		if msg == "" {
			msg = "unknown"
		}
		return usageResult{message: fmt.Sprintf("CodeBuddy CN quota error: %s", msg)}
	}
	var data map[string]any
	if d, ok := body["data"].(map[string]any); ok {
		if r, ok := d["Response"].(map[string]any); ok {
			data, _ = r["Data"].(map[string]any)
		}
	}
	var accounts []any
	if a, ok := data["Accounts"].([]any); ok {
		accounts = a
	}
	if len(accounts) == 0 {
		return usageResult{message: "CodeBuddy CN connected. No credit package found."}
	}
	var refills, bonuses []map[string]any
	for _, a := range accounts {
		if acc, ok := a.(map[string]any); ok {
			if codebuddyIsRefill(acc) {
				refills = append(refills, acc)
			} else {
				bonuses = append(bonuses, acc)
			}
		}
	}
	sort.SliceStable(refills, func(i, j int) bool { return codebuddyCycleEndMs(refills[i]) < codebuddyCycleEndMs(refills[j]) })
	sort.SliceStable(bonuses, func(i, j int) bool { return codebuddyCycleEndMs(bonuses[i]) < codebuddyCycleEndMs(bonuses[j]) })
	quotas := map[string]any{}
	seen := map[string]int{}
	for _, acc := range refills {
		base := codebuddyCadence(acc)
		seen[base]++
		name := base
		if seen[base] > 1 {
			name = fmt.Sprintf("%s %d", base, seen[base])
		}
		quotas[name] = map[string]any{
			"used":      codebuddyNum(acc["CycleCapacityUsedPrecise"], acc["CycleCapacityUsed"]),
			"total":     codebuddyNum(acc["CycleCapacitySizePrecise"], acc["CycleCapacitySize"]),
			"resetAt":   usageResetTimeToNil(acc["CycleEndTime"]),
			"unlimited": false, "recurring": true,
		}
	}
	for i, acc := range bonuses {
		quotas[fmt.Sprintf("Bonus Pack %d", i+1)] = map[string]any{
			"used":      codebuddyNum(acc["CapacityUsedPrecise"], acc["CapacityUsed"]),
			"total":     codebuddyNum(acc["CapacitySizePrecise"], acc["CapacitySize"]),
			"resetAt":   usageResetTimeToNil(acc["CycleEndTime"]),
			"unlimited": false, "recurring": false,
		}
	}
	plan := "CodeBuddy"
	base := map[string]any{}
	if len(refills) > 0 {
		base = refills[0]
	} else if len(accounts) > 0 {
		if m, ok := accounts[0].(map[string]any); ok {
			base = m
		}
	}
	if p, _ := base["PackageName"].(string); p != "" {
		plan = p
	} else if p, _ := base["SubProductName"].(string); p != "" {
		plan = p
	}
	return usageResult{plan: plan, quotas: quotas}
}

// ---------- kiro: codewhisperer getUsageLimits (3 attempts) ----------

const (
	kiroCwHost            = "https://codewhisperer.us-east-1.amazonaws.com"
	kiroQHost             = "https://q.us-east-1.amazonaws.com"
	kiroLimitsPath        = "/getUsageLimits"
	kiroProfileARNBuilder = "arn:aws:codewhisperer:us-east-1:638616132270:profile/AAAACCCCXXXX"
	kiroProfileARNSocial  = "arn:aws:codewhisperer:us-east-1:699475941385:profile/EHGA3GRVQMUK"
)

func resolveKiroDefaultProfileARN(authMethod string) string {
	if authMethod == "google" || authMethod == "github" {
		return kiroProfileARNSocial
	}
	return kiroProfileARNBuilder
}

func parseKiroQuotaData(data map[string]any) usageResult {
	var list []any
	if l, ok := data["usageBreakdownList"].([]any); ok {
		list = l
	}
	resetAt := usageResetTime(firstNonEmptyAny(data["nextDateReset"], data["resetDate"]))
	quotas := map[string]any{}
	for _, item := range list {
		b, ok := item.(map[string]any)
		if !ok {
			continue
		}
		rt, _ := b["resourceType"].(string)
		rt = strings.ToLower(strings.TrimSpace(rt))
		if rt == "" {
			rt = "unknown"
		}
		used := usageNum(b["currentUsageWithPrecision"], 0)
		total := usageNum(b["usageLimitWithPrecision"], 0)
		quotas[rt] = map[string]any{
			"used": used, "total": total, "remaining": total - used,
			"resetAt": usageResetTimeToNil(resetAtStr(resetAt)), "unlimited": false,
		}
		if ft, ok := b["freeTrialInfo"].(map[string]any); ok {
			fu := usageNum(ft["currentUsageWithPrecision"], 0)
			ftot := usageNum(ft["usageLimitWithPrecision"], 0)
			fr := usageResetTime(ft["freeTrialExpiry"])
			if fr == "" {
				fr = resetAt
			}
			quotas[rt+"_freetrial"] = map[string]any{
				"used": fu, "total": ftot, "remaining": ftot - fu,
				"resetAt": usageResetTimeToNil(fr), "unlimited": false,
			}
		}
	}
	plan := "Kiro"
	if sub, ok := data["subscriptionInfo"].(map[string]any); ok {
		if title, _ := sub["subscriptionTitle"].(string); title != "" {
			plan = title
		}
	}
	return usageResult{plan: plan, quotas: quotas}
}

func resetAtStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func fetchKiroUsage(ctx context.Context, accessToken string, psd map[string]any) usageResult {
	// Without a credential every attempt sends an empty bearer and comes back as
	// an auth rejection, which reads as "your token expired" when the real
	// problem is that the connection was saved without one.
	if strings.TrimSpace(accessToken) == "" {
		return usageResult{message: "No Kiro credential is stored for this connection.", quotas: map[string]any{}}
	}
	authMethod, _ := psd["authMethod"].(string)
	if authMethod == "" {
		authMethod = "builder-id"
	}
	extra := map[string]string{}
	if authMethod == "api_key" {
		extra["tokentype"] = "API_KEY"
	} else if authMethod == "external_idp" {
		extra["TokenType"] = "EXTERNAL_IDP"
	}
	var profileARN string
	if authMethod == "api_key" {
		profileARN, _ = psd["profileArn"].(string)
	} else {
		profileARN, _ = psd["profileArn"].(string)
		if profileARN == "" {
			profileARN = resolveKiroDefaultProfileARN(authMethod)
		}
	}
	params := url.Values{"isEmailRequired": {"true"}, "origin": {"AI_EDITOR"}, "resourceType": {"AGENTIC_REQUEST"}}
	type attempt struct {
		name    string
		method  string
		url     string
		headers map[string]string
		body    []byte
	}
	postBody := map[string]any{"origin": "AI_EDITOR", "resourceType": "AGENTIC_REQUEST"}
	if profileARN != "" {
		postBody["profileArn"] = profileARN
	}
	postBytes, _ := json.Marshal(postBody)
	qParams := url.Values{"origin": {"AI_EDITOR"}, "resourceType": {"AGENTIC_REQUEST"}}
	if profileARN != "" {
		qParams.Set("profileArn", profileARN)
	}
	base := map[string]string{
		"Authorization": "Bearer " + accessToken,
		"Accept":        "application/json",
	}
	cwGetHeaders := map[string]string{}
	for k, v := range base {
		cwGetHeaders[k] = v
	}
	cwGetHeaders["x-amz-user-agent"] = "aws-sdk-js/1.0.0 KiroIDE"
	cwGetHeaders["user-agent"] = "aws-sdk-js/1.0.0 KiroIDE"
	for k, v := range extra {
		cwGetHeaders[k] = v
	}
	cwPostHeaders := map[string]string{}
	for k, v := range base {
		cwPostHeaders[k] = v
	}
	cwPostHeaders["Content-Type"] = "application/x-amz-json-1.0"
	cwPostHeaders["x-amz-target"] = "AmazonCodeWhispererService.GetUsageLimits"
	for k, v := range extra {
		cwPostHeaders[k] = v
	}
	qGetHeaders := map[string]string{}
	for k, v := range base {
		qGetHeaders[k] = v
	}
	for k, v := range extra {
		qGetHeaders[k] = v
	}
	attempts := []attempt{
		{"codewhisperer-get", http.MethodGet, kiroCwHost + kiroLimitsPath + "?" + params.Encode(), cwGetHeaders, nil},
		{"codewhisperer-post", http.MethodPost, kiroCwHost, cwPostHeaders, postBytes},
		{"q-get", http.MethodGet, kiroQHost + kiroLimitsPath + "?" + qParams.Encode(), qGetHeaders, nil},
	}
	sawAuthError := false
	var lastErr string
	for _, a := range attempts {
		status, _, out, err := usageDo(ctx, a.method, a.url, a.headers, a.body)
		if err != nil {
			lastErr = a.name + ":" + err.Error()
			continue
		}
		if status < 200 || status >= 300 {
			t := strings.TrimSpace(string(out))
			if status == 401 || status == 403 {
				sawAuthError = true
			}
			lastErr = fmt.Sprintf("%s:%d", a.name, status)
			if t != "" {
				if len(t) > 120 {
					t = t[:120]
				}
				lastErr += ":" + t
			}
			continue
		}
		if data := usageJSON(out); data != nil {
			return parseKiroQuotaData(data)
		}
		lastErr = a.name + ":invalid JSON"
	}
	if sawAuthError && authMethod == "idc" {
		return usageResult{message: "Kiro quota API is unavailable for the current AWS IAM Identity Center session. Chat may still work. If this persists after renewing your session, reconnect Kiro.", quotas: map[string]any{}}
	}
	if sawAuthError && (authMethod == "google" || authMethod == "github") {
		return usageResult{message: "Kiro quota API authentication expired. Chat may still work.", quotas: map[string]any{}}
	}
	if sawAuthError {
		return usageResult{message: "Kiro quota API rejected the current token. Chat may still work.", quotas: map[string]any{}}
	}
	if lastErr != "" {
		return usageResult{message: fmt.Sprintf("Unable to fetch Kiro usage right now. (%s)", lastErr), quotas: map[string]any{}}
	}
	return usageResult{message: "Unable to fetch Kiro usage right now.", quotas: map[string]any{}}
}

// ---------- grok-cli: billing + user ----------

const (
	grokCliVersion          = providers.GrokCLIVersion
	grokCliClientIdentifier = providers.GrokCLIClientIdentifier
	grokCliUserAgent        = providers.GrokCLIUserAgent
	grokCliBillingURL       = "https://cli-chat-proxy.grok.com/v1/billing?format=credits"
	grokCliUserURL          = "https://cli-chat-proxy.grok.com/v1/user?include=subscription"
)

func grokCliHeaders(accessToken string, psd map[string]any) map[string]string {
	h := map[string]string{
		"Authorization":            "Bearer " + accessToken,
		"Accept":                   "application/json",
		"User-Agent":               grokCliUserAgent,
		"x-xai-token-auth":         "xai-grok-cli",
		"x-grok-client-identifier": grokCliClientIdentifier,
		"x-grok-client-version":    grokCliVersion,
		"x-grok-client-mode":       "headless",
	}
	if email := psdStr(psd, "email"); email != "" {
		h["x-email"] = email
	}
	if uid := psdStr(psd, "userId", "principalId"); uid != "" {
		h["x-userid"] = uid
	}
	return h
}

func grokMakeQuota(used, total float64, resetAt string) map[string]any {
	// Mirror upstream makeQuota: total<=0 renders as an unlimited row with
	// total=0 (the FE treats total===0 as unlimited).
	if total <= 0 {
		return map[string]any{
			"used": math.Max(0, used), "total": 0,
			"remainingPercentage": 100,
			"resetAt":             nil, "unlimited": true,
		}
	}
	return usageQuota(used, total, resetAt)
}

func grokSubscriptionTier(user, config map[string]any) string {
	maps := []map[string]any{user, config}
	for i, m := range maps {
		for _, k := range []string{"subscriptionTier", "subscription_tier", "tier"} {
			if k == "tier" && i == 1 {
				continue
			}
			if s, _ := m[k].(string); strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
		if sub, ok := m["subscription"].(map[string]any); ok {
			if s, _ := sub["tier"].(string); strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

func grokResolvePlan(user, config map[string]any) string {
	tier := grokSubscriptionTier(user, config)
	if tier != "" {
		parts := strings.Fields(strings.NewReplacer("_", " ", "-", " ").Replace(tier))
		for i, p := range parts {
			if len(p) > 0 {
				parts[i] = strings.ToUpper(p[:1]) + p[1:]
			}
		}
		return strings.Join(parts, " ")
	}
	if user["hasGrokCodeAccess"] == true {
		return "Grok Code"
	}
	return "Grok Build"
}

func grokPlanFromToken(accessToken string) string {
	parts := strings.Split(accessToken, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(payload, &m); err != nil {
		return ""
	}
	tiers := map[float64]string{
		0: "Free", 1: "SuperGrok", 2: "X Basic", 3: "X Premium",
		4: "X Premium Plus", 5: "SuperGrok Heavy", 6: "SuperGrok Lite",
	}
	if t, ok := m["tier"].(float64); ok {
		return tiers[t]
	}
	return ""
}

func fetchGrokCliUsage(ctx context.Context, accessToken string, psd map[string]any) usageResult {
	if strings.TrimSpace(accessToken) == "" {
		return usageResult{message: "Grok CLI access token not available."}
	}
	headers := grokCliHeaders(accessToken, psd)
	type fetchRes struct {
		status int
		out    []byte
		err    error
	}
	billingCh := make(chan fetchRes, 1)
	userCh := make(chan fetchRes, 1)
	go func() {
		s, _, o, e := usageGet(ctx, grokCliBillingURL, headers)
		billingCh <- fetchRes{s, o, e}
	}()
	go func() {
		s, _, o, e := usageGet(ctx, grokCliUserURL, headers)
		userCh <- fetchRes{s, o, e}
	}()
	billingRes, userRes := <-billingCh, <-userCh
	if billingRes.err != nil {
		return usageResult{message: fmt.Sprintf("Grok CLI error: %v", billingRes.err)}
	}
	if billingRes.status == 401 || billingRes.status == 403 {
		return usageResult{message: "Grok CLI authentication expired. Please re-authorize."}
	}
	if billingRes.status < 200 || billingRes.status >= 300 {
		msg := fmt.Sprintf("Grok CLI billing API error (%d)", billingRes.status)
		if t := strings.TrimSpace(string(billingRes.out)); t != "" {
			if len(t) > 200 {
				t = t[:200]
			}
			msg += ": " + t
		}
		return usageResult{message: msg}
	}
	billing := usageJSON(billingRes.out)
	if billing == nil {
		return usageResult{message: "Grok CLI billing response was not JSON."}
	}
	var user map[string]any
	if userRes.err == nil && userRes.status >= 200 && userRes.status < 300 {
		user = usageJSON(userRes.out)
	}
	quotas, _ := parseGrokCliBilling(billing, user)
	plan := grokPlanFromToken(accessToken)
	if plan == "" {
		cfg, _ := quotas["_config"].(map[string]any)
		plan = grokResolvePlan(user, cfg)
	}
	delete(quotas, "_config")
	if len(quotas) == 0 {
		msg := "Grok Build connected, but no credit allotment was returned. Free promo may be exhausted."
		if subAccessOf(billing, user) {
			msg = "Subscription access is active; Grok does not expose a numeric included quota."
		}
		return usageResult{plan: plan, message: msg}
	}
	// Attach periodEnd only when quotas render (message would hide the table).
	// NOTE: upstream grok-cli never returns periodEnd (plan+quotas only), so
	// unlike other fetchers we do NOT set res.extra here.
	return usageResult{plan: plan, quotas: quotas}
}

func subAccessOf(billing, user map[string]any) bool {
	tier := grokSubscriptionTier(user, billing)
	if config, ok := billing["config"].(map[string]any); ok {
		if tier == "" {
			tier = grokSubscriptionTier(user, config)
		}
	}
	return tier != "" && !regexp.MustCompile(`^(?i)(free|none|null)$`).MatchString(tier)
}

func parseGrokCliBilling(billing, user map[string]any) (map[string]any, string) {
	var config map[string]any
	if c, ok := billing["config"].(map[string]any); ok {
		config = c
	} else {
		config = billing
	}
	if config == nil {
		config = map[string]any{}
	}
	if user == nil {
		user = map[string]any{}
	}
	periodEnd := firstNonEmptyStr(
		usageResetTime(config["billingPeriodEnd"]), usageResetTime(config["billing_period_end"]),
		usageResetTime(nestedMap(config, "currentPeriod")["end"]),
		usageResetTime(config["resetAt"]), usageResetTime(config["resetsAt"]), usageResetTime(config["periodEnd"]),
		usageResetTime(billing["billingPeriodEnd"]), usageResetTime(billing["billing_period_end"]),
		usageResetTime(billing["resetAt"]), usageResetTime(billing["resetsAt"]), usageResetTime(billing["periodEnd"]),
	)
	quotas := map[string]any{"_config": config}
	tier := grokSubscriptionTier(user, config)
	subAccess := tier != "" && !regexp.MustCompile(`^(?i)(free|none|null)$`).MatchString(tier)

	monthlyLimit := usageNum(firstNonEmptyAny(config["monthlyLimit"], config["monthly_limit"], billing["monthlyLimit"], billing["monthly_limit"]), math.NaN())
	includedUsed := usageNum(firstNonEmptyAny(config["includedUsed"], config["included_used"], billing["includedUsed"], billing["included_used"]), math.NaN())
	totalUsed := usageNum(firstNonEmptyAny(config["totalUsed"], config["total_used"], billing["totalUsed"], billing["total_used"]), math.NaN())
	if !math.IsNaN(monthlyLimit) && monthlyLimit > 0 {
		used := 0.0
		if !math.IsNaN(includedUsed) {
			used = includedUsed
		} else if !math.IsNaN(totalUsed) {
			used = totalUsed
		}
		quotas["Monthly included"] = grokMakeQuota(used, monthlyLimit, periodEnd)
	}
	onDemandCap := usageNum(firstNonEmptyAny(config["onDemandCap"], billing["onDemandCap"]), math.NaN())
	onDemandUsed := usageNum(firstNonEmptyAny(config["onDemandUsed"], billing["onDemandUsed"]), math.NaN())
	if !math.IsNaN(onDemandCap) && onDemandCap > 0 {
		used := 0.0
		if !math.IsNaN(onDemandUsed) {
			used = math.Max(0, onDemandUsed)
		}
		quotas["On-demand"] = grokMakeQuota(used, onDemandCap, periodEnd)
	} else if !subAccess && !math.IsNaN(onDemandCap) && onDemandCap == 0 && !math.IsNaN(onDemandUsed) {
		quotas["On-demand"] = map[string]any{
			"used": 1, "total": 1, "remainingPercentage": 0,
			"resetAt": nil, "unlimited": false,
		}
		if periodEnd != "" {
			quotas["On-demand"].(map[string]any)["resetAt"] = periodEnd
		}
	}
	prepaid := usageNum(firstNonEmptyAny(config["prepaidBalance"], billing["prepaidBalance"]), math.NaN())
	if !math.IsNaN(prepaid) && prepaid > 0 {
		quotas["Prepaid"] = map[string]any{
			"used": 0, "total": prepaid, "remainingPercentage": 100,
			"resetAt": nil, "unlimited": false,
		}
	}
	usedPct := usageNum(firstNonEmptyAny(config["creditUsagePercent"], config["credit_usage_percent"], billing["creditUsagePercent"]), math.NaN())
	if !math.IsNaN(usedPct) && usedPct >= 0 {
		quotas["Weekly SuperGrok"] = grokMakeQuota(math.Max(0, math.Min(100, usedPct)), 100, periodEnd)
	}
	for _, bag := range creditBags(billing, config) {
		total := usageNum(firstNonEmptyAny(bag["total"], bag["limit"], bag["cap"], bag["allocation"], bag["amount"]), math.NaN())
		used := usageNum(firstNonEmptyAny(bag["used"], bag["spent"], bag["consumed"]), math.NaN())
		remaining := usageNum(firstNonEmptyAny(bag["remaining"], bag["balance"], bag["left"]), math.NaN())
		if !math.IsNaN(total) && total > 0 {
			resolved := 0.0
			if !math.IsNaN(used) {
				resolved = used
			} else if !math.IsNaN(remaining) {
				resolved = math.Max(0, total-remaining)
			}
			if _, exists := quotas["Credits"]; !exists {
				reset := usageResetTime(firstNonEmptyAny(bag["resetAt"], bag["resetsAt"], bag["end"]))
				if reset == "" {
					reset = periodEnd
				}
				quotas["Credits"] = grokMakeQuota(resolved, total, reset)
			}
		} else if !math.IsNaN(remaining) && remaining >= 0 {
			if _, exists := quotas["Credits"]; !exists {
				tot := remaining
				pct := 100.0
				if remaining <= 0 {
					tot = 1
					pct = 0
				}
				quotas["Credits"] = map[string]any{
					"used": 0, "total": tot, "remainingPercentage": pct,
					"resetAt": nil, "unlimited": false,
				}
				if periodEnd != "" {
					quotas["Credits"].(map[string]any)["resetAt"] = periodEnd
				}
			}
		}
	}
	return quotas, periodEnd
}

func creditBags(billing, config map[string]any) []map[string]any {
	// Upstream order: root.credits, root.creditBalance, root.usage,
	// config.credits, config.includedCredits, config.subscriptionCredits.
	ordered := []map[string]any{}
	for _, bag := range []map[string]any{
		nestedMap(billing, "credits"), nestedMap(billing, "creditBalance"), nestedMap(billing, "usage"),
		nestedMap(config, "credits"), nestedMap(config, "includedCredits"), nestedMap(config, "subscriptionCredits"),
	} {
		if len(bag) > 0 {
			ordered = append(ordered, bag)
		}
	}
	return ordered
}

func nestedMap(m map[string]any, key string) map[string]any {
	if n, ok := m[key].(map[string]any); ok {
		return n
	}
	return map[string]any{}
}

// ---------------------------------------------------------------------------
// Antigravity (dashboard presentation — parity with open-sse/services/usage/google.js)
// ---------------------------------------------------------------------------
// chat.RefreshAntigravityQuota parses every model and never checks the account
// tier. Upstream getAntigravityUsage instead:
//   - free-tier accounts skip per-model parsing entirely (fetchAvailableModels
//     returns misleading quota info there) and show weekly quotas only;
//   - paid accounts show a curated important-models list on a 1000 base;
//   - both get a best-effort weekly overlay with an exhaustion-reconcile rule.
// Without this, free-tier dashboards render 25 rows stuck at 100%.

var antigravityDashboardBaseURL = "https://cloudcode-pa.googleapis.com"

// antigravityDailyBaseURL serves the usage RPCs. Proven live: PROD
// fetchAvailableModels reports optimistic frac=1.0 while DAILY (the host the
// Next.js usage service uses) omits remainingFraction for exhausted models
// with the real reset time. Dashboard calls must use DAILY; chat routing
// intentionally stays on PROD and is untouched.
var antigravityDailyBaseURL = "https://daily-cloudcode-pa.googleapis.com"

const antigravityDashboardUA = "antigravity/ide/2.11.0 darwin/arm64"

var antigravityImportantModels = map[string]bool{
	"gemini-3.8-flash-high": true, "gemini-3.8-flash-medium": true, "gemini-3.8-flash-low": true,
	"gemini-3.7-flash-high": true, "gemini-3.7-flash-medium": true, "gemini-3.7-flash-low": true,
	"gemini-3.6-flash-high": true, "gemini-3.6-flash-medium": true, "gemini-3.6-flash-low": true,
	"gemini-3.5-flash-low": true, "gemini-3.5-flash-extra-low": true,
	"gemini-pro-agent": true, "gemini-3.1-pro-low": true,
	"claude-sonnet-4-6": true, "claude-opus-4-6-thinking": true,
	"gpt-oss-120b-medium":    true,
	"gemini-3.1-flash-image": true,
}

func antigravityDashboardHeaders(accessToken string) map[string]string {
	return map[string]string{
		"Authorization":    "Bearer " + accessToken,
		"User-Agent":       antigravityDashboardUA,
		"Content-Type":     "application/json",
		"X-Client-Name":    "antigravity",
		"X-Client-Version": "2.11.0",
	}
}

type antigravitySubInfo struct {
	projectID  string
	plan       string
	paidTierID string
}

// fetchAntigravitySubInfo mirrors getAntigravitySubscriptionInfo: one loadCodeAssist
// call reused for projectID + plan + tier detection.
func fetchAntigravitySubInfo(ctx context.Context, accessToken string) antigravitySubInfo {
	var sub antigravitySubInfo
	status, data, err := usagePost(ctx, antigravityDashboardBaseURL+"/v1internal:loadCodeAssist", map[string]any{
		"metadata": map[string]any{"ideType": 9, "platform": 2, "pluginType": 2},
		"mode":     1,
	}, antigravityDashboardHeaders(accessToken))
	if err != nil || status != http.StatusOK || data == nil {
		return sub
	}
	sub.projectID = antigravityProjectID(data["cloudaicompanionProject"])
	if tier, _ := data["currentTier"].(map[string]any); tier != nil {
		sub.plan, _ = tier["name"].(string)
	}
	if paid, _ := data["paidTier"].(map[string]any); paid != nil {
		sub.paidTierID, _ = paid["id"].(string)
	}
	return sub
}

func fetchAntigravityDashboardUsage(ctx context.Context, accessToken, projectID string) usageResult {
	sub := fetchAntigravitySubInfo(ctx, accessToken)
	pid := projectID
	if pid == "" {
		pid = sub.projectID
	}
	plan := sub.plan
	if plan == "" {
		plan = "Unknown"
	}
	headers := antigravityDashboardHeaders(accessToken)
	reqBody := map[string]any{}
	if pid != "" {
		reqBody["project"] = pid
	}
	status, body, err := usagePost(ctx, antigravityDailyBaseURL+"/v1internal:fetchAvailableModels", reqBody, headers)
	if err != nil {
		return usageResult{plan: plan, message: "Antigravity error: " + err.Error()}
	}
	if status == http.StatusForbidden {
		return usageResult{plan: plan, message: "Antigravity quota API access forbidden. Chat may still work."}
	}
	if status == http.StatusUnauthorized {
		return usageResult{plan: plan, message: "Antigravity quota API authentication expired. Chat may still work."}
	}
	if status < 200 || status >= 300 {
		return usageResult{plan: plan, message: fmt.Sprintf("Antigravity error: quota API returned %d.", status)}
	}
	quotas := map[string]any{}
	isFreeTier := sub.paidTierID == "" || sub.paidTierID == "free-tier"
	if !isFreeTier {
		if models, _ := body["models"].(map[string]any); models != nil {
			for modelKey, infoRaw := range models {
				info, _ := infoRaw.(map[string]any)
				if info == nil {
					continue
				}
				qi, _ := info["quotaInfo"].(map[string]any)
				if qi == nil {
					continue
				}
				if internal, _ := info["isInternal"].(bool); internal || !antigravityImportantModels[modelKey] {
					continue
				}
				frac := usageNum(qi["remainingFraction"], 0)
				total := 1000.0
				remaining := math.Round(total * frac)
				used := math.Max(0, total-remaining)
				dn, _ := info["displayName"].(string)
				if dn == "" {
					dn = modelKey
				}
				quotas[modelKey] = map[string]any{
					"used": used, "total": total,
					"resetAt":             usageResetTime(qi["resetTime"]),
					"remainingPercentage": frac * 100,
					"unlimited":           false,
					"displayName":         dn,
				}
			}
		}
	}
	// Best-effort weekly overlay — never breaks per-model results.
	// Hits DAILY retrieveUserQuotaSummary like upstream (PROD returns a
	// different weekly view) and parses weekly-only buckets like upstream
	// (no session rows — upstream shows 19 rows, not 21).
	if weekly := fetchAntigravityDashboardWeekly(ctx, accessToken, pid); len(weekly) > 0 {
		for key, wq := range weekly {
			if key == "gemini_weekly" {
				antigravityReconcileFamily(quotas, "gemini-", "image", wq)
			} else if key == "claude_gpt_weekly" {
				antigravityReconcileFamily(quotas, "claude-", "", wq)
			}
			quotas[key] = wq
		}

		// Reconcile gemini_session if all gemini models are exhausted (upstream google.js parity)
		if s, ok := quotas["gemini_session"].(map[string]any); ok {
			allGeminiExhausted := true
			var geminiCount int
			var maxResetAt string
			for k, v := range quotas {
				if strings.HasPrefix(k, "gemini-") && !strings.Contains(k, "image") {
					geminiCount++
					if vm, ok := v.(map[string]any); ok {
						if rem, ok := vm["remainingPercentage"].(float64); ok && rem > 0 {
							allGeminiExhausted = false
						}
						if rAt, ok := vm["resetAt"].(string); ok && rAt != "" {
							if maxResetAt == "" || rAt > maxResetAt {
								maxResetAt = rAt
							}
						}
					}
				}
			}
			if geminiCount > 0 && allGeminiExhausted {
				s["used"] = s["total"]
				s["remainingPercentage"] = 0.0
				if maxResetAt != "" {
					s["resetAt"] = maxResetAt
				}
			}
		}
	}
	return usageResult{plan: plan, quotas: quotas}
}

// fetchAntigravityDashboardWeekly mirrors antigravity-weekly.js: DAILY
// retrieveUserQuotaSummary, weekly-only buckets, first match per family wins.
func fetchAntigravityDashboardWeekly(ctx context.Context, accessToken, projectID string) map[string]any {
	reqBody := map[string]any{}
	if projectID != "" {
		reqBody["project"] = projectID
	}
	status, data, err := usagePost(ctx, antigravityDailyBaseURL+"/v1internal:retrieveUserQuotaSummary", reqBody, antigravityDashboardHeaders(accessToken))
	if err != nil || status < 200 || status >= 300 || data == nil {
		return nil
	}
	var groups []any
	if g, ok := data["groups"].([]any); ok && len(g) > 0 {
		groups = g
	} else if qs, ok := data["quotaSummary"].(map[string]any); ok {
		groups, _ = qs["groups"].([]any)
	}
	if len(groups) == 0 {
		return nil
	}
	type familyTarget struct {
		key         string
		displayName string
	}
	type familyConfig struct {
		pattern string
		weekly  familyTarget
		session familyTarget
	}
	configs := []familyConfig{
		{
			pattern: "gemini",
			weekly:  familyTarget{"gemini_weekly", "Gemini (Weekly)"},
			session: familyTarget{"gemini_session", "Gemini (5h)"},
		},
		{
			pattern: "claude",
			weekly:  familyTarget{"claude_gpt_weekly", "Claude & GPT (Weekly)"},
			session: familyTarget{"claude_gpt_session", "Claude & GPT (5h)"},
		},
	}
	result := map[string]any{}
	for _, gRaw := range groups {
		g, _ := gRaw.(map[string]any)
		if g == nil {
			continue
		}
		gName, _ := g["displayName"].(string)
		gNameLower := strings.ToLower(gName)
		buckets, _ := g["buckets"].([]any)
		for _, bRaw := range buckets {
			b, _ := bRaw.(map[string]any)
			if b == nil {
				continue
			}
			windowType := strings.ToLower(usageStr(b["window"]))
			bucketText := strings.ToLower(usageStr(b["bucketId"]) + " " + usageStr(b["displayName"]))
			isWeekly := windowType == "weekly" || strings.Contains(bucketText, "weekly")
			isSession := windowType == "5h" || strings.Contains(bucketText, "five hour") || strings.Contains(bucketText, "5h") || strings.Contains(bucketText, "daily") || windowType == "daily"
			if !isWeekly && !isSession {
				continue
			}
			disabled, _ := b["disabled"].(bool)
			if disabled && isWeekly {
				continue
			}
			var frac float64
			if disabled {
				frac = 0
			} else {
				var ok bool
				frac, ok = usageFiniteNum(b["remainingFraction"])
				if !ok {
					continue
				}
			}
			for _, cfg := range configs {
				if strings.Contains(gNameLower, cfg.pattern) || (cfg.pattern == "claude" && strings.Contains(gNameLower, "gpt")) {
					target := cfg.session
					if isWeekly {
						target = cfg.weekly
					}
					if _, exists := result[target.key]; exists {
						break
					}
					total := 1000.0
					remaining := math.Round(total * frac)
					used := math.Max(0, total-remaining)
					result[target.key] = map[string]any{
						"used":                used,
						"total":               total,
						"resetAt":             usageResetTime(b["resetTime"]),
						"remainingPercentage": frac * 100,
						"unlimited":           false,
						"displayName":         target.displayName,
					}
					break
				}
			}
		}
	}
	return result
}

// antigravityProjectID mirrors chat.extractProjectID (unexported there):
// cloudaicompanionProject arrives as a string id or an {id} object.
func antigravityProjectID(val any) string {
	if s, ok := val.(string); ok {
		return strings.TrimSpace(s)
	}
	if m, ok := val.(map[string]any); ok {
		if id, _ := m["id"].(string); id != "" {
			return strings.TrimSpace(id)
		}
	}
	return ""
}

// antigravityReconcileFamily mirrors the upstream reconcile: when every model of
// a family reads exhausted but weekly still claims availability, weekly is
// forced to exhausted (Free Starter tier misreports remainingFraction: 1).
func antigravityReconcileFamily(quotas map[string]any, prefix, exclude string, wqRaw any) {
	wq, _ := wqRaw.(map[string]any)
	if wq == nil {
		return
	}
	var family []map[string]any
	var maxReset string
	for k, v := range quotas {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		if exclude != "" && strings.Contains(k, exclude) {
			continue
		}
		m, _ := v.(map[string]any)
		if m == nil {
			continue
		}
		family = append(family, m)
		if rs, _ := m["resetAt"].(string); rs > maxReset {
			maxReset = rs
		}
	}
	if len(family) == 0 {
		return
	}
	for _, m := range family {
		if p, _ := m["remainingPercentage"].(float64); p != 0 {
			return
		}
	}
	if pct, _ := wq["remainingPercentage"].(float64); pct > 0 {
		total, _ := wq["total"].(float64)
		wq["used"] = total
		wq["remainingPercentage"] = 0.0
		if maxReset != "" {
			wq["resetAt"] = maxReset
		}
	}
}
