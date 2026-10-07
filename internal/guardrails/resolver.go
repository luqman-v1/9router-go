package guardrails

import (
	"database/sql"
	"errors"
	json "encoding/json/v2"
	"fmt"
	"strings"
)

// Target identifies what a request is headed for, used to pick the most
// specific matching policy.
type Target struct {
	Provider string
	Model    string
	APIKeyID string
}

// policy is the subset of a stored policy the resolver needs. Keeping the
// store an interface lets the engine and resolver be tested without a
// database, and keeps this package independent of the repository layer.
type policy struct {
	Config string
}

// PolicyStore is the repository capability the resolver needs.
type PolicyStore interface {
	ListGuardrailPolicies(scope, scopeID string) ([]policy, error)
}

// policyConfig is the JSON shape stored in guardrail_policies.config.
type policyConfig struct {
	Detectors []string `json:"detectors"`
	Action    string   `json:"action"`
}

// sqlPolicyStore adapts *sql.DB to PolicyStore.
type sqlPolicyStore struct{ db *sql.DB }

// ListGuardrailPolicies implements PolicyStore.
func (s sqlPolicyStore) ListGuardrailPolicies(scope, scopeID string) ([]policy, error) {
	rows, err := s.db.Query(
		`SELECT config FROM guardrail_policies
		 WHERE tenant_id = 'default' AND scope = ? AND scope_id = ? AND enabled = 1
		 ORDER BY id`,
		scope, scopeID,
	)
	if err != nil {
		return nil, fmt.Errorf("guardrails: list policies: %w", err)
	}
	defer rows.Close()

	var out []policy
	for rows.Next() {
		var p policy
		if err := rows.Scan(&p.Config); err != nil {
			return nil, fmt.Errorf("guardrails: scan policy: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("guardrails: read policies: %w", err)
	}
	return out, nil
}

// NewStore returns a PolicyStore backed directly by the database handle.
func NewStore(db *sql.DB) PolicyStore { return sqlPolicyStore{db: db} }

// scopeChain lists the scopes to consult, least specific first. The last
// non-empty result wins, which is what "most specific overrides" means in
// practice: a model policy can narrow a global one.
func scopeChain(t Target) []struct {
	Scope   string
	ScopeID string
} {
	chain := []struct {
		Scope   string
		ScopeID string
	}{{string(ScopeGlobal), ""}}
	if t.Provider != "" {
		chain = append(chain, struct {
			Scope   string
			ScopeID string
		}{string(ScopeProvider), t.Provider})
	}
	if t.Model != "" {
		chain = append(chain, struct {
			Scope   string
			ScopeID string
		}{string(ScopeModel), t.Model})
	}
	if t.APIKeyID != "" {
		chain = append(chain, struct {
			Scope   string
			ScopeID string
		}{string(ScopeAPIKey), t.APIKeyID})
	}
	return chain
}

// Resolve returns the engine that applies to this target, or nil when no policy
// enables anything.
//
// Resolution is last-wins down the chain, and a more specific policy replaces a
// broader one outright rather than merging into it — an operator who narrows a
// global policy for one model should not have to restate every detector.
func Resolve(store PolicyStore, t Target) (*Engine, error) {
	var resolved *policyConfig
	for _, sc := range scopeChain(t) {
		policies, err := store.ListGuardrailPolicies(sc.Scope, sc.ScopeID)
		if err != nil {
			return nil, err
		}
		if len(policies) == 0 {
			continue
		}
		for _, p := range policies {
			cfg, err := parsePolicyConfig(p.Config)
			if err != nil {
				// A malformed row must not take the whole gateway's traffic
				// down with it; skip it and keep the last good resolution.
				continue
			}
			if len(cfg.Detectors) == 0 {
				continue
			}
			resolved = cfg
		}
	}
	if resolved == nil {
		return nil, nil
	}
	return NewEngine(resolved.Detectors, Action(resolved.Action)), nil
}

func parsePolicyConfig(raw string) (*policyConfig, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("guardrails: empty policy config")
	}
	var cfg policyConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, fmt.Errorf("guardrails: parse policy config: %w", err)
	}
	return &cfg, nil
}