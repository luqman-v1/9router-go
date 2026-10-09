package media

import (
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
)

// XquikTweet represents a tweet item returned by Xquik API.
type XquikTweet struct {
	ID             string `json:"id"`
	Text           string `json:"text"`
	CreatedAt      string `json:"createdAt"`
	CreatedAtSnake string `json:"created_at"`
	Author         *struct {
		Username      string `json:"username"`
		Name          string `json:"name"`
		ProfileImgURL string `json:"profile_image_url"`
	} `json:"author"`
	Media []struct {
		MediaURL string `json:"mediaUrl"`
		URL      string `json:"url"`
		Type     string `json:"type"`
	} `json:"media"`
}

func (t *XquikTweet) GetPublishedAt() string {
	if t.CreatedAt != "" {
		return t.CreatedAt
	}
	return t.CreatedAtSnake
}

// XquikSearchResponse is the raw response envelope from Xquik API.
type XquikSearchResponse struct {
	Tweets      []XquikTweet `json:"tweets"`
	Data        []XquikTweet `json:"data"`
	HasNextPage bool         `json:"has_next_page"`
	NextCursor  string       `json:"next_cursor"`
}

func (h *MediaHandler) handleXquikSearch(w http.ResponseWriter, r *http.Request, body []byte, modelInfo *chat.ModelInfo) error {
	var reqBody struct {
		Query           string `json:"query"`
		Prompt          string `json:"prompt"`
		MaxResults      int    `json:"max_results"`
		SearchType      string `json:"search_type"`
		Language        string `json:"language"`
		ProviderOptions struct {
			QueryType string `json:"queryType"`
			Cursor    string `json:"cursor"`
		} `json:"provider_options"`
	}
	_ = json.Unmarshal(body, &reqBody)

	query := sanitizeSearchQuery(reqBody.Query)
	if query == "" {
		query = sanitizeSearchQuery(reqBody.Prompt)
	}
	if query == "" {
		return searchError(http.StatusBadRequest, "missing query in search request")
	}

	limit := reqBody.MaxResults
	if limit <= 0 {
		limit = defaultSearchMaxResults
	}
	if limit > maxSearchMaxResults {
		limit = maxSearchMaxResults
	}

	queryType := reqBody.ProviderOptions.QueryType
	if queryType != "" && queryType != "Latest" && queryType != "Top" {
		return searchError(http.StatusBadRequest, "Xquik queryType must be Latest or Top")
	}

	// Upstream parity (search.js credential loop): rotate through every active
	// account, locking the failed one per ClassifyError and trying the next.
	// A pinned x-connection-id is honored exactly once (no rotation).
	excludeIDs := []string{}
	usePinned := modelInfo.ConnectionID != ""
	var lastErr *searchUpstreamError
	for {
		conn, connData, err := h.ChatH.GetBestConnectionWithContext(r.Context(), modelInfo.Provider, modelInfo.ConnectionID, excludeIDs, modelInfo.Model)
		if err != nil || conn == nil {
			if lastErr != nil {
				return lastErr
			}
			return searchError(http.StatusBadRequest, fmt.Sprintf("no active connection for provider %s: %v", modelInfo.Provider, err))
		}
		attemptErr := h.tryXquikSearchConn(w, r, query, limit, queryType, reqBody.ProviderOptions.Cursor, reqBody.Language, modelInfo.Model, conn, connData)
		if attemptErr == nil {
			return nil
		}
		lastErr = attemptErr
		if usePinned || !attemptErr.Retryable {
			return lastErr
		}
		excludeIDs = append(excludeIDs, conn.ID)
	}
}

