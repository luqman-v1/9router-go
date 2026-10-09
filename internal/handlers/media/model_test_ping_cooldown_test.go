package media

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// The dashboard's model sweep paints a probe amber ("blocked") only when the
// account was parked, and red ("failed") when the model was actually asked and
// refused. Everything the sweep did NOT test must land in the first bucket.
//
// The gate is therefore pinned on three properties at once: it must hold for
// every lane that resolves a connection (chat answers 502, every media lane
// answers 404 for the same selector error), it must hold when the stamp has
// been cut off by probe truncation, and it must NOT fire on a provider's own
// quota refusal, which is recorded per-model and never clears by waiting.
func TestReadProbeResult_CooldownVerdict(t *testing.T) {
	const stamp = "2026-10-09T03:00:00Z"
	chatEnvelope := func(msg string) string {
		return `{"error":{"message":"` + msg + `","type":"server_error","code":"bad_gateway"}}`
	}
	mediaEnvelope := func(msg string) string {
		return `{"error":{"message":"` + msg + `","type":"invalid_request_error","code":"404"}}`
	}

	tests := []struct {
		name      string
		status    int
		body      string
		wantBlock bool
		wantReset string
	}{
		{
			name:      "chat lane wraps the selector error in a 502",
			status:    502,
			body:      chatEnvelope("upstream error: no available connections for provider: deepseek (all in cooldown, earliest reset " + stamp + ")"),
			wantBlock: true,
			wantReset: stamp,
		},
		{
			name:      "media lane reports the same selector error as a 404",
			status:    404,
			body:      mediaEnvelope("no available connections for provider: deepseek (all in cooldown, earliest reset " + stamp + ")"),
			wantBlock: true,
			wantReset: stamp,
		},
		{
			name:      "combo lane 429 when every entry came back retryable",
			status:    429,
			body:      mediaEnvelope("all connections for this provider are rate-limited"),
			wantBlock: true,
			wantReset: "",
		},
		{
			// probeDetail truncates the row text at probeTextLimit, so a long
			// provider name leaves the stamp half-written at the end of the
			// body. The park is still real; only the reset time is unknowable.
			name:      "truncated probe body yields no stamp rather than a partial one",
			status:    502,
			body:      chatEnvelope("upstream error: no available connections for provider: " + strings.Repeat("p", 240) + " (all in cooldown, earliest reset 2026-10-09T03:00)"),
			wantBlock: true,
			wantReset: "",
		},
		{
			name:      "model-scoped quota 429 is a failure, not a cooldown",
			status:    429,
			body:      mediaEnvelope("Rate limit reached for deepseek-chat on this account"),
			wantBlock: false,
			wantReset: "",
		},
		{
			name:      "an upstream message mentioning cooldown is not a gateway park",
			status:    429,
			body:      mediaEnvelope("account is in cooldown at the provider, retry later"),
			wantBlock: false,
			wantReset: "",
		},
		{
			name:      "all accounts excluded is a real failure, not a park",
			status:    404,
			body:      mediaEnvelope("no available connections for provider: deepseek (all excluded)"),
			wantBlock: false,
			wantReset: "",
		},
		{
			name:      "unhealthy provider 502 stays a failure",
			status:    502,
			body:      chatEnvelope("upstream error: provider deepseek/deepseek-chat is unhealthy"),
			wantBlock: false,
			wantReset: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			rec.Code = tt.status
			rec.Body.WriteString(tt.body)

			got := readProbeResult(rec, "")
			if got.Blocked != tt.wantBlock {
				t.Errorf("Blocked = %v, want %v (error: %q)", got.Blocked, tt.wantBlock, got.Error)
			}
			if got.ResetAt != tt.wantReset {
				t.Errorf("ResetAt = %q, want %q", got.ResetAt, tt.wantReset)
			}
			if tt.wantBlock && got.Error == "" {
				t.Error("a blocked verdict must still carry the reason for the dashboard row")
			}
		})
	}
}

// A park is only a park while the stamp is a real timestamp. Handing the
// operator "cooldown until 2026-10-09T03:00" — or "until the token expired" —
// is worse than telling them nothing about when it lifts.
func TestExtractEarliestReset(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{"full sentence", "all in cooldown, earliest reset 2026-10-09T03:00:00Z)", "2026-10-09T03:00:00Z"},
		{"followed by prose", "all in cooldown, earliest reset 2026-10-09T03:00:00Z; retry later", "2026-10-09T03:00:00Z"},
		{"cut mid-value", "all in cooldown, earliest reset 2026-10-09T03:00", ""},
		{"no stamp at all", "all connections for this provider are rate-limited", ""},
		{"non-timestamp payload", "all in cooldown, earliest reset soon", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractEarliestReset(tt.text); got != tt.want {
				t.Errorf("extractEarliestReset(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}
