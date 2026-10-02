package media

import (
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/translator"
)

const (
	agContextBefore = 150
	agContextAfter  = 250
)

type antigravitySearchSource struct {
	Title    string
	Snippets []string
	Contexts []string
}

func expandSegment(text string, startIndex, endIndex int) string {
	if text == "" || startIndex < 0 || endIndex < startIndex || startIndex > len(text) {
		return ""
	}
	start := max(0, startIndex-agContextBefore)
	end := min(len(text), endIndex+agContextAfter)
	out := strings.TrimSpace(text[start:end])
	if start > 0 {
		if idx := strings.IndexByte(out, ' '); idx != -1 {
			out = "..." + out[idx+1:]
		} else {
			out = "..." + out
		}
	}
	if end < len(text) {
		if idx := strings.LastIndexByte(out, ' '); idx != -1 {
			out = out[:idx] + "..."
		} else {
			out = out + "..."
		}
	}
	return strings.TrimSpace(out)
}

func joinDeduped(items []string, sep string) string {
	seen := make(map[string]bool)
	var filtered []string
	for _, item := range items {
		trimmed := strings.TrimSpace(item)
		if trimmed != "" && !seen[trimmed] {
			seen[trimmed] = true
			filtered = append(filtered, trimmed)
		}
	}
	return strings.Join(filtered, sep)
}

// SearchResponse is the standardized envelope for /v1/search matching upstream 9router.
type SearchResponse struct {
	Provider string         `json:"provider"`
	Query    string         `json:"query"`
	Results  []SearchResult `json:"results"`
	Answer   *SearchAnswer  `json:"answer,omitempty"`
	Usage    SearchUsage    `json:"usage"`
	Metrics  SearchMetrics  `json:"metrics"`
	Errors   []any          `json:"errors"`
}

// SearchResult represents a single grounded search result.
type SearchResult struct {
	Title       string         `json:"title"`
	URL         string         `json:"url"`
	Snippet     string         `json:"snippet"`
	Position    int            `json:"position"`
	Score       any            `json:"score"`
	PublishedAt any            `json:"published_at"`
	FaviconURL  any            `json:"favicon_url"`
	Content     string         `json:"content"`
	Metadata    map[string]any `json:"metadata"`
	Citation    SearchCitation `json:"citation"`
	ProviderRaw any            `json:"provider_raw"`
}

// SearchCitation holds citation metadata for a search result.
type SearchCitation struct {
	Provider    string `json:"provider"`
	RetrievedAt string `json:"retrieved_at"`
	Rank        int    `json:"rank"`
}

// SearchAnswer holds the LLM-synthesized answer text and source model.
type SearchAnswer struct {
	Source string `json:"source"`
	Text   string `json:"text"`
	Model  string `json:"model"`
}

// SearchUsage tracks query and token consumption for the search request.
type SearchUsage struct {
	QueriesUsed   int `json:"queries_used"`
	SearchCostUSD any `json:"search_cost_usd"`
	LLMTokens     int `json:"llm_tokens"`
}

// SearchMetrics reports timings and result counts.
type SearchMetrics struct {
	ResponseTimeMS        int64 `json:"response_time_ms"`
	UpstreamLatencyMS     int64 `json:"upstream_latency_ms"`
	TotalResultsAvailable any   `json:"total_results_available"`
}

