// Package policy resolves the effective policy for one API key: the reusable
// plan template assigned to the key, overlaid with the key's own columns.
//
// It exists so there is exactly ONE answer to "what limits does this key
// have?". The rate limiter reads RPM, TPM and concurrency from here instead of
// off the key's columns directly, and the model gate unions the plan's
// allowlist with the key's; a plan and a key override that were resolved
// independently would drift into two different answers for the same request.
//
// Precedence, least specific first:
//
//  1. the operator's global rate-limit default (supplied by the caller),
//  2. the plan assigned to the key,
//  3. the key's own column, when set (> 0 / non-empty).
//
// A key with no plan (`planId` empty) resolves to exactly what its columns
// say, so every key minted before this feature behaves as it did before.
package policy

import (
	"strings"

	"9router/proxy/internal/db"
	"9router/proxy/internal/models"
)

// PlanSource loads one plan by id. *db.Repo satisfies it.
type PlanSource interface {
	GetPlan(id string) (db.Plan, error)
}

// EffectivePolicy is the resolved policy for one API key.
//
// The budget fields (LimitMicros, LimitTokens, Period, AlertPct, HardCutoff)
// are carried here so the budget engine reads them from the same resolution
// the limiter uses; before the budget engine lands they are inert data.
type EffectivePolicy struct {
	PlanID string
	RPM    int
	TPM    int
	// Concurrency caps how many requests the key may have in flight.
	Concurrency int
	// QueueTimeoutMs is a per-key wait budget for a free concurrency slot.
	QueueTimeoutMs int
	// TokenQuota caps the key's cumulative prompt+completion tokens across
	// its lifetime. It is never inherited from a plan: a plan's LimitTokens
	// is a per-period budget, which is a different guarantee.
	TokenQuota int
	// LimitMicros is the per-period spend ceiling in USD micros (integer, so
	// no float rounding can let a key overdraw).
	LimitMicros int64
	// LimitTokens is the per-period token ceiling.
	LimitTokens int64
	// Period is the budget window: daily | weekly | monthly | total.
	Period string
	// AlertPct is the spend percentage at which a warning is raised.
	AlertPct int
	// HardCutoff refuses the request once the period budget is exhausted.
	HardCutoff bool
	// AllowedModels is the plan's model allowlist, if any. The model gate
	// unions it with the key's own allowlist.
	AllowedModels []string
}

// Resolve returns the effective policy for a key.
//
// A nil key resolves to the zero policy, and a nil PlanSource means the
// install has no plans wired, both of which leave the caller's global default
// in charge.
func Resolve(plans PlanSource, key *models.APIKey) EffectivePolicy {
	var out EffectivePolicy
	if key == nil {
		return out
	}
	applyPlan(&out, plans, key.PlanID)
	applyKeyOverrides(&out, key)
	return out
}

// applyPlan fills the policy from the key's plan.
//
// A failed or missing plan read degrades to "no plan" rather than failing the
// request: a broken policy read must not refuse traffic the key has always
// been allowed to send. The model allowlist makes the same choice.
func applyPlan(out *EffectivePolicy, plans PlanSource, planID *string) {
	if plans == nil || planID == nil {
		return
	}
	id := strings.TrimSpace(*planID)
	if id == "" {
		return
	}
	plan, err := plans.GetPlan(id)
	if err != nil {
		return
	}
	out.PlanID = plan.ID
	out.RPM = plan.RPMLimit
	out.TPM = plan.TPMLimit
	out.Concurrency = plan.ConcurrencyLimit
	out.LimitMicros = plan.LimitMicros
	out.LimitTokens = plan.LimitTokens
	out.Period = plan.Period
	out.AlertPct = plan.AlertPct
	out.HardCutoff = plan.HardCutoff
	out.AllowedModels = plan.AllowedModels
}

// applyKeyOverrides lets the key's own columns win where they are set.
//
// Only a positive value overrides: 0 means "not set" in every one of these
// columns, which is what keeps a plan from being silently flattened by a key
// that never configured the field.
func applyKeyOverrides(out *EffectivePolicy, key *models.APIKey) {
	if v := derefInt(key.RateLimitRPM); v > 0 {
		out.RPM = v
	}
	if v := derefInt(key.RateLimitTPM); v > 0 {
		out.TPM = v
	}
	if v := derefInt(key.RateLimitConcurrency); v > 0 {
		out.Concurrency = v
	}
	if v := derefInt(key.QueueTimeoutMs); v > 0 {
		out.QueueTimeoutMs = v
	}
	if v := derefInt(key.TokenQuota); v > 0 {
		out.TokenQuota = v
	}
}

// derefInt reads a nullable int column, treating nil as 0 ("not set").
func derefInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}
