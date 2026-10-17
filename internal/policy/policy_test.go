package policy

import (
	"errors"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/models"
)

// stubPlans is a PlanSource that answers from a map, or fails on demand.
type stubPlans struct {
	plans map[string]db.Plan
	err   error
}

func (s stubPlans) GetPlan(id string) (db.Plan, error) {
	if s.err != nil {
		return db.Plan{}, s.err
	}
	p, ok := s.plans[id]
	if !ok {
		return db.Plan{}, errors.New("plan not found")
	}
	return p, nil
}

func TestResolve(t *testing.T) {
	pro := db.Plan{
		ID:               "pro",
		RPMLimit:         120,
		TPMLimit:         90000,
		ConcurrencyLimit: 4,
		LimitMicros:      5_000_000,
		LimitTokens:      1_000_000,
		Period:           "monthly",
		AlertPct:         80,
		HardCutoff:       true,
		AllowedModels:    []string{"gpt-4o", "claude-*"},
	}
	src := stubPlans{plans: map[string]db.Plan{"pro": pro}}

	// planOnly is the policy a key with the pro plan and no columns of its own
	// resolves to; it is the want of every case that must not flatten the plan.
	planOnly := EffectivePolicy{
		PlanID: "pro", RPM: 120, TPM: 90000, Concurrency: 4,
		LimitMicros: 5_000_000, LimitTokens: 1_000_000,
		Period: "monthly", AlertPct: 80, HardCutoff: true,
		AllowedModels: []string{"gpt-4o", "claude-*"},
	}

	tests := []struct {
		name  string
		plans PlanSource
		key   *models.APIKey
		want  EffectivePolicy
	}{
		{
			name:  "nil key resolves to the zero policy",
			plans: src,
			key:   nil,
			want:  EffectivePolicy{},
		},
		{
			name:  "no plan id leaves the key's own columns in charge",
			plans: src,
			key: &models.APIKey{
				RateLimitRPM: new(30), RateLimitTPM: new(4000), RateLimitConcurrency: new(2),
			},
			want: EffectivePolicy{RPM: 30, TPM: 4000, Concurrency: 2},
		},
		{
			name:  "empty plan id is treated as no plan",
			plans: src,
			key: &models.APIKey{
				PlanID: new(""), RateLimitRPM: new(30),
			},
			want: EffectivePolicy{RPM: 30},
		},
		{
			name:  "plan applies when the key sets nothing",
			plans: src,
			key:   &models.APIKey{PlanID: new("pro")},
			want:  planOnly,
		},
		{
			name:  "key column overrides the plan",
			plans: src,
			key: &models.APIKey{
				PlanID: new("pro"), RateLimitRPM: new(600), RateLimitTPM: new(1),
			},
			want: EffectivePolicy{
				PlanID: "pro", RPM: 600, TPM: 1, Concurrency: 4,
				LimitMicros: 5_000_000, LimitTokens: 1_000_000,
				Period: "monthly", AlertPct: 80, HardCutoff: true,
				AllowedModels: []string{"gpt-4o", "claude-*"},
			},
		},
		{
			name:  "a zero key column does not flatten the plan",
			plans: src,
			key: &models.APIKey{
				PlanID: new("pro"), RateLimitRPM: new(0), TokenQuota: new(0),
			},
			want: planOnly,
		},
		{
			name:  "lifetime token quota is never inherited from a plan",
			plans: src,
			key:   &models.APIKey{PlanID: new("pro")},
			want:  planOnly,
		},
		{
			name:  "key queue timeout overrides the plan",
			plans: src,
			key:   &models.APIKey{PlanID: new("pro"), QueueTimeoutMs: new(1500)},
			want: EffectivePolicy{
				PlanID: "pro", RPM: 120, TPM: 90000, Concurrency: 4, QueueTimeoutMs: 1500,
				LimitMicros: 5_000_000, LimitTokens: 1_000_000,
				Period: "monthly", AlertPct: 80, HardCutoff: true,
				AllowedModels: []string{"gpt-4o", "claude-*"},
			},
		},
		{
			name:  "a failed plan read degrades to no plan",
			plans: stubPlans{err: errors.New("db down")},
			key:   &models.APIKey{PlanID: new("pro"), RateLimitRPM: new(30)},
			want:  EffectivePolicy{RPM: 30},
		},
		{
			name:  "a missing plan row degrades to no plan",
			plans: src,
			key:   &models.APIKey{PlanID: new("ghost"), RateLimitRPM: new(30)},
			want:  EffectivePolicy{RPM: 30},
		},
		{
			name:  "nil plan source leaves the key in charge",
			plans: nil,
			key:   &models.APIKey{PlanID: new("pro"), RateLimitRPM: new(30)},
			want:  EffectivePolicy{RPM: 30},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Resolve(tt.plans, tt.key)
			if got.PlanID != tt.want.PlanID || got.RPM != tt.want.RPM || got.TPM != tt.want.TPM ||
				got.Concurrency != tt.want.Concurrency || got.QueueTimeoutMs != tt.want.QueueTimeoutMs ||
				got.TokenQuota != tt.want.TokenQuota || got.LimitMicros != tt.want.LimitMicros ||
				got.LimitTokens != tt.want.LimitTokens || got.Period != tt.want.Period ||
				got.AlertPct != tt.want.AlertPct || got.HardCutoff != tt.want.HardCutoff {
				t.Fatalf("Resolve() = %+v, want %+v", got, tt.want)
			}
			if len(got.AllowedModels) != len(tt.want.AllowedModels) {
				t.Fatalf("AllowedModels = %v, want %v", got.AllowedModels, tt.want.AllowedModels)
			}
			for i := range got.AllowedModels {
				if got.AllowedModels[i] != tt.want.AllowedModels[i] {
					t.Fatalf("AllowedModels[%d] = %q, want %q", i, got.AllowedModels[i], tt.want.AllowedModels[i])
				}
			}
		})
	}
}

// TestResolvePlanIDWhitespaceIsTrimmed guards the lookup key: a plan id with
// stray whitespace must not silently resolve to nothing.
func TestResolvePlanIDWhitespaceIsTrimmed(t *testing.T) {
	src := stubPlans{plans: map[string]db.Plan{"pro": {ID: "pro", RPMLimit: 10}}}
	got := Resolve(src, &models.APIKey{PlanID: new("  pro  ")})
	if got.RPM != 10 || got.PlanID != "pro" {
		t.Errorf("Resolve() = %+v, want the pro plan (RPM 10)", got)
	}
}
