package providers

import "testing"

func TestEffectiveStatus(t *testing.T) {
	tests := []struct {
		name              string
		isActive          int
		testStatus        string
		hasActiveCooldown bool
		lastError         string
		want              string
	}{
		{
			name:       "disabled toggle wins over any status",
			isActive:   0,
			testStatus: StatusActive,
			want:       "disabled",
		},
		{
			name:       "never probed reads as active",
			isActive:   1,
			testStatus: "",
			want:       StatusActive,
		},
		{
			name:       "unknown is treated as never probed",
			isActive:   1,
			testStatus: StatusUnknown,
			want:       StatusActive,
		},
		{
			name:              "never probed with a live lock is unavailable",
			isActive:          1,
			testStatus:        "",
			hasActiveCooldown: true,
			want:              StatusUnavailable,
		},
		{
			name:       "active passes through",
			isActive:   1,
			testStatus: StatusActive,
			want:       StatusActive,
		},
		{
			name:       "unavailable with no live lock recovers to active",
			isActive:   1,
			testStatus: StatusUnavailable,
			want:       StatusActive,
		},
		{
			name:              "unavailable with a live lock stays unavailable",
			isActive:          1,
			testStatus:        StatusUnavailable,
			hasActiveCooldown: true,
			want:              StatusUnavailable,
		},
		{
			name:              "spent balance with unavailable status and live lock",
			isActive:          1,
			testStatus:        StatusUnavailable,
			hasActiveCooldown: true,
			lastError:         "credit insufficient balance: balance=0",
			want:              StatusQuotaExhausted,
		},
		{
			name:       "spent balance outlives its cooldown",
			isActive:   1,
			testStatus: StatusUnavailable,
			lastError:  "insufficient credit",
			want:       StatusQuotaExhausted,
		},

		{
			name:              "per-minute quota is a throttle, not a spent balance",
			isActive:          1,
			testStatus:        StatusUnavailable,
			hasActiveCooldown: true,
			lastError:         "quota exceeded per minute",
			want:              StatusRateLimited,
		},
		{
			name:              "rate limit with a live lock is throttled",
			isActive:          1,
			testStatus:        StatusUnavailable,
			hasActiveCooldown: true,
			lastError:         "too many requests",
			want:              StatusRateLimited,
		},
		{
			name:       "rejected key does not heal on a timer",
			isActive:   1,
			testStatus: StatusUnavailable,
			lastError:  "invalid api key",
			want:       StatusUnavailable,
		},
		{
			name:       "revoked grant is a permanent failure",
			isActive:   1,
			testStatus: StatusUnavailable,
			lastError:  "invalid_grant",
			want:       StatusUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EffectiveStatusWithError(tt.isActive, tt.testStatus, tt.hasActiveCooldown, tt.lastError)
			if got != tt.want {
				t.Errorf("EffectiveStatusWithError() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsEffectivelyActive(t *testing.T) {
	tests := []struct {
		name              string
		isActive          int
		testStatus        string
		hasActiveCooldown bool
		lastError         string
		want              bool
	}{
		{name: "enabled and healthy", isActive: 1, testStatus: StatusActive, want: true},
		{name: "disabled never counts", isActive: 0, testStatus: StatusActive, want: false},
		{name: "quota exhausted never counts", isActive: 1, testStatus: StatusUnavailable, lastError: "balance=0", want: false},
		{name: "throttled does not count", isActive: 1, testStatus: StatusUnavailable, hasActiveCooldown: true, lastError: "rate limit", want: false},
		{name: "success spelling is healthy", isActive: 1, testStatus: "success", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsEffectivelyActiveWithError(tt.isActive, tt.testStatus, tt.hasActiveCooldown, tt.lastError)
			if got != tt.want {
				t.Errorf("IsEffectivelyActiveWithError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStatusVariant(t *testing.T) {
	tests := []struct {
		isActive        int
		effectiveStatus string
		want            string
	}{
		{0, StatusActive, "default"},
		{1, StatusActive, "success"},
		{1, "success", "success"},
		{1, StatusRateLimited, "warn"},
		{1, StatusQuotaExhausted, "error"},
		{1, StatusUnavailable, "error"},
		{1, StatusExpired, "error"},
		{1, StatusError, "error"},
		{1, "something-else", "default"},
	}
	for _, tt := range tests {
		got := StatusVariant(tt.isActive, tt.effectiveStatus)
		if got != tt.want {
			t.Errorf("StatusVariant(%d, %q) = %q, want %q", tt.isActive, tt.effectiveStatus, got, tt.want)
		}
	}
}

func TestHasActiveCooldown(t *testing.T) {
	tests := []struct {
		name string
		data string
		want bool
	}{
		{"empty", "", false},
		{"whitespace", "   ", false},
		{"no lock", `{"testStatus":"active"}`, false},
		{"lock present", `{"modelLock_gpt-5":"2026-10-30T00:00:00Z"}`, true},
		{"null lock still counts as a key", `{"modelLock_gpt-5":null}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasActiveCooldown(tt.data); got != tt.want {
				t.Errorf("HasActiveCooldown(%q) = %v, want %v", tt.data, got, tt.want)
			}
		})
	}
}

func TestConnectionStatus(t *testing.T) {
	tests := []struct {
		name       string
		isActive   int
		data       string
		wantStatus string
		wantActive bool
	}{
		{
			name:       "empty data reads as healthy",
			isActive:   1,
			data:       "",
			wantStatus: StatusActive,
			wantActive: true,
		},
		{
			name:       "quota exhausted in lastError is surfaced",
			isActive:   1,
			data:       `{"testStatus":"unavailable","lastError":{"message":"credit insufficient balance: balance=0"}}`,
			wantStatus: StatusQuotaExhausted,
			wantActive: false,
		},
		{
			name:       "string lastError is handled",
			isActive:   1,
			data:       `{"lastError":"invalid api key"}`,
			wantStatus: StatusUnavailable,
			wantActive: false,
		},
		{
			name:       "malformed JSON degrades to the no-data path",
			isActive:   1,
			data:       `{not json`,
			wantStatus: StatusActive,
			wantActive: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, active := ConnectionStatus(tt.isActive, tt.data)
			if status != tt.wantStatus {
				t.Errorf("ConnectionStatus() status = %q, want %q", status, tt.wantStatus)
			}
			if active != tt.wantActive {
				t.Errorf("ConnectionStatus() active = %v, want %v", active, tt.wantActive)
			}
		})
	}
}

func TestLooksLikeHelpers(t *testing.T) {
	if !LooksLikeQuotaExhausted("credit insufficient balance: balance=0") {
		t.Error("expected balance=0 to read as quota exhausted")
	}
	if LooksLikeQuotaExhausted("invalid request body") {
		t.Error("a plain 400 must not read as quota exhausted")
	}
	if !LooksLikeThrottleWindow("quota exceeded per minute") {
		t.Error("per-minute wording must read as a throttle window")
	}
	if !LooksLikePermanentFailure("401 Unauthorized") {
		t.Error("401 must read as a permanent failure")
	}
	if !LooksLikeRateLimited("429 Too Many Requests") {
		t.Error("429 must read as rate limited")
	}
}