func (h *MediaHandler) handleAntigravitySearch(w http.ResponseWriter, r *http.Request, body []byte, modelInfo *chat.ModelInfo) error {
	var reqBody struct {
		Query      string `json:"query"`
		Prompt     string `json:"prompt"`
		MaxResults int    `json:"max_results"`
	}
	_ = json.Unmarshal(body, &reqBody)
	query := sanitizeSearchQuery(reqBody.Query)
	if query == "" {
		query = sanitizeSearchQuery(reqBody.Prompt)
	}
	if query == "" {
		return searchError(http.StatusBadRequest, "missing query in search request")
	}

	model := modelInfo.Model
	if model == "" || model == "antigravity" || model == "search" {
		model = "gemini-2.5-flash"
	}
	model = translator.NormalizeAntigravityModel(model)
	// gemini-2.5-flash is the Next.js search default (ag -> 2.5-flash); keep it
	// even though NormalizeAntigravityModel maps some 3.x aliases to tiered models.
	if model == "gemini-3-flash-agent" {
		model = "gemini-2.5-flash"
	}
	limit := reqBody.MaxResults
	if limit <= 0 {
		limit = defaultSearchMaxResults
	}
	if limit > maxSearchMaxResults {
		limit = maxSearchMaxResults
	}

	excludeIDs := []string{}
	usePinned := modelInfo.ConnectionID != ""
	var lastErr *searchUpstreamError
	for {
		conn, connData, err := h.ChatH.GetBestConnection("antigravity", modelInfo.ConnectionID, excludeIDs, model)
		if err != nil || conn == nil {
			if lastErr != nil {
				return searchError(lastErr.Status, fmt.Sprintf("all antigravity accounts failed, last error: %s", lastErr.Message))
			}
			return searchError(http.StatusBadRequest, fmt.Sprintf("no active connection for antigravity: %v", err))
		}

		attemptErr := h.tryAntigravitySearchConn(w, r, body, query, limit, model, conn, connData)
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

func (h *MediaHandler) tryAntigravitySearchConn(w http.ResponseWriter, r *http.Request, body []byte, query string, maxResults int, model string, conn *models.ProviderConnection, connData *chat.ConnectionData) *searchUpstreamError {
	startTime := time.Now()

	apiKey := chat.ExtractAPIKey(connData)
	if apiKey == "" {
		return searchError(http.StatusUnauthorized, fmt.Sprintf("no API key found for antigravity connection %s", conn.ID))
	}

	projectID := ""
	if conn != nil && conn.Data != "" {
		var d struct {
			ProjectID            string `json:"projectId"`
			ProviderSpecificData struct {
				ProjectID string `json:"projectId"`
			} `json:"providerSpecificData"`
		}
		if err := json.Unmarshal([]byte(conn.Data), &d); err == nil {
			if d.ProjectID != "" {
				projectID = d.ProjectID
			} else if d.ProviderSpecificData.ProjectID != "" {
				projectID = d.ProviderSpecificData.ProjectID
			}
		}
	}
	if projectID == "" && connData.ProviderSpecificData != nil {
		if pid, ok := connData.ProviderSpecificData["projectId"].(string); ok {
			projectID = pid
		} else if pid, ok := connData.ProviderSpecificData["project_id"].(string); ok {
			projectID = pid
		}
	}

	if projectID == "" {
		return searchError(http.StatusBadRequest, "Antigravity account has no projectId — reconnect the account")
	}

	providerCfg, err := h.ChatH.GetProviderConfig("antigravity", connData)
	if err != nil {
		return searchError(http.StatusInternalServerError, fmt.Sprintf("get antigravity config: %v", err))
	}

	// Build Antigravity search request body matching Next.js reference
	searchReq := map[string]any{
		"project":     projectID,
		"model":       model,
		"userAgent":   "antigravity",
		"requestType": "search",
		"request": map[string]any{
			"contents": []map[string]any{
				{
					"role": "user",
					"parts": []map[string]any{
						{"text": query},
					},
				},
			},
			"tools": []map[string]any{
				{"googleSearch": map[string]any{}},
			},
			"generationConfig": map[string]any{
				"temperature":     1.0,
				"maxOutputTokens": 8192,
			},
		},
	}

	searchBytes, err := json.Marshal(searchReq)
	if err != nil {
		return searchError(http.StatusInternalServerError, fmt.Sprintf("marshal antigravity search request: %v", err))
	}

	baseURL := strings.TrimRight(providerCfg.BaseURL, "/")
	targetURL := fmt.Sprintf("%s/v1internal:generateContent", baseURL)

	httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(searchBytes))
	if err != nil {
		return searchError(http.StatusInternalServerError, fmt.Sprintf("create antigravity search request: %v", err))
	}

	httpReq.Header.Set(constants.HeaderContentType, constants.ContentTypeJSON)
	httpReq.Header.Set(constants.HeaderAuthorization, "Bearer "+apiKey)
	httpReq.Header.Set("User-Agent", "antigravity/ide/2.11.0 darwin/arm64")

	client, clientErr := h.ChatH.GetClientForConnection(connData)
	if clientErr != nil {
		return &searchUpstreamError{Message: clientErr.Error()}
	}
	upstreamStart := time.Now()
	resp, err := client.Do(httpReq)
	if err != nil {
		// Transport-level failure (proxy block, DNS, reset): retry once direct
		// before giving up on this account. Never retry on a real upstream
		// status — the direct client hits the same mock/sandbox and would
		// mask the true error (e.g. 403 VALIDATION_REQUIRED became 200).
		directReq, err2 := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(searchBytes))
		if err2 == nil {
			directReq.Header = httpReq.Header.Clone()
			if resp2, err3 := directHTTPClient.Do(directReq); err3 == nil {
				if resp != nil {
					resp.Body.Close()
				}
				resp = resp2
				err = nil
			}
		}
	}
	upstreamLatencyMs := time.Since(upstreamStart).Milliseconds()
	if err != nil {
		return &searchUpstreamError{Status: http.StatusBadGateway, Message: fmt.Sprintf("antigravity search upstream request: %v", err), Retryable: true}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		errText := string(respBody)
		if errText == "" {
			errText = resp.Status
		}
		// Upstream parity (checkFallbackError): 403 VALIDATION_REQUIRED means
		// this Google account needs verification — an account-scoped lock, not
		// a request bug. Exclude it from rotation and try the next account.
		backoff := 0
		if h.Repo != nil {
			backoff = h.Repo.GetConnectionBackoffLevel(conn.ID)
		}
		classification := providers.ClassifyError(resp.StatusCode, errText, backoff)
		if classification.ShouldFallback && h.Repo != nil {
			lockModel := translator.NormalizeAntigravityModel(model)
			cooldownSec := max(classification.CooldownMs/1000, 1)
			_ = h.Repo.LockConnectionModel(conn.ID, lockModel, cooldownSec, classification.NewBackoffLevel)
			if lockModel != model {
				_ = h.Repo.LockConnectionModel(conn.ID, model, cooldownSec, classification.NewBackoffLevel)
			}
			log.Warn("search", "antigravity account locked, trying next", "conn", conn.ID[:min(8, len(conn.ID))], "status", resp.StatusCode, "cooldown_s", cooldownSec)
		}
		return &searchUpstreamError{Status: resp.StatusCode, Message: fmt.Sprintf("antigravity search upstream returned %d: %s", resp.StatusCode, errText), Retryable: classification.ShouldFallback}
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return &searchUpstreamError{Status: http.StatusBadGateway, Message: fmt.Sprintf("read antigravity search response: %v", err), Retryable: true}
	}

	// Parse grounding metadata
	var agResp struct {
		Response struct {
			Candidates []struct {
				Content *struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
				GroundingMetadata *struct {
					GroundingChunks []struct {
						Web *struct {
							URI   string `json:"uri"`
							URL   string `json:"url"`
							Title string `json:"title"`
						} `json:"web"`
					} `json:"groundingChunks"`
					GroundingSupports []struct {
						Segment *struct {
							Text       string `json:"text"`
							StartIndex int    `json:"startIndex"`
							EndIndex   int    `json:"endIndex"`
						} `json:"segment"`
						GroundingChunkIndices []int `json:"groundingChunkIndices"`
					} `json:"groundingSupports"`
				} `json:"groundingMetadata"`
			} `json:"candidates"`
			UsageMetadata *struct {
				TotalTokenCount int `json:"totalTokenCount"`
			} `json:"usageMetadata"`
		} `json:"response"`
		Candidates []struct {
			Content *struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
			GroundingMetadata *struct {
				GroundingChunks []struct {
					Web *struct {
						URI   string `json:"uri"`
						URL   string `json:"url"`
						Title string `json:"title"`
					} `json:"web"`
				} `json:"groundingChunks"`
				GroundingSupports []struct {
					Segment *struct {
						Text       string `json:"text"`
						StartIndex int    `json:"startIndex"`
						EndIndex   int    `json:"endIndex"`
					} `json:"segment"`
					GroundingChunkIndices []int `json:"groundingChunkIndices"`
				} `json:"groundingSupports"`
			} `json:"groundingMetadata"`
		} `json:"candidates"`
		UsageMetadata *struct {
			TotalTokenCount int `json:"totalTokenCount"`
		} `json:"usageMetadata"`
	}

	_ = json.Unmarshal(respBytes, &agResp)

	candidates := agResp.Response.Candidates
	if len(candidates) == 0 {
		candidates = agResp.Candidates
	}

	var answerText string
	sourcesMap := make(map[string]*antigravitySearchSource)
	var sourceOrder []string

	if len(candidates) > 0 {
		c := candidates[0]
		if c.Content != nil {
			for _, p := range c.Content.Parts {
				answerText += p.Text
			}
		}

		if c.GroundingMetadata != nil {
			chunks := c.GroundingMetadata.GroundingChunks
			byIndex := make([]*antigravitySearchSource, len(chunks))
			for i, ch := range chunks {
				if ch.Web != nil {
					u := ch.Web.URI
					if u == "" {
						u = ch.Web.URL
					}
					if u != "" {
						if _, exists := sourcesMap[u]; !exists {
							sourcesMap[u] = &antigravitySearchSource{
								Title: ch.Web.Title,
							}
							sourceOrder = append(sourceOrder, u)
						}
						byIndex[i] = sourcesMap[u]
					}
				}
			}

			for _, s := range c.GroundingMetadata.GroundingSupports {
				grounded := ""
				expanded := ""
				if s.Segment != nil {
					grounded = s.Segment.Text
					expanded = expandSegment(answerText, s.Segment.StartIndex, s.Segment.EndIndex)
					if expanded == "" {
						expanded = grounded
					}
				}
				for _, idx := range s.GroundingChunkIndices {
					if idx >= 0 && idx < len(byIndex) && byIndex[idx] != nil {
						src := byIndex[idx]
						if grounded != "" {
							src.Snippets = append(src.Snippets, grounded)
						}
						if expanded != "" {
							src.Contexts = append(src.Contexts, expanded)
						}
					}
				}
			}
		}
	}

	limit := maxResults
	if limit <= 0 {
		limit = 10
	}
	if len(sourceOrder) > limit {
		sourceOrder = sourceOrder[:limit]
	}

	retrievedAt := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	var results []SearchResult
	for idx, u := range sourceOrder {
		src := sourcesMap[u]
		snippet := joinDeduped(src.Snippets, " | ")
		if snippet == "" {
			snippet = src.Title
		}
		content := joinDeduped(src.Contexts, "\n\n")
		if content == "" {
			content = snippet
		}

		results = append(results, SearchResult{
			Title:       src.Title,
			URL:         u,
			Snippet:     snippet,
			Position:    idx + 1,
			Score:       nil,
			PublishedAt: nil,
			FaviconURL:  nil,
			Content:     content,
			Metadata:    map[string]any{},
			Citation: SearchCitation{
				Provider:    "antigravity",
				RetrievedAt: retrievedAt,
				Rank:        idx + 1,
			},
			ProviderRaw: nil,
		})
	}

	tokens := 0
	if agResp.Response.UsageMetadata != nil {
		tokens = agResp.Response.UsageMetadata.TotalTokenCount
	} else if agResp.UsageMetadata != nil {
		tokens = agResp.UsageMetadata.TotalTokenCount
	}

	if h.Repo != nil {
		h.Repo.UpdateConnectionLastUsed(conn.ID)
		_ = h.Repo.UnlockConnectionModel(conn.ID, model)
		_ = h.Repo.UnlockConnectionModel(conn.ID, translator.NormalizeAntigravityModel(model))
	}
	log.Info("request", "POST /v1/search", "provider", "antigravity", "model", model, "query", query, "results", len(results), "conn", conn.ID[:min(8, len(conn.ID))], "tokens", tokens)
	responseTimeMs := time.Since(startTime).Milliseconds()
	log.Info("usage", "logged", "provider", "antigravity", "model", model, "query", query, "results", len(results), "tokens", tokens)
	searchResponse := SearchResponse{
		Provider: "antigravity",
		Query:    query,
		Results:  results,
		Answer: &SearchAnswer{
			Source: "antigravity",
			Text:   answerText,
			Model:  model,
		},
		Usage: SearchUsage{
			QueriesUsed:   1,
			SearchCostUSD: 0,
			LLMTokens:     tokens,
		},
		Metrics: SearchMetrics{
			ResponseTimeMS:        responseTimeMs,
			UpstreamLatencyMS:     upstreamLatencyMs,
			TotalResultsAvailable: nil,
		},
		Errors: []any{},
	}

	handlerutil.WriteJSON(w, http.StatusOK, searchResponse)
	return nil
}
