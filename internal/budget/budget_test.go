package budget

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestResolveWindow(t *testing.T) {
	// A fixed instant so the anchored windows are deterministic: Wednesday
	// 2026-11-04 14:30 UTC.
	now := time.Date(2026, 11, 4, 14, 30, 0, 0, time.UTC)

	tests := []struct {
		name   string
		period Period
		want   Window
	}{
		{
			name:   "daily starts at midnight",
			period: PeriodDaily,
			want: Window{
				Since:   "2026-11-04T00:00:00Z",
				Until:   "2026-11-05T00:00:00Z",
				ResetAt: time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC),
			},
		},
		{
			name:   "weekly starts Monday",
			period: PeriodWeekly,
			want: Window{
				Since:   "2026-11-02T00:00:00Z",
				Until:   "2026-11-09T00:00:00Z",
				ResetAt: time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC),
			},
		},
		{
			name:   "monthly starts on the first",
			period: PeriodMonthly,
			want: Window{
				Since:   "2026-11-01T00:00:00Z",
				Until:   "2026-12-01T00:00:00Z",
				ResetAt: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC),
			},
		},
		{
			name:   "total is unbounded",
			period: PeriodTotal,
			want:   Window{},
		},
		{
			// A typo must not silently disable the ceiling.
			name:   "unknown falls back to monthly",
			period: "yerly",
			want: Window{
				Since:   "2026-11-01T00:00:00Z",
				Until:   "2026-12-01T00:00:00Z",
				ResetAt: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC),
			},
		},
		{
			name:   "empty falls back to monthly",
			period: "",
			want: Window{
				Since:   "2026-11-01T00:00:00Z",
				Until:   "2026-12-01T00:00:00Z",
				ResetAt: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveWindow(tt.period, now)
			if got.Since != tt.want.Since || got.Until != tt.want.Until {
				t.Fatalf("ResolveWindow(%q) = %+v, want %+v", tt.period, got, tt.want)
			}
			if !got.ResetAt.Equal(tt.want.ResetAt) {
				t.Errorf("ResetAt = %v, want %v", got.ResetAt, tt.want.ResetAt)
			}
		})
	}
}

// Sunday must roll back to the previous Monday rather than forward to the next
// one: a Sunday request belongs to the week that started six days earlier.
func TestResolveWindow_SundayRollsBack(t *testing.T) {
	sunday := time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)
	got := ResolveWindow(PeriodWeekly, sunday)
	if got.Since != "2026-11-02T00:00:00Z" {
		t.Errorf("Sunday weekly Since = %q, want 2026-11-02T00:00:00Z", got.Since)
	}
}

