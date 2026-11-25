package oauth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/providers"
)

// The token-poll shapes, pinned against what api.freepi.ai actually answers
// (probed 2026-11-09).
//
// This is a regression guard with a specific history: the first implementation
// only recognised free-pi-cli's union schema (`{token}` / `{code}` /
// `{retry_after}`), but the real "not authorised yet" body is
// `{"status":"pending"}`. It happened to fall through to "pending" anyway, so the
// visible failure was elsewhere — an expired login reported as an indefinite
// spinner. Pinning all three observed shapes keeps both mistakes out.
func TestFreePiPollToken_RealShapes(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		want       freePiPollStatus
		wantToken  string
		wantErr    bool
		wantRetry  int
		regression string
	}{
		{
			name:       "not authorised yet",
			status:     http.StatusOK,
			body:       `{"status":"pending"}`,
			want:       freePiPollPending,
			regression: "the real pending body carries no code and no retry_after",
		},
		{
			name:      "authorised",
			status:    http.StatusOK,
			body:      `{"status":"authorized","token":"jwt-abc"}`,
			want:      freePiPollGranted,
			wantToken: "jwt-abc",
		},
		{
			name:   "authorised with a bare token and no status",
			status: http.StatusOK,
			body:   `{"token":"jwt-bare"}`,
			want:   freePiPollGranted, wantToken: "jwt-bare",
		},
		{
			name:       "consent not recorded",
			status:     http.StatusForbidden,
			body:       `{"code":"consent_required","message":"consent required before sign-in"}`,
			want:       freePiPollConsentRequired,
			regression: "the server answers 403 with a code, not a 200 body",
		},
		{
			name:   "slow down with a retry hint",
			status: http.StatusOK,
			body:   `{"retry_after":9}`,
			want:   freePiPollPending, wantRetry: 9,
		},
		{
			// An unparseable body must not be read as "pending": that is how a
			// stalled sign-in becomes an infinite spinner with no diagnostic.
			name:    "unparseable body is an error",
			status:  http.StatusOK,
			body:    `<html>gateway error</html>`,
			wantErr: true,
		},
		{
			name:    "upstream server error is an error",
			status:  http.StatusBadGateway,
			body:    `{"error":"upstream down"}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			prev := freePiAuthBaseURL
			freePiAuthBaseURL = srv.URL
			t.Cleanup(func() { freePiAuthBaseURL = prev })

			req := httptest.NewRequest(http.MethodPost, "/api/oauth/free-pi/poll", nil)
			got, err := freePiPollToken(req, "sess-1")

			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Status != tt.want {
				t.Errorf("status = %v, want %v", got.Status, tt.want)
			}
			if got.Token != tt.wantToken {
				t.Errorf("token = %q, want %q", got.Token, tt.wantToken)
			}
			if got.RetryAfter != tt.wantRetry {
				t.Errorf("retryAfter = %d, want %d", got.RetryAfter, tt.wantRetry)
			}
		})
	}
}

// TestFreePiPoll_ExpiredLoginIsGoneNotPending is the bug the user hit: the
// pending window closed before the browser flow finished, and the old handler
// answered with a bare 400 that the dashboard silently swallowed. 410 is what
// lets the client stop and say "start again" instead of spinning.
func TestFreePiPoll_ExpiredLoginIsGoneNotPending(t *testing.T) {
	newFakeFreePiAuth(t)
	h := &OAuthHandler{}

	// A session this gateway never started (or one whose deadline passed).
	rec := freePiPostJSON(t, h.HandleFreePiPoll, "/api/oauth/free-pi/poll", `{"sessionId":"never-started"}`)

	if rec.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410", rec.Code)
	}
}

// TestFreePiPendingTTLOutlivesTheDeviceCode pins the relationship that caused the
// bug: the server issues codes with expires_in ~899s, and a first-time GitHub
// sign-in can take longer than that. Our window must never be the shorter one.
func TestFreePiPendingTTLOutlivesTheDeviceCode(t *testing.T) {
	if freePiPendingTTL <= 899*1e9 {
		t.Fatalf("freePiPendingTTL = %v, must exceed the device code's ~899s lifetime", freePiPendingTTL)
	}

	// And the entry honours the server's own deadline when it is reported, with
	// one minute of slack so a poll landing just after it gets upstream's answer.
	freePiResetPendingForTest()
	freePiPendingStore("sess-lifetime", 5, 120)
	if !freePiPendingKnown("sess-lifetime") {
		t.Fatal("a freshly stored login should be known")
	}
	freePiPendingMu.Lock()
	entry := freePiPending["sess-lifetime"]
	freePiPendingMu.Unlock()

	wantDeadline := entry.createdAt.Add(120*1e9 + 60*1e9)
	if diff := entry.expiresAt.Sub(wantDeadline); diff > 1e9 || diff < -1e9 {
		t.Errorf("expiresAt = %v, want ~1m past the server's 120s deadline", entry.expiresAt)
	}
}

var _ = providers.FreePiCodeConsentRequired
