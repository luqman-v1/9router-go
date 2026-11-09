package executor

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	json "encoding/json/v2"

	fhttp "github.com/bogdanfinn/fhttp"

	"9router/proxy/internal/log"
)

// Quota on Gemini Web.
//
// The site publishes the account's allowance in two places, and neither one
// alone is the whole answer:
//
//   - the `qpEbW` RPC returns the credit buckets — how many credits are left
//     in the 5-hour window and in the weekly one, and when they reset;
//   - the GraphQL `getUsageInfo` query returns the same two windows as
//     percentages, with the exact refresh timestamps.
//
// The RPC is the one that carries the credit counts, and it only answers a
// signed-in session; an anonymous session has no allowance to report and gets
// nothing back. The GraphQL query needs the same credential.
//
// Reference: azrinaldi/gemini-stealth-bridge internal/gemini/client.go
// (GetQuota, enrichWithGraphqlUsagePools) — the RPC id, the two payloads and
// the bucket layout are that project's shape.

// geminiWebQuotaRPC is the RPC that answers "how much is left".
const geminiWebQuotaRPC = "qpEbW"

// geminiWebQuotaPayloads are the two bucket sets the RPC is asked for.
//
// The first is the Flash allowance and the second the Pro one. A session with
// no Pro allowance answers the second with nothing, which is not an error.
var geminiWebQuotaPayloads = []string{
	"[[[1,11],[2,11],[6,11]]]",
	"[[[1,4],[6,6],[1,15]]]",
}

// geminiWebQuotaActionLabels names the buckets by the site's own action ids.
var geminiWebQuotaActionLabels = map[int]string{
	4:  "Gemini Pro",
	11: "Gemini Flash",
	15: "Gemini Flash Thinking",
}

// geminiWebQuotaGraphQLPath is the GraphQL endpoint the usage pools come from.
const geminiWebQuotaGraphQLPath = "/_/BardChatUi/graphql/schemas/GEMINI_WEB_GRAPHQL/executeQuery"

// geminiWebQuotaQuerySignature is the site's query signature for the usage
// pools. It is a constant of the site's own schema, not a credential.
const geminiWebQuotaQuerySignature = "2/eV2urSc0npZclHCYNGf6nOVD14WHCs52VfsJoHiZgEs="

// geminiWebQuotaVariables are the entrypoints the usage query is asked for.
const geminiWebQuotaVariables = `{"g1Request":{"entrypoints":["GEMINI_USAGE_SETTINGS_UPGRADE_BANNER",` +
	`"GEMINI_USAGE_SETTINGS_AI_CREDITS_UPSELL_CARD","GEMINI_USAGE_SETTINGS_UPGRADE_AND_CREDITS_BANNER"]},` +
	`"workspaceRequest":{"surface":"GEMINI_CHAT_USAGE_LIMITS_UPSELL"}}`

// geminiWebQuotaWindow totals, used when the RPC does not report exact
// numbers.
//
// The 5-hour window holds 2,400 credits and the weekly pool 48,384. These
// are the site's own numbers for a paid account; the RPC's numbers win when
// it reports them, because the site is the authority on what an account
// actually has.
const (
	geminiWebQuotaFiveHourTotal = 2400
	geminiWebQuotaWeeklyTotal   = 48384
)

// GeminiWebQuotaBucket is one allowance window.
type GeminiWebQuotaBucket struct {
	// ID names the window: "5h", "weekly", or the RPC's own bucket id.
	ID string
	// Label is the human-readable name.
	Label string
	// Used is how much of the window is spent.
	Used int
	// Total is the window's size.
	Total int
	// Remaining is how much is left.
	Remaining int
	// ResetAt is when the window refreshes, RFC 3339.
	ResetAt string
	// Unlimited reports a bucket the site reports as unbounded.
	Unlimited bool
}

// GeminiWebQuota reads the account's allowance.
//
// An empty credential is not an error: an anonymous session has no allowance
// to report, and the caller is told so with an empty list rather than a
// failure that would look like the provider is broken.
func GeminiWebQuota(ctx context.Context, cookie string) ([]GeminiWebQuotaBucket, error) {
	cookie = strings.TrimSpace(cookie)
	if cookie == "" {
		return nil, nil
	}
	session := geminiWebSessionForCredential(cookie)
	buckets, err := session.quotaBuckets(ctx)
	if err != nil {
		return nil, err
	}
	return session.enrichWithUsagePools(ctx, buckets), nil
}

// quotaBuckets reads the credit buckets from the RPC.
func (s *geminiWebSession) quotaBuckets(ctx context.Context) ([]GeminiWebQuotaBucket, error) {
	var buckets []GeminiWebQuotaBucket
	for _, payload := range geminiWebQuotaPayloads {
		decoded, err := s.batchPayload(ctx, geminiWebQuotaRPC, payload)
		if err != nil {
			return nil, err
		}
		buckets = append(buckets, geminiWebQuotaBucketsFrom(decoded)...)
	}
	return buckets, nil
}

