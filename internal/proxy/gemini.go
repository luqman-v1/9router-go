package proxy

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/translator"
)

// ForwardGemini sends an OpenAI-format request to a Gemini-native endpoint.
// projectID is non-empty for antigravity (cloudcode-pa.googleapis.com).
func ForwardGemini(ctx context.Context, client *http.Client, cfg *providers.ProviderConfig, apiKey, bodyStr string, isStream bool, projectID, modelName string) (*http.Response, error) {
	body := []byte(bodyStr)
	if projectID != "" {
		modelName = translator.NormalizeAntigravityModel(modelName)
	}

	var sendBody []byte
	if projectID != "" && translator.IsAntigravityImageModel(modelName) {
		isStream = false
		cleanModel, aspectRatio := translator.ParseImageConfig(modelName)

		var oreq struct {
			Prompt   string `json:"prompt"`
			Messages []struct {
				Content any `json:"content"`
			} `json:"messages"`
		}
		prompt := ""
		if err := json.Unmarshal(body, &oreq); err == nil {
			if oreq.Prompt != "" {
				prompt = oreq.Prompt
			} else {
				for _, m := range oreq.Messages {
					if s, ok := m.Content.(string); ok && s != "" {
						prompt = s
					}
				}
			}
		}

		wrapped, err := translator.WrapAntigravityImageRequest(prompt, "", projectID, cleanModel, aspectRatio)
		if err != nil {
			return nil, fmt.Errorf("wrap antigravity image: %w", err)
		}
		sendBody = wrapped
	} else {
		// Translate OpenAI → Gemini native
		geminiBody, err := translator.TranslateOpenAIToGemini(body)
		if err != nil {
			return nil, fmt.Errorf("translate to Gemini: %w", err)
		}

		// Wrap for antigravity if needed
		sendBody = geminiBody
		if projectID != "" {
			wrapped, err := translator.WrapForAntigravity(geminiBody, projectID, modelName)
			if err != nil {
				return nil, fmt.Errorf("wrap for antigravity: %w", err)
			}
			sendBody = wrapped
		}
	}

	// Build URL
	baseURL := strings.TrimRight(cfg.BaseURL, "/")
	if idx := strings.Index(baseURL, "/v1beta/openai"); idx != -1 {
		baseURL = baseURL[:idx]
	} else if idx := strings.Index(baseURL, "/v1/"); idx != -1 {
		baseURL = baseURL[:idx]
	}

	action := "generateContent"
	if isStream {
		action = "streamGenerateContent?alt=sse"
	}
	var requestURL string
	if projectID != "" {
		requestURL = fmt.Sprintf("%s/v1internal:%s", baseURL, action)
	} else {
		requestURL = fmt.Sprintf("%s/v1beta/models/%s:%s", baseURL, modelName, action)
	}

	headers := map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer " + apiKey,
		"User-Agent":    "antigravity/ide/2.1.1 darwin/arm64",
	}

	req, err := http.NewRequestWithContext(ctx, "POST", requestURL, bytes.NewReader(sendBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upstream request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		errBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("upstream returned %d and body read failed: %w", resp.StatusCode, readErr)
		}
		// Always dump 400 INVALID_ARGUMENT payloads for post-mortem (covers
		// "items.items: missing field." and other schema rejections).
		if resp.StatusCode == http.StatusBadRequest {
			_ = os.WriteFile("/tmp/9router-gemini-400.json", sendBody, 0644)
			preview := sendBody
			if len(preview) > 8000 {
				preview = preview[:8000]
			}
			log.Warn("gemini", "dumped 400 request to /tmp/9router-gemini-400.json", "model", modelName, "bytes", len(sendBody), "error", string(errBody[:min(500, len(errBody))]), "preview", string(preview))
		}
		if bytes.Contains(errBody, []byte("thought signature")) || bytes.Contains(errBody, []byte("Thought signature")) {
			_ = os.WriteFile("/tmp/9router-ag-debug.json", sendBody, 0644)
			log.Warn("gemini", "dumped thought-signature request to /tmp/9router-ag-debug.json", "model", modelName, "bytes", len(sendBody), "error", string(errBody[:min(200, len(errBody))]))
			preview := sendBody
			if len(preview) > 4000 {
				preview = preview[:4000]
			}
			log.Warn("gemini", "thought-signature preview", "preview", string(preview))

			// Retry with a known-valid default signature. Stripping makes
			// Gemini 3 complain about missing signature; replacing with the
			// default (same as Next.js backfill) gives the backend a
			// signature it accepts for the cloaked tool history.
			replaced := translator.ReplaceThoughtSignatures(sendBody, translator.DefaultThinkingSignature)
			if !bytes.Equal(replaced, sendBody) {
				log.Warn("gemini", "retrying with default thoughtSignature", "model", modelName)
				req2, err := http.NewRequestWithContext(ctx, "POST", requestURL, bytes.NewReader(replaced))
				if err == nil {
					for k, v := range headers {
						req2.Header.Set(k, v)
					}
					if resp2, err := client.Do(req2); err == nil {
						if resp2.StatusCode == http.StatusOK {
							return resp2, nil
						}
						errBody2, _ := io.ReadAll(io.LimitReader(resp2.Body, 1*1024*1024))
						resp2.Body.Close()
						log.Warn("gemini", "retry with default thoughtSignature also failed", "status", resp2.StatusCode, "body", string(errBody2[:min(500, len(errBody2))]))
						// Fall through to try stripping as last resort
						if bytes.Contains(errBody2, []byte("thought signature")) || bytes.Contains(errBody2, []byte("Thought signature")) {
							stripped := translator.StripThoughtSignatures(replaced)
							if !bytes.Equal(stripped, replaced) {
								log.Warn("gemini", "retrying without thoughtSignature", "model", modelName)
								req3, err := http.NewRequestWithContext(ctx, "POST", requestURL, bytes.NewReader(stripped))
								if err == nil {
									for k, v := range headers {
										req3.Header.Set(k, v)
									}
									if resp3, err := client.Do(req3); err == nil {
										if resp3.StatusCode == http.StatusOK {
											return resp3, nil
										}
										errBody3, _ := io.ReadAll(io.LimitReader(resp3.Body, 1*1024*1024))
										resp3.Body.Close()
										log.Warn("gemini", "retry without thoughtSignature also failed", "status", resp3.StatusCode, "body", string(errBody3[:min(500, len(errBody3))]))
										return nil, &UpstreamError{StatusCode: resp3.StatusCode, Body: errBody3}
									}
								}
							}
						}
						return nil, &UpstreamError{StatusCode: resp2.StatusCode, Body: errBody2}
					}
				}
			}
		}
		return nil, &UpstreamError{StatusCode: resp.StatusCode, Body: errBody}
	}
	return resp, nil
}
