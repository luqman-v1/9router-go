package dashboard

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"

	"9router/proxy/internal/proxy"
)

var freebuffUsageClient = &http.Client{
	Transport: proxy.NewFallbackTransport(http.DefaultTransport),
	Timeout:   usageHTTPTimeout,
}

var freebuffSessionStatusURLFor = freebuffSessionStatusURLFromBase

func freebuffSessionStatusURLFromBase(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	for _, suffix := range []string{"/v1/chat/completions", "/chat/completions", "/v1"} {
		if trimmed := strings.TrimSuffix(base, suffix); trimmed != base {
			return trimmed + "/api/v1/freebuff/session"
		}
	}
	if base == "" {
		return "https://www.codebuff.com/api/v1/freebuff/session"
	}
	return base + "/api/v1/freebuff/session"
}

func fetchFreebuffUsage(ctx context.Context, token, baseURL string) usageResult {
	if token == "" {
		return usageResult{message: "freebuff: no credential on this connection"}
	}

	reqURL := freebuffSessionStatusURLFor(baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return usageResult{message: fmt.Sprintf("freebuff: %v", err)}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "Freebuff-CLI/0.2.36")
	req.Header.Set("Accept", "application/json")

	resp, err := freebuffUsageClient.Do(req)
	if err != nil {
		return usageResult{message: fmt.Sprintf("freebuff: %v", err)}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized {
		return usageResult{message: "freebuff: credential expired — sign in again"}
	}
	if resp.StatusCode != http.StatusOK {
		return usageResult{message: fmt.Sprintf("freebuff: HTTP %d", resp.StatusCode)}
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return usageResult{message: fmt.Sprintf("freebuff: read response: %v", err)}
	}

	var status struct {
		Status       string `json:"status"`
		CurrentModel string `json:"currentModel"`
		ExpiresAt    string `json:"expiresAt"`
		Tier         string `json:"tier"`
		Freebucks    struct {
			Daily struct {
				Limit     float64 `json:"limit"`
				Spent     float64 `json:"spent"`
				Remaining float64 `json:"remaining"`
				ResetAt   string  `json:"resetAt"`
			} `json:"daily"`
			Wallet struct {
				Balance float64 `json:"balance"`
			} `json:"wallet"`
		} `json:"freebucks"`
	}

	if err := json.Unmarshal(raw, &status); err != nil {
		return usageResult{message: fmt.Sprintf("freebuff: parse response: %v", err)}
	}

	quotas := map[string]any{}
	daily := status.Freebucks.Daily
	if daily.Limit > 0 {
		quotas["daily"] = map[string]any{
			"used":        daily.Spent,
			"total":       daily.Limit,
			"remaining":   daily.Remaining,
			"displayName": "Daily Freebucks",
			"currency":    "Freebucks",
			"resetAt":     daily.ResetAt,
			"recurring":   true,
		}
	}

	wallet := status.Freebucks.Wallet
	if wallet.Balance > 0 {
		quotas["wallet"] = map[string]any{
			"remaining":       wallet.Balance,
			"displayName":     "Wallet Balance",
			"isCreditBalance": true,
			"currency":        "Freebucks",
		}
	}

	if status.Status == "active" && status.ExpiresAt != "" {
		modelName := status.CurrentModel
		if modelName == "" {
			modelName = "active"
		}
		quotas["active_session"] = map[string]any{
			"used":        1,
			"total":       1,
			"displayName": "Active Session (" + modelName + ")",
			"resetAt":     status.ExpiresAt,
		}
	}

	plan := status.Tier
	if plan == "" {
		plan = "free"
	}

	return usageResult{
		plan:   plan,
		quotas: quotas,
	}
}
