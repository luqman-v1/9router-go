package codexquota

import (
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestParseUsage_WindowEnvelopes(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		wantSession  float64
		wantWeekly   float64
		wantPlan     string
		wantNoWeekly bool
		limitReached bool
		resetCredits int
	}{
		{
			name: "rate_limit envelope with both windows",
			body: `{"plan_type":"plus","rate_limit":{"limit_reached":false,
				"primary_window":{"used_percent":0,"reset_at":1712345678},
				"secondary_window":{"used_percent":65,"reset_at":1712432078}}}`,
			wantSession: 0,
			wantWeekly:  65,
			wantPlan:    "plus",
		},
		{
			name:         "rate_limits envelope",
			body:         `{"rate_limits":{"primary_window":{"percent_used":42}}}`,
			wantSession:  42,
			wantPlan:     "unknown",
			wantNoWeekly: true,
		},
		{
			name:         "rate_limits_by_limit_id codex envelope",
			body:         `{"rate_limits_by_limit_id":{"codex":{"primary_window":{"used_percent":7}}}}`,
			wantSession:  7,
			wantPlan:     "unknown",
			wantNoWeekly: true,
		},
		{
			name:         "nested rate_limit level is unwrapped",
			body:         `{"rate_limit":{"rate_limit":{"primary_window":{"used_percent":88}}}}`,
			wantSession:  88,
			wantPlan:     "unknown",
			wantNoWeekly: true,
		},
		{
			name:         "plan falls back to summary.plan",
			body:         `{"summary":{"plan":"pro"},"rate_limit":{"primary_window":{"used_percent":0}}}`,
			wantSession:  0,
			wantPlan:     "pro",
			wantNoWeekly: true,
		},
		{
			name: "limit_reached and reset credits",
			body: `{"rate_limit":{"limit_reached":true,"primary_window":{"used_percent":100}},
				"rate_limit_reset_credits":{"available_count":2}}`,
			wantSession:  100,
			wantPlan:     "unknown",
			wantNoWeekly: true,
			limitReached: true,
			resetCredits: 2,
		},
		{
			// Upstream never fabricates a 7d row for an account without one;
			// the reporter's "7d=None" is the account lacking the window.
			name:         "missing secondary window stays nil",
			body:         `{"rate_limit":{"primary_window":{"used_percent":0}}}`,
			wantSession:  0,
			wantPlan:     "unknown",
			wantNoWeekly: true,
		},
		{
			name:         "used percent is clamped to 0..100",
			body:         `{"rate_limit":{"primary_window":{"used_percent":140}}}`,
			wantSession:  100,
			wantPlan:     "unknown",
			wantNoWeekly: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usage, err := ParseUsage([]byte(tt.body))
			if err != nil {
				t.Fatalf("ParseUsage() error = %v", err)
			}
			if usage.Session == nil {
				t.Fatal("expected a session window")
			}
			if usage.Session.UsedPercent != tt.wantSession {
				t.Errorf("session used = %v, want %v", usage.Session.UsedPercent, tt.wantSession)
			}
			if usage.Plan != tt.wantPlan {
				t.Errorf("plan = %q, want %q", usage.Plan, tt.wantPlan)
			}
			if usage.LimitReached != tt.limitReached {
				t.Errorf("limitReached = %v, want %v", usage.LimitReached, tt.limitReached)
			}
			if usage.ResetCredits != tt.resetCredits {
				t.Errorf("resetCredits = %d, want %d", usage.ResetCredits, tt.resetCredits)
			}
			if tt.wantNoWeekly {
				if usage.Weekly != nil {
					t.Errorf("expected no weekly window, got %+v", usage.Weekly)
				}
			} else if usage.Weekly == nil {
				t.Error("expected a weekly window")
			} else if usage.Weekly.UsedPercent != tt.wantWeekly {
				t.Errorf("weekly used = %v, want %v", usage.Weekly.UsedPercent, tt.wantWeekly)
			}
		})
	}
}

func TestParseUsage_Remaining(t *testing.T) {
	usage, err := ParseUsage([]byte(`{"rate_limit":{"primary_window":{"used_percent":65},"secondary_window":{"used_percent":100}}}`))
	if err != nil {
		t.Fatalf("ParseUsage() error = %v", err)
	}
	if got := usage.Session.Remaining(); got != 35 {
		t.Errorf("session remaining = %v, want 35", got)
	}
	if got := usage.Weekly.Remaining(); got != 0 {
		t.Errorf("weekly remaining = %v, want 0", got)
	}
}