func (h *MediaHandler) tryXquikSearchConn(w http.ResponseWriter, r *http.Request, query string, limit int, queryType, cursor, language, model string, conn *models.ProviderConnection, connData *chat.ConnectionData) *searchUpstreamError {
	apiKey := chat.ExtractAPIKey(connData)
	if apiKey == "" {
		return searchError(http.StatusUnauthorized, "no API key found for Xquik connection")
	}

	baseURL := "https://xquik.com/api/v1/x/tweets/search"
	if providerCfg, cfgErr := h.ChatH.GetProviderConfig("xquik", connData); cfgErr == nil && providerCfg != nil && providerCfg.BaseURL != "" {
		baseURL = strings.TrimRight(providerCfg.BaseURL, "/")
		if !strings.HasSuffix(baseURL, "/api/v1/x/tweets/search") {
			baseURL = strings.TrimRight(baseURL, "/") + "/api/v1/x/tweets/search"
		}
	}

	qp := url.Values{}
	qp.Set("q", query)
	qp.Set("limit", strconv.Itoa(limit))
	if queryType != "" {
		qp.Set("queryType", queryType)
	}
	if cursor != "" {
		qp.Set("cursor", cursor)
	}
	if language != "" {
		qp.Set("language", language)
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, baseURL+"?"+qp.Encode(), nil)
	if err != nil {
		return searchError(http.StatusBadGateway, fmt.Sprintf("create Xquik request: %v", err))
	}
	req.Header.Set(constants.HeaderAccept, constants.ContentTypeJSON)
	req.Header.Set(constants.HeaderXAPIKey, apiKey)

	client, clientErr := h.ChatH.GetClientForConnection(connData)
	if clientErr != nil {
		return &searchUpstreamError{Message: clientErr.Error()}
	}
	if client == nil {
		client = h.Client
	}

	upstreamStart := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		directReq, err2 := http.NewRequestWithContext(r.Context(), http.MethodGet, baseURL+"?"+qp.Encode(), nil)
		if err2 == nil {
			directReq.Header = req.Header.Clone()
			if resp2, err3 := directHTTPClient.Do(directReq); err3 == nil {
				resp = resp2
				err = nil
			}
		}
	}
	if err != nil {
		return &searchUpstreamError{Status: http.StatusBadGateway, Message: fmt.Sprintf("Xquik upstream request failed: %v", err), Retryable: true}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		errText := strings.TrimSpace(string(errBody))
		if errText == "" {
			errText = resp.Status
		}
		backoff := 0
		if h.Repo != nil {
			backoff = h.Repo.GetConnectionBackoffLevel(conn.ID)
		}
		classification := providers.ClassifyError(resp.StatusCode, errText, backoff)
		if classification.ShouldFallback && h.Repo != nil && !handlerutil.IsProbeContext(r.Context()) {
			cooldownSec := max(classification.CooldownMs/1000, 1)
			_ = h.Repo.LockConnectionModel(conn.ID, model, cooldownSec, classification.NewBackoffLevel)
			log.Warn("search", "xquik account locked, trying next", "conn", conn.ID[:min(8, len(conn.ID))], "status", resp.StatusCode, "cooldown_s", cooldownSec)
		}
		return &searchUpstreamError{Status: resp.StatusCode, Message: fmt.Sprintf("Xquik upstream error (status %d): %s", resp.StatusCode, errText), Retryable: classification.ShouldFallback}
	}

	var xqResp XquikSearchResponse
	if err := json.UnmarshalRead(resp.Body, &xqResp); err != nil {
		return &searchUpstreamError{Status: http.StatusBadGateway, Message: fmt.Sprintf("decode Xquik response: %v", err), Retryable: true}
	}

	tweets := xqResp.Tweets
	if len(tweets) == 0 && len(xqResp.Data) > 0 {
		tweets = xqResp.Data
	}
	if len(tweets) > limit {
		tweets = tweets[:limit]
	}

	var results []map[string]any
	for idx, tweet := range tweets {
		var title, tweetURL, displayURL string
		var authorName any

		if tweet.Author != nil && tweet.Author.Username != "" {
			username := tweet.Author.Username
			title = fmt.Sprintf("@%s on X", username)
			tweetURL = fmt.Sprintf("https://x.com/%s/status/%s", username, tweet.ID)
			displayURL = fmt.Sprintf("x.com/%s/status/%s", username, tweet.ID)
			authorName = "@" + username
		} else {
			title = "X post"
			tweetURL = fmt.Sprintf("https://x.com/i/web/status/%s", tweet.ID)
			displayURL = fmt.Sprintf("x.com/i/web/status/%s", tweet.ID)
			authorName = nil
		}

		var imageURL any
		for _, m := range tweet.Media {
			if m.MediaURL != "" {
				imageURL = m.MediaURL
				break
			}
			if m.URL != "" {
				imageURL = m.URL
				break
			}
		}

		var pubAt any
		if pub := tweet.GetPublishedAt(); pub != "" {
			pubAt = pub
		}

		results = append(results, map[string]any{
			"title":        title,
			"url":          tweetURL,
			"display_url":  displayURL,
			"snippet":      tweet.Text,
			"position":     idx + 1,
			"published_at": pubAt,
			"favicon_url":  nil,
			"content": map[string]any{
				"format": "text",
				"text":   tweet.Text,
				"length": len(tweet.Text),
			},
			"metadata": map[string]any{
				"author":      authorName,
				"source_type": "x_post",
				"image_url":   imageURL,
			},
			"citation": map[string]any{
				"provider": "xquik",
				"rank":     idx + 1,
			},
		})
	}

	upstreamLatencyMs := time.Since(upstreamStart).Milliseconds()
	if h.Repo != nil {
		h.Repo.UpdateConnectionLastUsed(conn.ID)
		// Probes read production state; they never clear it (see
		// tts_forward.go for the same reasoning on the failure path).
		if !handlerutil.IsProbeContext(r.Context()) {
			_ = h.Repo.UnlockConnectionModel(conn.ID, model)
		}
	}

	var nextCursor any
	if xqResp.NextCursor != "" {
		nextCursor = xqResp.NextCursor
	}

	searchResponse := map[string]any{
		"provider": "xquik",
		"query":    query,
		"results":  results,
		"usage": map[string]any{
			"queries_used":          1,
			"search_cost_usd":       nil,
			"provider_credits_used": len(results),
		},
		"pagination": map[string]any{
			"has_more":    xqResp.HasNextPage,
			"next_cursor": nextCursor,
		},
		"answer": nil,
		"metrics": map[string]any{
			"response_time_ms":        upstreamLatencyMs,
			"upstream_latency_ms":     upstreamLatencyMs,
			"total_results_available": nil,
		},
		"errors": []any{},
	}

	handlerutil.WriteJSON(w, http.StatusOK, searchResponse)
	return nil
}
