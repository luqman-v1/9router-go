package media

import (
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
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

func (h *MediaHandler) handleAntigravitySearch(w http.ResponseWriter, r *http.Request, body []byte, modelInfo *chat.ModelInfo) error {
	var reqBody struct {
		Query      string `json:"query"`
		Prompt     string `json:"prompt"`
		MaxResults int    `json:"max_results"`
	}
	_ = json.Unmarshal(body, &reqBody)
	query := reqBody.Query
	if query == "" {
		query = reqBody.Prompt
	}
	if query == "" {
		return fmt.Errorf("missing query in search request")
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

	conn, connData, err := h.ChatH.GetBestConnection("antigravity", modelInfo.ConnectionID, nil, model)
	if err != nil || conn == nil {
		return fmt.Errorf("no active connection for antigravity: %w", err)
	}

	apiKey := chat.ExtractAPIKey(connData)
	if apiKey == "" {
		return fmt.Errorf("no API key found for antigravity connection %s", conn.ID)
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
		return fmt.Errorf("Antigravity account has no projectId — reconnect the account")
	}

	providerCfg, err := h.ChatH.GetProviderConfig("antigravity", connData)
	if err != nil {
		return fmt.Errorf("get antigravity config: %w", err)
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
		return fmt.Errorf("marshal antigravity search request: %w", err)
	}

	baseURL := strings.TrimRight(providerCfg.BaseURL, "/")
	targetURL := fmt.Sprintf("%s/v1internal:generateContent", baseURL)

	httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(searchBytes))
	if err != nil {
		return fmt.Errorf("create antigravity search request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("User-Agent", "antigravity/ide/2.1.1 darwin/arm64")

	client := h.ChatH.GetClientForConnection(connData)
	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("antigravity search upstream request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("antigravity search upstream returned %d: %s", resp.StatusCode, string(respBody))
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read antigravity search response: %w", err)
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

	limit := reqBody.MaxResults
	if limit <= 0 {
		limit = 10
	}
	if len(sourceOrder) > limit {
		sourceOrder = sourceOrder[:limit]
	}

	retrievedAt := time.Now().UTC().Format(time.RFC3339)
	var results []map[string]any
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

		results = append(results, map[string]any{
			"title":        src.Title,
			"url":          u,
			"snippet":      snippet,
			"position":     idx + 1,
			"score":        nil,
			"published_at": nil,
			"favicon_url":  nil,
			"content":      content,
			"metadata":     map[string]any{},
			"citation": map[string]any{
				"provider":     "antigravity",
				"retrieved_at": retrievedAt,
				"rank":         idx + 1,
			},
			"provider_raw": nil,
		})
	}

	tokens := 0
	if agResp.Response.UsageMetadata != nil {
		tokens = agResp.Response.UsageMetadata.TotalTokenCount
	} else if agResp.UsageMetadata != nil {
		tokens = agResp.UsageMetadata.TotalTokenCount
	}

	h.Repo.UpdateConnectionLastUsed(conn.ID)
	log.Info("request", "POST /v1/search", "provider", "antigravity", "model", model, "query", query, "results", len(results), "conn", conn.ID[:min(8, len(conn.ID))], "tokens", tokens)
	log.Info("usage", "logged", "provider", "antigravity", "model", model, "query", query, "results", len(results), "tokens", tokens)

	searchResponse := map[string]any{
		"provider": "antigravity",
		"query":    query,
		"results":  results,
		"answer": map[string]any{
			"source": "antigravity",
			"text":   answerText,
			"model":  model,
		},
		"usage": map[string]any{
			"queries_used":    1,
			"search_cost_usd": 0,
			"llm_tokens":      tokens,
		},
		"metrics": map[string]any{
			"total_results_available": nil,
		},
		"errors": []any{},
	}

	handlerutil.WriteJSON(w, http.StatusOK, searchResponse)
	return nil
}