func TestLimitsArmed(t *testing.T) {
	tests := []struct {
		name   string
		limits Limits
		want   bool
	}{
		{"nothing configured", Limits{}, false},
		{"cost only", Limits{LimitMicros: 1_000_000}, true},
		{"tokens only", Limits{LimitTokens: 1000}, true},
		{"both", Limits{LimitMicros: 1, LimitTokens: 1}, true},
		{"alert pct alone is not a budget", Limits{AlertPct: 80}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.limits.Armed(); got != tt.want {
				t.Errorf("Armed() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDecide(t *testing.T) {
	reset := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		spend   Spend
		limits  Limits
		want    Decision
	}{
		{
			name:   "under both ceilings passes",
			spend:  Spend{Micros: 500_000, Tokens: 400},
			limits: Limits{LimitMicros: 1_000_000, LimitTokens: 1000, Period: PeriodMonthly, AlertPct: 80},
			want:   Decision{Spend: Spend{Micros: 500_000, Tokens: 400}, Limits: Limits{LimitMicros: 1_000_000, LimitTokens: 1000, Period: PeriodMonthly, AlertPct: 80}, ResetAt: reset},
		},
		{
			name:   "cost ceiling reached without hard cutoff alerts",
			spend:  Spend{Micros: 1_000_000, Tokens: 10},
			limits: Limits{LimitMicros: 1_000_000, Period: PeriodMonthly, AlertPct: 80},
			want: Decision{
				Alerting: true,
				Spend:    Spend{Micros: 1_000_000, Tokens: 10},
				Limits:   Limits{LimitMicros: 1_000_000, Period: PeriodMonthly, AlertPct: 80},
				ResetAt:  reset,
			},
		},
		{
			name:   "cost ceiling with hard cutoff refuses",
			spend:  Spend{Micros: 1_500_000, Tokens: 10},
			limits: Limits{LimitMicros: 1_000_000, Period: PeriodMonthly, HardCutoff: true},
			want: Decision{
				Refuse:   true,
				Axis:     "cost",
				Spend:    Spend{Micros: 1_500_000, Tokens: 10},
				Limits:   Limits{LimitMicros: 1_000_000, Period: PeriodMonthly, HardCutoff: true},
				ResetAt:  reset,
			},
		},
		{
			name:   "token ceiling with hard cutoff refuses",
			spend:  Spend{Micros: 10, Tokens: 2000},
			limits: Limits{LimitTokens: 1000, Period: PeriodDaily, HardCutoff: true},
			want: Decision{
				Refuse:  true,
				Axis:    "tokens",
				Spend:   Spend{Micros: 10, Tokens: 2000},
				Limits:  Limits{LimitTokens: 1000, Period: PeriodDaily, HardCutoff: true},
				ResetAt: time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC),
			},
		},
		{
			// Cost is checked first, so a key over on both names the money
			// axis — the one an operator reads as "you have spent $X".
			name:   "cost wins when both are exhausted",
			spend:  Spend{Micros: 2_000_000, Tokens: 5000},
			limits: Limits{LimitMicros: 1_000_000, LimitTokens: 1000, HardCutoff: true},
			want: Decision{
				Refuse:  true,
				Axis:    "cost",
				Spend:   Spend{Micros: 2_000_000, Tokens: 5000},
				Limits:  Limits{LimitMicros: 1_000_000, LimitTokens: 1000, HardCutoff: true},
				ResetAt: reset,
			},
		},
		{
			name:   "alert threshold crossed on tokens",
			spend:  Spend{Micros: 0, Tokens: 850},
			limits: Limits{LimitTokens: 1000, Period: PeriodMonthly, AlertPct: 80},
			want: Decision{
				Alerting: true,
				Spend:    Spend{Tokens: 850},
				Limits:   Limits{LimitTokens: 1000, Period: PeriodMonthly, AlertPct: 80},
				ResetAt:  reset,
			},
		},
		{
			name:   "alert threshold not yet crossed",
			spend:  Spend{Micros: 100_000, Tokens: 100},
			limits: Limits{LimitMicros: 1_000_000, Period: PeriodMonthly, AlertPct: 80},
			want: Decision{
				Spend:   Spend{Micros: 100_000, Tokens: 100},
				Limits:  Limits{LimitMicros: 1_000_000, Period: PeriodMonthly, AlertPct: 80},
				ResetAt: reset,
			},
		},
		{
			// Exactly at the threshold counts as reached: 80% of 1000 is 800.
			name:   "alert threshold is inclusive",
			spend:  Spend{Tokens: 800},
			limits: Limits{LimitTokens: 1000, Period: PeriodMonthly, AlertPct: 80},
			want: Decision{
				Alerting: true,
				Spend:    Spend{Tokens: 800},
				Limits:   Limits{LimitTokens: 1000, Period: PeriodMonthly, AlertPct: 80},
				ResetAt:  reset,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decide(tt.spend, tt.limits, limitsReset(t, tt.limits.Period))
			if got.Refuse != tt.want.Refuse || got.Alerting != tt.want.Alerting || got.Axis != tt.want.Axis {
				t.Fatalf("decide() = %+v, want %+v", got, tt.want)
			}
			if got.Spend != tt.want.Spend {
				t.Errorf("Spend = %+v, want %+v", got.Spend, tt.want.Spend)
			}
			if !got.ResetAt.Equal(tt.want.ResetAt) {
				t.Errorf("ResetAt = %v, want %v", got.ResetAt, tt.want.ResetAt)
			}
		})
	}
}

// limitsReset derives the ResetAt the decide() call is expected to carry, so
// the table above does not repeat it per case.
func limitsReset(t *testing.T, period Period) time.Time {
	t.Helper()
	now := time.Date(2026, 11, 4, 14, 30, 0, 0, time.UTC)
	return ResolveWindow(period, now).ResetAt
}

type stubReader struct {
	spend Spend
	err   error
	calls int
}

func (s *stubReader) SpendForKeySince(_ context.Context, _, _ string) (Spend, error) {
	s.calls++
	return s.spend, s.err
}

func TestCheck(t *testing.T) {
	now := time.Date(2026, 11, 4, 14, 30, 0, 0, time.UTC)

	t.Run("unarmed key costs no read", func(t *testing.T) {
		r := &stubReader{spend: Spend{Micros: 9_999_999}}
		got := Check(context.Background(), r, "sk-test", Limits{}, now)
		if got.Refuse || got.Alerting {
			t.Errorf("decision = %+v, want empty", got)
		}
		if r.calls != 0 {
			t.Errorf("reader called %d times, want 0 for an unarmed key", r.calls)
		}
	})

	t.Run("empty key costs no read", func(t *testing.T) {
		r := &stubReader{}
		got := Check(context.Background(), r, "", Limits{LimitMicros: 1}, now)
		if got.Refuse || got.Alerting {
			t.Errorf("decision = %+v, want empty", got)
		}
		if r.calls != 0 {
			t.Errorf("reader called %d times, want 0 for an empty key", r.calls)
		}
	})

	// A storage blip must not lock a key out of a service it has always been
	// able to call: the ceiling is a spend cap, not an access grant.
	t.Run("read failure fails open", func(t *testing.T) {
		r := &stubReader{err: errors.New("db down")}
		got := Check(context.Background(), r, "sk-test", Limits{LimitMicros: 1, HardCutoff: true}, now)
		if got.Refuse {
			t.Errorf("decision = %+v, want fail-open", got)
		}
		if r.calls != 1 {
			t.Errorf("reader called %d times, want 1", r.calls)
		}
	})

	t.Run("nil reader fails open", func(t *testing.T) {
		got := Check(context.Background(), nil, "sk-test", Limits{LimitMicros: 1, HardCutoff: true}, now)
		if got.Refuse {
			t.Errorf("decision = %+v, want fail-open", got)
		}
	})

	t.Run("exhausted key refuses", func(t *testing.T) {
		r := &stubReader{spend: Spend{Micros: 2_000_000, Tokens: 10}}
		got := Check(context.Background(), r, "sk-test", Limits{LimitMicros: 1_000_000, Period: PeriodMonthly, HardCutoff: true}, now)
		if !got.Refuse || got.Axis != "cost" {
			t.Fatalf("decision = %+v, want a cost refusal", got)
		}
		if got.Spend.Micros != 2_000_000 {
			t.Errorf("Spend.Micros = %d, want 2000000", got.Spend.Micros)
		}
	})
}

func TestFormatMicros(t *testing.T) {
	tests := []struct {
		micros int64
		want   string
	}{
		{0, "0.00"},
		{1_000_000, "1.00"},
		{1_500_000, "1.50"},
		{123_456, "0.12"},
		{2_500_000, "2.50"},
	}
	for _, tt := range tests {
		if got := FormatMicros(tt.micros); got != tt.want {
			t.Errorf("FormatMicros(%d) = %q, want %q", tt.micros, got, tt.want)
		}
	}
}