func TestParseUsage_ResetTime(t *testing.T) {
	tests := []struct {
		name string
		body string
		want time.Time
	}{
		{
			name: "unix seconds",
			body: `{"rate_limit":{"primary_window":{"used_percent":0,"reset_at":1712345678}}}`,
			want: time.Unix(1712345678, 0).UTC(),
		},
		{
			name: "unix milliseconds above 1e12",
			body: `{"rate_limit":{"primary_window":{"used_percent":0,"reset_at":1712345678000}}}`,
			want: time.UnixMilli(1712345678000).UTC(),
		},
		{
			name: "resets_at is read as a fallback key",
			body: `{"rate_limit":{"primary_window":{"used_percent":0,"resets_at":1712345678}}}`,
			want: time.Unix(1712345678, 0).UTC(),
		},
		{
			name: "numeric string",
			body: `{"rate_limit":{"primary_window":{"used_percent":0,"reset_at":"1712345678"}}}`,
			want: time.Unix(1712345678, 0).UTC(),
		},
		{
			name: "iso 8601 string",
			body: `{"rate_limit":{"primary_window":{"used_percent":0,"reset_at":"2024-04-05T04:54:38Z"}}}`,
			want: time.Date(2024, 4, 5, 4, 54, 38, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usage, err := ParseUsage([]byte(tt.body))
			if err != nil {
				t.Fatalf("ParseUsage() error = %v", err)
			}
			if usage.Session.ResetAt == nil {
				t.Fatal("expected a reset time")
			}
			if !usage.Session.ResetAt.Equal(tt.want) {
				t.Errorf("resetAt = %v, want %v", usage.Session.ResetAt, tt.want)
			}
		})
	}
}

func TestParseUsage_UnparseableResetStaysNil(t *testing.T) {
	usage, err := ParseUsage([]byte(`{"rate_limit":{"primary_window":{"used_percent":0,"reset_at":"not-a-date"}}}`))
	if err != nil {
		t.Fatalf("ParseUsage() error = %v", err)
	}
	if usage.Session == nil {
		t.Fatal("expected a session window")
	}
	if usage.Session.ResetAt != nil {
		t.Errorf("expected nil resetAt, got %v", usage.Session.ResetAt)
	}
}

func TestParseUsage_ReviewAndSparkWindows(t *testing.T) {
	usage, err := ParseUsage([]byte(`{
		"rate_limit":{"primary_window":{"used_percent":10},"secondary_window":{"used_percent":20}},
		"rate_limits_by_limit_id":{
			"code_review":{"primary_window":{"used_percent":30},"secondary_window":{"used_percent":40}},
			"gpt-5.3-codex-spark":{"primary_window":{"used_percent":50}}
		}}`))
	if err != nil {
		t.Fatalf("ParseUsage() error = %v", err)
	}
	for _, tc := range []struct {
		name string
		got  *Window
		want float64
	}{
		{"session", usage.Session, 10},
		{"weekly", usage.Weekly, 20},
		{"review_session", usage.ReviewSession, 30},
		{"review_weekly", usage.ReviewWeekly, 40},
		{"spark_session", usage.SparkSession, 50},
	} {
		if tc.got == nil {
			t.Errorf("%s: expected a window", tc.name)
			continue
		}
		if tc.got.UsedPercent != tc.want {
			t.Errorf("%s: used = %v, want %v", tc.name, tc.got.UsedPercent, tc.want)
		}
	}
	if usage.SparkWeekly != nil {
		t.Errorf("expected no spark weekly window, got %+v", usage.SparkWeekly)
	}
}

func TestParseUsage_AdditionalRateLimitsScan(t *testing.T) {
	usage, err := ParseUsage([]byte(`{
		"rate_limit":{"primary_window":{"used_percent":0}},
		"additional_rate_limits":[
			{"limit_name":"something_else","primary_window":{"used_percent":1}},
			{"limit_name":"code_review","primary_window":{"used_percent":15}}
		]}`))
	if err != nil {
		t.Fatalf("ParseUsage() error = %v", err)
	}
	if usage.ReviewSession == nil {
		t.Fatal("expected review session window from additional_rate_limits")
	}
	if usage.ReviewSession.UsedPercent != 15 {
		t.Errorf("review session used = %v, want 15", usage.ReviewSession.UsedPercent)
	}
}

func TestParseUsage_RejectsGarbage(t *testing.T) {
	if _, err := ParseUsage([]byte("not json")); err == nil {
		t.Fatal("expected an error for a non-JSON body")
	}
}

func TestFetch_SendsOnlyTheTwoUpstreamHeaders(t *testing.T) {
	var gotAuth, gotAccept, gotOriginator string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		gotOriginator = r.Header.Get("originator")
		_, _ = w.Write([]byte(`{"rate_limit":{"primary_window":{"used_percent":0}}}`))
	}))
	defer srv.Close()

	withTestURL(t, srv.URL)
	usage, err := Fetch(t.Context(), srv.Client(), "tok-123")
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if usage.Session == nil {
		t.Fatal("expected a session window")
	}
	if gotAuth != "Bearer tok-123" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer tok-123")
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q, want application/json", gotAccept)
	}
	// Upstream parity: wham authenticates on the bearer alone and does not want
	// the executor's Codex CLI identity. A revision added originator/User-Agent
	// on the theory that a WAF was blocking the Go default agent; a live probe
	// showed the refusal was a proxy CONNECT rejection and that these headers
	// change nothing, so they stay off.
	if gotOriginator != "" {
		t.Errorf("originator = %q, want empty", gotOriginator)
	}
}

