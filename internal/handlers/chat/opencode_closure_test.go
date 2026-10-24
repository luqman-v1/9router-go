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