// geminiWebQuotaBucketsFrom reads the bucket list out of the RPC payload.
//
// The layout is the site's own: a list of buckets, each with an id list whose
// second member names the bucket, then the usage percentage, the reset
// timestamp, the total and the remaining credits.
func geminiWebQuotaBucketsFrom(payload any) []GeminiWebQuotaBucket {
	arr, ok := payload.([]any)
	if !ok || len(arr) == 0 {
		return nil
	}
	items, ok := arr[0].([]any)
	if !ok {
		return nil
	}
	var buckets []GeminiWebQuotaBucket
	for _, itemAny := range items {
		item, ok := itemAny.([]any)
		if !ok || len(item) < 6 {
			continue
		}
		idList, ok := item[0].([]any)
		if !ok || len(idList) < 2 {
			continue
		}
		actionID, ok := geminiWebIntAt(idList, 1)
		if !ok {
			continue
		}
		label, known := geminiWebQuotaActionLabels[actionID]
		if !known {
			continue
		}
		idParts := make([]string, 0, len(idList))
		for _, part := range idList {
			if number, ok := part.(float64); ok {
				idParts = append(idParts, fmt.Sprintf("%d", int(number)))
			}
		}
		total, _ := geminiWebIntAt(item, 4)
		remaining, _ := geminiWebIntAt(item, 5)
		bucket := GeminiWebQuotaBucket{
			ID:        strings.Join(idParts, "-"),
			Label:     label,
			Total:     total,
			Remaining: remaining,
			Used:      total - remaining,
			Unlimited: total == 0 && remaining == 0,
		}
		if usage, ok := item[2].(float64); ok {
			// The site's percentage is authoritative when it is there: it
			// counts what the window has spent, which is not the same as
			// total minus remaining once the window has rolled over.
			bucket.Used = int(math.Round(usage * float64(total) / 100))
		}
		if reset, ok := item[3].([]any); ok && len(reset) > 0 {
			if seconds, ok := reset[0].(float64); ok && seconds > 0 {
				bucket.ResetAt = time.Unix(int64(seconds), 0).UTC().Format(time.RFC3339)
			}
		}
		buckets = append(buckets, bucket)
	}
	return buckets
}

// enrichWithUsagePools folds the GraphQL windows into the credit buckets.
//
// The RPC reports the credit counts and the GraphQL query reports the same
// two windows as percentages with the exact refresh timestamps. A bucket
// whose count the RPC does not know is rebuilt from the percentage, and a
// 5-hour window that is larger than the weekly pool is capped by it — the
// weekly pool is the outer limit, so a 5-hour window cannot promise more
// than the week has left.
func (s *geminiWebSession) enrichWithUsagePools(ctx context.Context, buckets []GeminiWebQuotaBucket) []GeminiWebQuotaBucket {
	pools := s.usagePools(ctx)
	if len(pools) == 0 {
		return buckets
	}
	fiveHourTotal, fiveHourRemaining := geminiWebQuotaFiveHourTotal, -1
	weeklyTotal, weeklyRemaining := geminiWebQuotaWeeklyTotal, -1
	for _, bucket := range buckets {
		switch {
		case bucket.Total > 5000:
			weeklyTotal, weeklyRemaining = bucket.Total, bucket.Remaining
		case bucket.Total > 0:
			fiveHourTotal, fiveHourRemaining = bucket.Total, bucket.Remaining
		}
	}
	var enriched []GeminiWebQuotaBucket
	if pool, ok := pools["GXU_FIVE_HOURLY"]; ok {
		remaining := fiveHourRemaining
		if remaining < 0 {
			remaining = int(math.Round(float64(fiveHourTotal) * (1 - pool.PercentageUsed/100)))
		}
		if remaining < 0 {
			remaining = 0
		}
		enriched = append(enriched, GeminiWebQuotaBucket{
			ID:        "5h",
			Label:     "5h Window",
			Used:      fiveHourTotal - remaining,
			Total:     fiveHourTotal,
			Remaining: remaining,
			ResetAt:   pool.RefreshesAt,
		})
	}
	if pool, ok := pools["GXU_WEEKLY"]; ok {
		remaining := weeklyRemaining
		if remaining < 0 {
			remaining = int(math.Round(float64(weeklyTotal) * (1 - pool.PercentageUsed/100)))
		}
		if remaining < 0 {
			remaining = 0
		}
		enriched = append(enriched, GeminiWebQuotaBucket{
			ID:        "weekly",
			Label:     "Weekly Pool",
			Used:      weeklyTotal - remaining,
			Total:     weeklyTotal,
			Remaining: remaining,
			ResetAt:   pool.RefreshesAt,
		})
	}
	if len(enriched) != 2 {
		return buckets
	}
	// The weekly pool is the outer limit: a 5-hour window that promises more
	// than the week has left is a promise the site will not keep.
	if enriched[1].Remaining < enriched[0].Remaining {
		enriched[0].Remaining = enriched[1].Remaining
		enriched[0].Used = enriched[0].Total - enriched[0].Remaining
	}
	// The two windows the GraphQL query reports come first, and the credit
	// buckets the RPC reported follow: the windows are what the account can
	// spend, and the buckets are the per-model detail underneath them.
	return append(enriched, buckets...)
}

