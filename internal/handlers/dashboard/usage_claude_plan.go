package dashboard

import (
	"context"
	json "encoding/json/v2"
	"net/http"
	"strings"

	"9router/proxy/internal/handlers/chat"
)

// Claude plan tier.
//
// There is no expiry date to show: api.anthropic.com/api/oauth/profile reports
// the tier the seat holds, not when it lapses, and the endpoint that does carry
// billing state (claude.ai/api/organizations/{org}/subscription_details) is
// cookie-only. cockpit-tools reads the same profile fields and likewise shows a
// tier with no end date.
//
// claudeProfilePlan reads the tier off an oauth/profile body. organization_type
// is the enum the OAuth profile publishes; rate_limit_tier refines a "Max" seat
// to its multiplier. An unknown or absent value yields "" rather than a guess.
func claudeProfilePlan(body []byte) string {
	var resp struct {
		Organization struct {
			OrganizationType string `json:"organization_type"`
			RateLimitTier    string `json:"rate_limit_tier"`
		} `json:"organization"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return ""
	}
	org := strings.ToLower(strings.TrimSpace(resp.Organization.OrganizationType))
	var plan string
	switch org {
	case "claude_max":
		plan = "Max"
	case "claude_pro":
		plan = "Pro"
	case "claude_team":
		plan = "Team"
	case "claude_enterprise":
		plan = "Enterprise"
	default:
		return ""
	}
	if plan == "Max" {
		// The multiplier lives in rate_limit_tier ("default_claude_max_20x");
		// the seat is a Max either way when it is missing.
		switch strings.ToLower(strings.TrimSpace(resp.Organization.RateLimitTier)) {
		case "default_claude_max_20x", "claude_max_20x":
			plan = "Max 20x"
		case "default_claude_max_5x", "claude_max_5x":
			plan = "Max 5x"
		}
	}
	return plan
}

// fetchClaudePlan reads the tier for a Claude OAuth token. Fail-open: a plan
// badge is decoration, so a profile read that fails must not cost the caller
// the quota rows that did load.
func fetchClaudePlan(ctx context.Context, accessToken string) string {
	status, _, body, err := usageDo(ctx, http.MethodGet, claudeProfileURL, map[string]string{
		"Authorization":     "Bearer " + accessToken,
		"Accept":            "application/json",
		"anthropic-beta":    claudeUsageBeta,
		"anthropic-version": claudeUsageVersion,
		"User-Agent":        chat.ApplyClaudeCLIVersionHeader(claudeUsageUserAgent),
	}, nil)
	if err != nil || status != http.StatusOK {
		return ""
	}
	return claudeProfilePlan(body)
}
