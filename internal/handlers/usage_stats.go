package handlers

import (
	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/translator"
	"9router/proxy/internal/usagetracker"
	"database/sql"
	json "encoding/json/v2"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// usagePeriod is the window /api/usage/stats aggregates over.
type usagePeriod struct {
	// daily reports whether the window is aggregated from usageDaily rows. The
	// two sub-day windows instead scan raw usageHistory, because a daily rollup
	// cannot express "since 14:00 yesterday".
	daily bool
	// days is the usageDaily row limit for a daily window.
	days int
	// since is the usageHistory cutoff for a raw-history window.
	since time.Time
	// shape names the window for the aggregate cache. It is decided here because
	// this is where the difference is actually known: `today` starts at midnight
	// and every poll in that day shares one start, while `24h` and `<n>h` are
	// measured back from now and move on every request. Deriving it later from
	// the instant alone cannot tell those apart.
	shape windowShape
}

// allUsageDays bounds an unbounded period. usageDaily holds one row per calendar
// day, so this covers every window this build can have written while keeping a
// query parameter from asking for an unbounded scan.
const allUsageDays = 3650

// resolveUsagePeriod maps the ?period parameter onto a window. The named
// shortcuts cover the dashboard presets; the <n>d and <n>h forms let a client ask
// for a window that is not one of them.
//
// A zero day count means "unbounded", which only the explicit `all` shortcut
// produces: a user-supplied 0 would otherwise silently read as the floor.
func resolveUsagePeriod(raw string, now time.Time) usagePeriod {
	if raw == "" {
		raw = "today"
	}

	switch raw {
	case "today":
		day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		return usagePeriod{since: day, shape: anchoredShape(day)}
	case "24h":
		return usagePeriod{since: now.Add(-24 * time.Hour), shape: slidingShape(24 * time.Hour)}
	case "all":
		return usagePeriod{daily: true, days: allUsageDays}
	}

	if n, ok := usagePeriodCount(raw, "d"); ok {
		return usagePeriod{daily: true, days: n}
	}
	if n, ok := usagePeriodCount(raw, "h"); ok {
		span := time.Duration(n) * time.Hour
		return usagePeriod{since: now.Add(-span), shape: slidingShape(span)}
	}

	// Unrecognized input falls back to the 7-day window rather than erroring: the
	// dashboard keeps working against an older gateway that does not know a
	// period, which is the common case when the SPA is newer than the server.
	return usagePeriod{daily: true, days: 7}
}

// usagePeriodCount parses the "<n><unit>" shorthand, accepting only a positive
// count with no sign or decimal part.
func usagePeriodCount(raw, unit string) (int, bool) {
	digits, ok := strings.CutSuffix(raw, unit)
	if !ok || digits == "" {
		return 0, false
	}
	n, err := strconv.Atoi(digits)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

type ProviderUsageItem struct {
	Requests         int     `json:"requests"`
	PromptTokens     int64   `json:"promptTokens"`
	CompletionTokens int64   `json:"completionTokens"`
	CachedTokens     int64   `json:"cachedTokens"`
	Cost             float64 `json:"cost"`
}

type ModelUsageItem struct {
	Requests         int     `json:"requests"`
	PromptTokens     int64   `json:"promptTokens"`
	CompletionTokens int64   `json:"completionTokens"`
	CachedTokens     int64   `json:"cachedTokens"`
	Cost             float64 `json:"cost"`
	RawModel         string  `json:"rawModel"`
	Provider         string  `json:"provider"`
	LastUsed         string  `json:"lastUsed"`
}

type AccountUsageItem struct {
	Requests         int     `json:"requests"`
	PromptTokens     int64   `json:"promptTokens"`
	CompletionTokens int64   `json:"completionTokens"`
	CachedTokens     int64   `json:"cachedTokens"`
	Cost             float64 `json:"cost"`
	RawModel         string  `json:"rawModel"`
	Provider         string  `json:"provider"`
	ConnectionID     string  `json:"connectionId"`
	AccountName      string  `json:"accountName"`
	LastUsed         string  `json:"lastUsed"`
}

type ApiKeyUsageItem struct {
	Requests         int     `json:"requests"`
	PromptTokens     int64   `json:"promptTokens"`
	CompletionTokens int64   `json:"completionTokens"`
	CachedTokens     int64   `json:"cachedTokens"`
	Cost             float64 `json:"cost"`
	RawModel         string  `json:"rawModel"`
	Provider         string  `json:"provider"`
	ApiKeyMasked     string  `json:"apiKeyMasked,omitempty"`
	KeyName          string  `json:"keyName"`
	ApiKeyKey        string  `json:"apiKeyKey"`
	LastUsed         string  `json:"lastUsed"`
}

type EndpointUsageItem struct {
	Requests         int     `json:"requests"`
	PromptTokens     int64   `json:"promptTokens"`
	CompletionTokens int64   `json:"completionTokens"`
	CachedTokens     int64   `json:"cachedTokens"`
	Cost             float64 `json:"cost"`
	Endpoint         string  `json:"endpoint"`
	RawModel         string  `json:"rawModel"`
	Provider         string  `json:"provider"`
	LastUsed         string  `json:"lastUsed"`
}

type UsageStatsResponse struct {
	TotalRequests         int                          `json:"totalRequests"`
	TotalPromptTokens     int64                        `json:"totalPromptTokens"`
	TotalCompletionTokens int64                        `json:"totalCompletionTokens"`
	TotalCachedTokens     int64                        `json:"totalCachedTokens"`
	TotalCost             float64                      `json:"totalCost"`
	ByProvider            map[string]ProviderUsageItem `json:"byProvider"`
	ByModel               map[string]ModelUsageItem    `json:"byModel"`
	ByAccount             map[string]AccountUsageItem  `json:"byAccount"`
	ByApiKey              map[string]ApiKeyUsageItem   `json:"byApiKey"`
	ByEndpoint            map[string]EndpointUsageItem `json:"byEndpoint"`
	ActiveRequests        []usagetracker.ActiveRequest `json:"activeRequests"`
	RecentRequests        []usagetracker.RecentRequest `json:"recentRequests"`
	ErrorProvider         string                       `json:"errorProvider"`
	Pending               usagetracker.PendingState    `json:"pending"`
}

// HandleUsageStats aggregates usage over the window named by ?period. Besides the
// dashboard presets ("today", "24h", "7d", "30d", "60d", "all") it accepts any
// <n>d or <n>h window, so the selector is not limited to a fixed set.
func HandleUsageStats(repo *db.Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		window := resolveUsagePeriod(r.URL.Query().Get("period"), time.Now())

		tracker := usagetracker.GetTracker()
		activeState := tracker.GetActiveState(repo)

		resp := UsageStatsResponse{
			ByProvider:     make(map[string]ProviderUsageItem),
			ByModel:        make(map[string]ModelUsageItem),
			ByAccount:      make(map[string]AccountUsageItem),
			ByApiKey:       make(map[string]ApiKeyUsageItem),
			ByEndpoint:     make(map[string]EndpointUsageItem),
			ActiveRequests: activeState.ActiveRequests,
			Pending:        activeState.Pending,
			ErrorProvider:  activeState.ErrorProvider,
		}

		// Load connection mapping for friendly account names
		connMap := make(map[string]string)
		if conns, err := repo.GetProviderConnections("", true); err == nil {
			for _, c := range conns {
				name := c.ID
				if c.Name != nil && *c.Name != "" {
					name = *c.Name
				} else if c.Email != nil && *c.Email != "" {
					name = *c.Email
				}
				connMap[c.ID] = name
			}
		}

		// Load provider nodes for custom reverse-proxy display names
		nodeNameMap := make(map[string]string)
		if nodes, err := repo.GetProviderNodes(); err == nil {
			for _, n := range nodes {
				if n.ID != "" && n.Name != nil && *n.Name != "" {
					nodeNameMap[n.ID] = *n.Name
				}
			}
		}

		// Friendly names for the API-key breakdown. The stored value is not
		// uniform across the shared database: rows this build writes keep only
		// the masked key, while rows the Next.js dashboard wrote keep the full
		// one. Index both forms so a stored row resolves either way.
		keyNames := make(map[string]string)
		if keys, err := repo.GetApiKeys(); err == nil {
			for _, k := range keys {
				if k.Name == nil || *k.Name == "" {
					continue
				}
				keyNames[k.Key] = *k.Name
				keyNames[handlerutil.MaskAPIKey(k.Key)] = *k.Name
			}
		}

		// addAPIKeyUsage folds one request into the byApiKey bucket. The bucket is
		// keyed by the stored key value, not by a freshly derived display mask:
		// every key an instance mints shares the same prefix, so a re-derived
		// mask collapsed a whole team key set into a single row and attributed
		// one key's usage to another (upstream v0.5.91, same class of fix).
		addAPIKeyUsage := func(apiKey, rawModel, provider, providerDisplay, timestamp string, requests int, promptTok, complTok, cachedTok int64, cost float64) {
			if apiKey == "" || apiKey == "***" {
				apiKey = "local-no-key"
			}
			bucketKey := apiKey + "|" + rawModel + "|" + provider
			cur := resp.ByApiKey[bucketKey]
			cur.RawModel = rawModel
			cur.Provider = providerDisplay
			cur.ApiKeyMasked = apiKey
			cur.ApiKeyKey = apiKey
			switch {
			case keyNames[apiKey] != "":
				cur.KeyName = keyNames[apiKey]
			case apiKey == "local-no-key":
				cur.KeyName = "Local (No Key)"
			default:
				cur.KeyName = apiKey[:min(8, len(apiKey))] + "..."
			}
			cur.Requests += requests
			cur.PromptTokens += promptTok
			cur.CompletionTokens += complTok
			cur.CachedTokens += cachedTok
			cur.Cost += cost
			if timestamp > cur.LastUsed {
				cur.LastUsed = timestamp
			}
			resp.ByApiKey[bucketKey] = cur
		}

		if window.daily {
			dailyRows, err := repo.GetUsageDailyRecent(window.days)
			if err != nil {
				handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
				return
			}
			for _, rowJSON := range dailyRows {
				var dayData map[string]any
				if err := json.Unmarshal([]byte(rowJSON), &dayData); err != nil {
					continue
				}

				// byProvider
				if bp, ok := dayData["byProvider"].(map[string]any); ok {
					for prov, pVal := range bp {
						if pm, ok := pVal.(map[string]any); ok {
							cur := resp.ByProvider[prov]
							cur.Requests += getMapInt(pm, "requests")
							cur.PromptTokens += getMapInt64(pm, "promptTokens")
							cur.CompletionTokens += getMapInt64(pm, "completionTokens")
							cur.CachedTokens += getMapInt64(pm, "cachedTokens")
							cur.Cost += getMapFloat(pm, "cost")
							resp.ByProvider[prov] = cur
						}
					}
				}

				// byModel
				if bm, ok := dayData["byModel"].(map[string]any); ok {
					for mk, mVal := range bm {
						if mm, ok := mVal.(map[string]any); ok {
							rawModel, _ := mm["rawModel"].(string)
							prov, _ := mm["provider"].(string)
							if rawModel == "" {
								parts := strings.Split(mk, "|")
								rawModel = parts[0]
								if len(parts) > 1 && prov == "" {
									prov = parts[1]
								}
							}
							statsKey := rawModel
							if prov != "" {
								statsKey = rawModel + " (" + prov + ")"
							}
							displayName := prov
							if dn, ok := nodeNameMap[prov]; ok && dn != "" {
								displayName = dn
							}

							cur := resp.ByModel[statsKey]
							cur.RawModel = rawModel
							cur.Provider = displayName
							cur.Requests += getMapInt(mm, "requests")
							cur.PromptTokens += getMapInt64(mm, "promptTokens")
							cur.CompletionTokens += getMapInt64(mm, "completionTokens")
							cur.CachedTokens += getMapInt64(mm, "cachedTokens")
							cur.Cost += getMapFloat(mm, "cost")
							resp.ByModel[statsKey] = cur
						}
					}
				}

				// byAccount
				if ba, ok := dayData["byAccount"].(map[string]any); ok {
					for connID, aVal := range ba {
						if am, ok := aVal.(map[string]any); ok {
							rawModel, _ := am["rawModel"].(string)
							prov, _ := am["provider"].(string)
							accName := connMap[connID]
							if accName == "" {
								if len(connID) > 8 {
									accName = "Account " + connID[:8] + "..."
								} else {
									accName = "Account " + connID
								}
							}
							accountKey := rawModel + " (" + prov + " - " + accName + ")"
							displayName := prov
							if dn, ok := nodeNameMap[prov]; ok && dn != "" {
								displayName = dn
							}

							cur := resp.ByAccount[accountKey]
							cur.RawModel = rawModel
							cur.Provider = displayName
							cur.ConnectionID = connID
							cur.AccountName = accName
							cur.Requests += getMapInt(am, "requests")
							cur.PromptTokens += getMapInt64(am, "promptTokens")
							cur.CompletionTokens += getMapInt64(am, "completionTokens")
							cur.CachedTokens += getMapInt64(am, "cachedTokens")
							cur.Cost += getMapFloat(am, "cost")
							resp.ByAccount[accountKey] = cur
						}
					}
				}

				// byApiKey. The daily payload carries no per-request timestamp
				// (the date is the row key), so LastUsed stays empty here — same
				// as the other daily branches.
				if bak, ok := dayData["byApiKey"].(map[string]any); ok {
					for _, kVal := range bak {
						km, ok := kVal.(map[string]any)
						if !ok {
							continue
						}
						rawModel, _ := km["rawModel"].(string)
						prov, _ := km["provider"].(string)
						apiKey, _ := km["apiKey"].(string)
						displayName := prov
						if dn, ok := nodeNameMap[prov]; ok && dn != "" {
							displayName = dn
						}
						addAPIKeyUsage(
							apiKey, rawModel, prov, displayName, "",
							getMapInt(km, "requests"),
							getMapInt64(km, "promptTokens"),
							getMapInt64(km, "completionTokens"),
							getMapInt64(km, "cachedTokens"),
							getMapFloat(km, "cost"),
						)
					}
				}
			}
		} else {
			// Sub-day windows read raw history: a daily rollup cannot express
			// "since 14:00 yesterday".
			//
			// The fold is served from usageWindowCache, which builds a window's
			// aggregate once and then advances it from a delta. The first read
			// costs what the handler always cost; every poll after it reads only
			// the rows written since the previous poll.
			agg, err := rawWindows.getRawWindow(repo, window)
			if err != nil {
				handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
				return
			}
			applyRawAggregate(&resp, agg, nodeNameMap, connMap, keyNames)
		}

		// Calculate total aggregates from byProvider
		for _, p := range resp.ByProvider {
			resp.TotalRequests += p.Requests
			resp.TotalPromptTokens += p.PromptTokens
			resp.TotalCompletionTokens += p.CompletionTokens
			resp.TotalCachedTokens += p.CachedTokens
			resp.TotalCost += p.Cost
		}

		// Build recent requests list (20 deduped from usageHistory)
		recentHistory, err := repo.GetRecentUsageHistory(60)
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		var dedupedRecent []usagetracker.RecentRequest
		seen := make(map[string]bool)
		for _, rh := range recentHistory {
			if rh.PromptTokens == 0 && rh.CompletionTokens == 0 {
				continue
			}
			min := ""
			if len(rh.Timestamp) >= 16 {
				min = rh.Timestamp[:16]
			}
			k := rh.Model + "|" + rh.Provider + "|" + strconv.Itoa(rh.PromptTokens) + "|" + strconv.Itoa(rh.CompletionTokens) + "|" + min
			if seen[k] {
				continue
			}
			seen[k] = true

			cachedTok := int(translator.CachedTokensFromJSON([]byte(rh.Tokens)))
			status := "ok"
			if rh.Status != "success" && rh.Status != "ok" && rh.Status != "" {
				status = rh.Status
			}

			dedupedRecent = append(dedupedRecent, usagetracker.RecentRequest{
				Timestamp:        rh.Timestamp,
				Model:            rh.Model,
				Provider:         rh.Provider,
				PromptTokens:     rh.PromptTokens,
				CompletionTokens: rh.CompletionTokens,
				CachedTokens:     cachedTok,
				Status:           status,
			})
			if len(dedupedRecent) >= 20 {
				break
			}
		}
		resp.RecentRequests = dedupedRecent

		handlerutil.WriteJSON(w, http.StatusOK, resp)
	}
}

// detailListItem is one collapsed Details-tab row.
type detailListItem struct {
	ID           string           `json:"id"`
	Timestamp    string           `json:"timestamp"`
	Provider     string           `json:"provider"`
	Model        string           `json:"model"`
	ConnectionID string           `json:"connectionId,omitempty"`
	Status       string           `json:"status"`
	Latency      map[string]int64 `json:"latency"`
	Tokens       map[string]any   `json:"tokens"`
	Account      string           `json:"account,omitempty"`
	Cost         float64          `json:"cost,omitempty"`
	Combo        string           `json:"combo,omitempty"`
	Protocol     string           `json:"protocol,omitempty"`
}

// HandleRequestDetails returns paged request detail objects for the Details tab.
//
// Only the values the table renders travel in the list. The request messages and
// response body stay in the database until /api/usage/request-detail/{id} is
// called for the row a user opens, which turns a 400 KB page into a 4 KB one.
func HandleRequestDetails(repo *db.Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit := 50
		offset := 0
		if lStr := r.URL.Query().Get("limit"); lStr != "" {
			if parsed, err := strconv.Atoi(lStr); err == nil && parsed > 0 && parsed <= 100 {
				limit = parsed
			}
		}
		if oStr := r.URL.Query().Get("offset"); oStr != "" {
			if parsed, err := strconv.Atoi(oStr); err == nil && parsed >= 0 {
				offset = parsed
			}
		}

		rows, total, err := repo.GetRequestDetailsPaged(limit, offset)
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}

		details := make([]detailListItem, 0, len(rows))
		for _, row := range rows {
			details = append(details, newDetailListItem(row))
		}

		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"details": details,
			"total":   total,
			"limit":   limit,
			"offset":  offset,
		})
	}
}