// geminiWebUsagePool is one window the GraphQL query reports.
type geminiWebUsagePool struct {
	// PoolType is the site's own name: GXU_FIVE_HOURLY or GXU_WEEKLY.
	PoolType string
	// PercentageUsed is how much of the window is spent.
	PercentageUsed float64
	// RefreshesAt is when the window refreshes.
	RefreshesAt string
}

// usagePools reads the two usage windows from the GraphQL query.
//
// A session that is not signed in answers with an empty viewer, which is not
// an error: the caller keeps the credit buckets it already has.
func (s *geminiWebSession) usagePools(ctx context.Context) map[string]geminiWebUsagePool {
	query := url.Values{}
	query.Set("query_signature", geminiWebQuotaQuerySignature)
	query.Set("variables", geminiWebQuotaVariables)
	request, err := fhttp.NewRequestWithContext(ctx, http.MethodGet,
		s.endpoints.origin+geminiWebQuotaGraphQLPath+"?"+query.Encode(), nil)
	if err != nil {
		return nil
	}
	values := geminiWebCORSValues(s.endpoints.origin, s.endpoints.referer())
	values["accept"] = "application/json"
	geminiWebSetOrdered(request.Header, geminiWebCORSOrder, values)
	response, err := s.stealth.do(request)
	if err != nil {
		return nil
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil
	}
	return geminiWebParseUsagePools(string(body))
}

// geminiWebParseUsagePools reads the pool list out of the GraphQL answer.
//
// The answer is a JSON array of chunks, each with the pools under
// data.viewer.rpcs.getUsageInfo.pools. A chunk whose viewer is null — the
// shape an anonymous session gets — carries nothing.
func geminiWebParseUsagePools(body string) map[string]geminiWebUsagePool {
	trimmed := strings.TrimSpace(body)
	if strings.HasPrefix(trimmed, ")]}'") {
		trimmed = strings.TrimSpace(trimmed[4:])
	}
	if trimmed == "" {
		return nil
	}
	var chunks []struct {
		Data struct {
			Viewer struct {
				Rpcs struct {
					GetUsageInfo struct {
						Pools []struct {
							PoolType       string  `json:"poolType"`
							PercentageUsed float64 `json:"percentageUsed"`
							Gxu            struct {
								WindowRefreshesAt string `json:"windowRefreshesAt"`
							} `json:"gxu"`
						} `json:"pools"`
					} `json:"getUsageInfo"`
				} `json:"rpcs"`
			} `json:"viewer"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(trimmed), &chunks); err != nil {
		return nil
	}
	pools := map[string]geminiWebUsagePool{}
	for _, chunk := range chunks {
		for _, pool := range chunk.Data.Viewer.Rpcs.GetUsageInfo.Pools {
			if pool.PoolType == "" {
				continue
			}
			pools[pool.PoolType] = geminiWebUsagePool{
				PoolType:       pool.PoolType,
				PercentageUsed: pool.PercentageUsed,
				RefreshesAt:    geminiWebNormalizeTimestamp(pool.Gxu.WindowRefreshesAt),
			}
		}
	}
	if len(pools) == 0 {
		return nil
	}
	return pools
}

// geminiWebNormalizeTimestamp renders the refresh timestamp in RFC 3339.
//
// The site sends an RFC 3339 string, but it may also send epoch seconds, and
// a timestamp the dashboard cannot parse is a timestamp the user cannot read.
func geminiWebNormalizeTimestamp(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if seconds, err := time.ParseDuration(trimmed + "s"); err == nil {
		return time.Unix(int64(seconds.Seconds()), 0).UTC().Format(time.RFC3339)
	}
	if _, err := time.Parse(time.RFC3339, trimmed); err == nil {
		return trimmed
	}
	return trimmed
}

// geminiWebSessionForCredential returns the session a cookie's quota reads
// use.
//
// The dashboard reads a quota for a stored credential, outside the chat path,
// so it has no request to build the session from. The session is keyed the
// same way the chat path keys it, so both share one connection and one token
// cache for the same credential.
func geminiWebSessionForCredential(credential string) *geminiWebSession {
	credential = strings.TrimSpace(credential)
	endpoints := geminiWebEndpointsFor(nil)
	key := credential + "\x00" + endpoints.origin
	geminiWebSessionMu.Lock()
	defer geminiWebSessionMu.Unlock()
	if session, ok := geminiWebSessions[key]; ok {
		return session
	}
	stealth, err := newGeminiWebStealthClient(nil, endpoints.origin, credential)
	if err != nil {
		log.Error("gemini-web", "could not build TLS client", "error", err)
	}
	session := &geminiWebSession{endpoints: endpoints, stealth: stealth, cookie: credential}
	geminiWebSessions[key] = session
	return session
}
