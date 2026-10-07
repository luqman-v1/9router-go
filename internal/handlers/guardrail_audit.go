package handlers

import (
	json "encoding/json/v2"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/guardrails"
	"9router/proxy/internal/log"
	"9router/proxy/internal/observ"
)

// guardrailAudit persists one guardrail decision per firing.
//
// Writing the audit row is best-effort and deliberately not allowed to fail the
// request: a full disk or a locked database must not turn a log_only policy
// into a block. The failure is visible in the error log instead.
func guardrailAudit(repo *db.Repo) guardrails.Audit {
	return func(d guardrails.Decision, t guardrails.Target) {
		start := time.Now()
		observ.RecordGuardrailDecision(
			firstDetector(d),
			string(d.Action),
			string(guardrails.DirectionInbound),
			time.Since(start),
		)
		findings, err := json.Marshal(d.Findings)
		if err != nil {
			findings = []byte("[]")
		}
		row := &db.GuardrailLog{
			TenantID:  db.DefaultGuardrailTenant,
			APIKeyID:  t.APIKeyID,
			Model:     t.Model,
			Detector:  firstDetector(d),
			Direction: string(guardrails.DirectionInbound),
			Action:    string(d.Action),
			Reason:    d.Reason,
			Findings:  string(findings),
			Severity:  highestSeverity(d),
		}
		if err := repo.InsertGuardrailLog(row); err != nil {
			log.Warn("guardrails", "audit write failed", "error", err)
		}
	}
}

// firstDetector names the detectors that fired, comma-joined, so one log row
// covers a decision that several detectors contributed to.
func firstDetector(d guardrails.Decision) string {
	if len(d.Findings) == 0 {
		return "unknown"
	}
	seen := make(map[string]struct{}, len(d.Findings))
	names := make([]string, 0, len(d.Findings))
	for _, f := range d.Findings {
		if _, dup := seen[f.Detector]; dup {
			continue
		}
		seen[f.Detector] = struct{}{}
		names = append(names, f.Detector)
	}
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ","
		}
		out += n
	}
	return out
}

// highestSeverity reports the most severe finding on the decision.
func highestSeverity(d guardrails.Decision) string {
	rank := map[guardrails.Severity]int{
		guardrails.SeverityLow:    1,
		guardrails.SeverityMedium: 2,
		guardrails.SeverityHigh:   3,
	}
	best := guardrails.SeverityLow
	bestRank := 0
	for _, f := range d.Findings {
		if r := rank[f.Severity]; r > bestRank {
			best, bestRank = f.Severity, r
		}
	}
	return string(best)
}