// newDetailListItem maps a collapsed row onto the wire shape, running the same
// cached_tokens normalization the full-payload path applied, so a row reads the
// same before and after the collapse.
func newDetailListItem(row db.RequestDetailListRow) detailListItem {
	tokens := map[string]any{}
	if row.TokensJSON != "" {
		if err := json.Unmarshal([]byte(row.TokensJSON), &tokens); err != nil {
			tokens = map[string]any{}
		}
	}
	if rawTokens, err := json.Marshal(tokens); err == nil {
		tokens["cached_tokens"] = float64(translator.CachedTokensFromJSON(rawTokens))
	}
	account := row.Account
	if account == "" {
		if row.ConnectionID != "" {
			account = row.ConnectionID
		} else {
			account = "Default"
		}
	}
	protocol := row.Protocol
	if protocol == "" {
		protocol = "OpenAI-Chat"
	}
	return detailListItem{
		ID:           row.ID,
		Timestamp:    row.Timestamp,
		Provider:     row.Provider,
		Model:        row.Model,
		ConnectionID: row.ConnectionID,
		Status:       row.Status,
		Latency:      map[string]int64{"ttft": row.LatencyTTFT, "total": row.LatencyTotal},
		Tokens:       tokens,
		Account:      account,
		Cost:         row.Cost,
		Combo:        row.Combo,
		Protocol:     protocol,
	}
}

