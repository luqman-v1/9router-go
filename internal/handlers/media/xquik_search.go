package media

import (
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/handlerutil"
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

	query := reqBody.Query
	if query == "" {
		query = reqBody.Prompt
	}
	if query == "" {
		return fmt.Errorf("missing query in search request")
	}

	limit := reqBody.MaxResults
	if limit <= 0 {
		limit = 10
	}

	queryType := reqBody.ProviderOptions.QueryType
	if queryType == "" {
		queryType = "Latest"
	}
	if queryType != "Latest" && queryType != "Top" {
		return fmt.Errorf("Xquik queryType must be Latest or Top")
	}

	conn, connData, err := h.ChatH.GetBestConnection(modelInfo.Provider, modelInfo.ConnectionID, nil, modelInfo.Model)
	if err != nil || conn == nil {
		return fmt.Errorf("no active connection for provider %s: %w", modelInfo.Provider, err)
	}

	apiKey := chat.ExtractAPIKey(connData)
	if apiKey == "" {
		return fmt.Errorf("no API key found for Xquik connection")
	}

	baseURL := "https://xquik.com/api/v1/x/tweets/search"
	providerCfg, cfgErr := h.ChatH.GetProviderConfig(modelInfo.Provider, connData)
	if cfgErr == nil && providerCfg.BaseURL != "" {
		baseURL = strings.TrimRight(providerCfg.BaseURL, "/")
		if !strings.HasSuffix(baseURL, "/search") && !strings.HasSuffix(baseURL, "/tweets/search") {
			baseURL += "/api/v1/x/tweets/search"
		}
	}

	// Build query params
	qp := url.Values{}
	qp.Set("q", query)
	qp.Set("limit", strconv.Itoa(limit))
	qp.Set("queryType", queryType)
	if reqBody.ProviderOptions.Cursor != "" {
		qp.Set("cursor", reqBody.ProviderOptions.Cursor)
	}
	if reqBody.Language != "" {
		qp.Set("language", reqBody.Language)
	}

	fullURL := baseURL + "?" + qp.Encode()

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, fullURL, nil)
	if err != nil {
		return fmt.Errorf("create Xquik request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-api-key", apiKey)

	client := h.ChatH.GetClientForConnection(connData)
	if client == nil {
		client = h.Client
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Xquik upstream request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("Xquik upstream error (status %d): %s", resp.StatusCode, string(errBody))
	}

	var xqResp XquikSearchResponse
	if err := json.UnmarshalRead(resp.Body, &xqResp); err != nil {
		return fmt.Errorf("decode Xquik response: %w", err)
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

	h.Repo.UpdateConnectionLastUsed(conn.ID)

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
		"metrics": map[string]any{
			"total_results_available": nil,
		},
		"errors": []any{},
	}

	handlerutil.WriteJSON(w, http.StatusOK, searchResponse)
	return nil
}
