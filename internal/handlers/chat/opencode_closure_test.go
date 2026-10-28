package chat

import (
	json "encoding/json/v2"
	"errors"
	"net/http"
	"testing"
)

func TestOpencodeClosureError_ClassifiesVariants(t *testing.T) {
	tests := []struct {
		name           string
		provider       string
		upstreamStatus int
		upstreamBody   string
		wantStatus     int
		wantType       string
		wantRetryAfter string
	}{
		{
			name:           "generic free tier refusal",
			provider:       "opencode",
			upstreamStatus: 403,
			upstreamBody:   `{"error":{"type":"FreeTierError","message":"OpenCode's free tier can only be used from within OpenCode."}}`,
			wantStatus:     http.StatusServiceUnavailable,
			wantType:       "free_tier_unavailable",
			wantRetryAfter: "30",
		},
		{
			name:           "region blocked",
			provider:       "opencode",
			upstreamStatus: 403,
			upstreamBody:   `{"error":{"type":"FreeTierError","message":"This service is not available in your country"}}`,
			wantStatus:     http.StatusForbidden,
			wantType:       "free_tier_region_blocked",
			wantRetryAfter: "",
		},
		{
			name:           "usage limit reached (real 429 shape)",
			provider:       "opencode",
			upstreamStatus: 429,
			upstreamBody:   `{"type":"error","error":{"type":"FreeUsageLimitError","message":"Free usage limit reached. Subscribe to OpenCode Go to keep going: https://opencode.ai/go"}}`,
			wantStatus:     http.StatusTooManyRequests,
			wantType:       "free_tier_quota_exhausted",
			wantRetryAfter: "60",
		},
		{
			name:           "rate limit / quota exceeded",
			provider:       "opencode-zen",
			upstreamStatus: 403,
			upstreamBody:   `{"error":{"type":"FreeTierError","message":"FreeUsageLimitError: rate limit exceeded for today"}}`,
			wantStatus:     http.StatusTooManyRequests,
			wantType:       "free_tier_quota_exhausted",
			wantRetryAfter: "60",
		},
		{
			name:           "upgrade required",
			provider:       "opencode-go",
			upstreamStatus: 403,
			upstreamBody:   `{"error":{"type":"FreeTierError","message":"Upgrade Required: please update your client version"}}`,
			wantStatus:     http.StatusUpgradeRequired,
			wantType:       "free_tier_upgrade_required",
			wantRetryAfter: "",
		},
		{
			name:           "abuse traffic detected",
			provider:       "opencode",
			upstreamStatus: 403,
			upstreamBody:   `{"error":{"type":"FreeTierError","message":"Forbidden, OpenCode Free Model Gateway Detect Abuse Traffic"}}`,
			wantStatus:     http.StatusServiceUnavailable,
			wantType:       "free_tier_abuse_detected",
			wantRetryAfter: "30",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origErr := &upstreamError{
				StatusCode: tt.upstreamStatus,
				Body:       []byte(tt.upstreamBody),
			}

			classified := opencodeClosureError(tt.provider, "nemotron-3-free", origErr)
			var ue *upstreamError
			if !errors.As(classified, &ue) {
				t.Fatalf("expected *upstreamError, got %T: %v", classified, classified)
			}

			if ue.StatusCode != tt.wantStatus {
				t.Errorf("StatusCode = %d, want %d", ue.StatusCode, tt.wantStatus)
			}

			var body struct {
				Error struct {
					Type    string `json:"type"`
					Message string `json:"message"`
					Code    int    `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(ue.Body, &body); err != nil {
				t.Fatalf("unmarshal error body failed: %v", err)
			}

			if body.Error.Type != tt.wantType {
				t.Errorf("Error.Type = %q, want %q", body.Error.Type, tt.wantType)
			}
			if body.Error.Code != tt.wantStatus {
				t.Errorf("Error.Code = %d, want %d", body.Error.Code, tt.wantStatus)
			}

			gotRetryAfter := ue.Header.Get("Retry-After")
			if gotRetryAfter != tt.wantRetryAfter {
				t.Errorf("Retry-After = %q, want %q", gotRetryAfter, tt.wantRetryAfter)
			}
		})
	}
}

func TestOpencodeClosureError_NonOpencodePassesThrough(t *testing.T) {
	origErr := &upstreamError{
		StatusCode: 403,
		Body:       []byte(`{"error":{"type":"FreeTierError","message":"not allowed"}}`),
	}
	classified := opencodeClosureError("claude", "claude-3-5-sonnet", origErr)
	if !errors.Is(classified, origErr) {
		t.Errorf("expected original error untouched for non-opencode provider")
	}
}

func TestOpencodeClosureError_NonFreeTierErrorPassesThrough(t *testing.T) {
	origErr := &upstreamError{
		StatusCode: 403,
		Body:       []byte(`{"error":{"type":"AuthError","message":"invalid token"}}`),
	}
	classified := opencodeClosureError("opencode", "model", origErr)
	if !errors.Is(classified, origErr) {
		t.Errorf("expected original error untouched for non-FreeTierError")
	}
}

// The gate refuses in two shapes, and both must be recognised so the failover
// loop rotates the egress instead of cooling a healthy account. A 429 that is
// NOT the free-tier usage limit is a genuine rate limit and keeps the normal
// path, as does a refusal reported for another provider.
func TestOpencodeFreeTierRefusal_ClassifiesGateShapes(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		status   int
		body     string
		want     bool
	}{
		{
			name:     "403 free tier error",
			provider: "opencode",
			status:   http.StatusForbidden,
			body:     `{"error":{"type":"FreeTierError","message":"free tier can only be used from within OpenCode"}}`,
			want:     true,
		},
		{
			name:     "429 free usage limit",
			provider: "opencode",
			status:   http.StatusTooManyRequests,
			body:     `{"type":"error","error":{"type":"FreeUsageLimitError","message":"Free usage limit reached. Subscribe to OpenCode Go to keep going: https://opencode.ai/go"}}`,
			want:     true,
		},
		{
			name:     "429 free usage limit on zen lane",
			provider: "opencode-zen",
			status:   http.StatusTooManyRequests,
			body:     `{"error":{"type":"FreeUsageLimitError","message":"Free usage limit reached."}}`,
			want:     true,
		},
		{
			name:     "429 without the usage-limit token is a real rate limit",
			provider: "opencode",
			status:   http.StatusTooManyRequests,
			body:     `{"error":{"type":"RateLimitError","message":"too many requests"}}`,
			want:     false,
		},
		{
			name:     "403 without the free-tier token stays on the normal path",
			provider: "opencode",
			status:   http.StatusForbidden,
			body:     `{"error":{"type":"AuthError","message":"invalid token"}}`,
			want:     false,
		},
		{
			name:     "other provider 429 untouched",
			provider: "claude",
			status:   http.StatusTooManyRequests,
			body:     `{"error":{"type":"FreeUsageLimitError","message":"Free usage limit reached."}}`,
			want:     false,
		},
		{
			name:     "other provider 403 untouched",
			provider: "antigravity",
			status:   http.StatusForbidden,
			body:     `{"error":{"type":"FreeTierError","message":"free tier"}}`,
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ue := &upstreamError{StatusCode: tt.status, Body: []byte(tt.body)}
			if got := isOpencodeFreeTierRefusal(tt.provider, ue); got != tt.want {
				t.Errorf("isOpencodeFreeTierRefusal(%q, %d) = %v, want %v", tt.provider, tt.status, got, tt.want)
			}
		})
	}
}

// A genuine 429 rate limit from OpenCode is not a free-tier refusal, so it must
// keep its own classification rather than being rewritten as a gate refusal.
func TestOpencodeClosureError_RealRateLimitPassesThrough(t *testing.T) {
	origErr := &upstreamError{
		StatusCode: http.StatusTooManyRequests,
		Body:       []byte(`{"error":{"type":"RateLimitError","message":"too many requests"}}`),
	}
	classified := opencodeClosureError("opencode", "deepseek-v4.1-flash-free", origErr)
	if !errors.Is(classified, origErr) {
		t.Errorf("expected a non-free-tier 429 to be forwarded untouched")
	}
}