// HandleRequestDetail serves the full stored payload for one request: the
// request messages and the response body the list deliberately omits.
func HandleRequestDetail(repo *db.Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		raw, err := repo.GetRequestDetailByID(id)
		if err != nil {
			// A detail that was never recorded, or has since been pruned by
			// retention, is a 404 rather than a server fault.
			if errors.Is(err, sql.ErrNoRows) {
				handlerutil.WriteJSONError(w, http.StatusNotFound, "request detail not found")
				return
			}
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}

		var item map[string]any
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, "stored request detail is unreadable")
			return
		}
		if tokens, ok := item["tokens"].(map[string]any); ok {
			if rawTokens, marshalErr := json.Marshal(tokens); marshalErr == nil {
				tokens["cached_tokens"] = float64(translator.CachedTokensFromJSON(rawTokens))
			}
		}

		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"detail": item})
	}
}

// Helper functions for map extraction
func getMapInt(m map[string]any, key string) int {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		}
	}
	return 0
}

func getMapInt64(m map[string]any, key string) int64 {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case float64:
			return int64(n)
		case int64:
			return n
		case int:
			return int64(n)
		}
	}
	return 0
}

func getMapFloat(m map[string]any, key string) float64 {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case float64:
			return n
		case int:
			return float64(n)
		}
	}
	return 0
}