// A refused CONNECT tunnel must be reported as a proxy refusal rather than as a
// bare "Forbidden" that reads like the provider rejected the credential.
func TestFetch_ClassifiesProxyRefusal(t *testing.T) {
	refused := &url.Error{Op: "Get", URL: UsageURL, Err: errors.New("Forbidden")}
	if !proxyRefused(refused) {
		t.Error("a transport Forbidden must be classified as a proxy refusal")
	}
	if proxyRefused(errors.New("connection reset by peer")) {
		t.Error("an unrelated transport error must not be classified as a refusal")
	}
	var target *ProxyRefusedError
	if !errors.As(error(&ProxyRefusedError{URL: "u", Err: refused}), &target) {
		t.Error("ProxyRefusedError must be matchable with errors.As")
	}
}

func TestFetch_Non2xxIsStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"detail":"nope"}`))
	}))
	defer srv.Close()

	withTestURL(t, srv.URL)
	_, err := Fetch(t.Context(), srv.Client(), "tok")
	if err == nil {
		t.Fatal("expected an error")
	}
	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("expected *StatusError, got %T", err)
	}
	if statusErr.Status != http.StatusForbidden {
		t.Errorf("status = %d, want 403", statusErr.Status)
	}
}

func TestFetch_RequiresToken(t *testing.T) {
	if _, err := Fetch(t.Context(), nil, "  "); err == nil {
		t.Fatal("expected an error for a blank token")
	}
}

func withTestURL(t *testing.T, url string) {
	t.Helper()
	prev := UsageURL
	UsageURL = url
	t.Cleanup(func() { UsageURL = prev })
}

func TestWindow_Remaining(t *testing.T) {
	var nilW *Window
	if nilW.Remaining() != 0 {
		t.Errorf("expected 0 for nil window, got %f", nilW.Remaining())
	}
	w := &Window{UsedPercent: 35.5}
	if w.Remaining() != 64.5 {
		t.Errorf("expected 64.5, got %f", w.Remaining())
	}
	wOver := &Window{UsedPercent: 110.0}
	if wOver.Remaining() != 0 {
		t.Errorf("expected 0 for over-limit window, got %f", wOver.Remaining())
	}
}

func TestErrors_StringAndUnwrap(t *testing.T) {
	se := &StatusError{Status: 404}
	if se.Error() == "" || !strings.Contains(se.Error(), "404") {
		t.Errorf("unexpected StatusError.Error(): %s", se.Error())
	}

	inner := errors.New("connection refused")
	pre := &ProxyRefusedError{URL: "https://example.com", Err: inner}
	if pre.Unwrap() != inner {
		t.Errorf("ProxyRefusedError.Unwrap() = %v, want %v", pre.Unwrap(), inner)
	}
	if !strings.Contains(pre.Error(), "https://example.com") {
		t.Errorf("unexpected ProxyRefusedError.Error(): %s", pre.Error())
	}

	rce := resetError(409, "no_credit", "no available credits")
	if !strings.Contains(rce.Error(), "no_credit") || !strings.Contains(rce.Error(), "no available credits") {
		t.Errorf("unexpected ResetCreditError.Error(): %s", rce.Error())
	}
}

func TestFiniteNum_EdgeCases(t *testing.T) {
	cases := []struct {
		input any
		want  float64
	}{
		{int(10), 10.0},
		{"25.5", 25.5},
		{"invalid", 0.0},
		{math.NaN(), 0.0},
		{math.Inf(1), 0.0},
		{map[string]any{"val": 55.0}, 55.0},
		{map[string]any{"val": "77.5"}, 77.5},
		{map[string]any{"other": 1.0}, 0.0},
		{nil, 0.0},
	}
	for _, tc := range cases {
		got := finiteNum(tc.input)
		if got != tc.want {
			t.Errorf("finiteNum(%v) = %f, want %f", tc.input, got, tc.want)
		}
	}
}

func TestFetch_SuccessWithDefaultClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":10}}}`))
	}))
	defer srv.Close()

	withTestURL(t, srv.URL)
	u, err := Fetch(t.Context(), srv.Client(), "valid-token")
	if err != nil {
		t.Fatalf("unexpected fetch error: %v", err)
	}
	if u.Plan != "plus" {
		t.Errorf("expected plan plus, got %s", u.Plan)
	}
}
