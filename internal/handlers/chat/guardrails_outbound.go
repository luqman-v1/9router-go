package chat

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/guardrails"
	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
	"9router/proxy/internal/middleware"
	"9router/proxy/internal/observ"
	"9router/proxy/internal/translator"
)

// resolveGuardrailEngine returns the engine for this key and model, or nil when
// no policy applies. A nil engine means the response path stays completely
// untouched, which is what keeps guardrails free when the feature is not
// configured.
func (h *ChatHandler) resolveGuardrailEngine(keyID, model string) *guardrails.Engine {
	if h == nil || h.Repo == nil {
		return nil
	}
	engine, err := guardrails.Resolve(
		guardrails.NewStore(h.Repo.RawDB()),
		guardrails.Target{APIKeyID: keyID, Model: model},
	)
	if err != nil || engine == nil || !engine.Enabled() {
		return nil
	}
	return engine
}

// requestKeyID returns the authenticated API key id for this request, or "".
func requestKeyID(r *http.Request) string {
	if r == nil {
		return ""
	}
	key := middleware.GetAuthenticatedApiKey(r)
	if key == nil {
		return ""
	}
	return key.ID
}

// guardrailKeyContextKey carries the authenticated key id from the dispatch
// path to the response path. It is unexported so nothing outside this package
// can spoof a policy scope.
type guardrailKeyContextKey struct{}

// withGuardrailKey records the dispatching key on the context. A keyless caller
// leaves it unset and the tap stays inert.
func withGuardrailKey(ctx context.Context, keyID string) context.Context {
	if ctx == nil || keyID == "" {
		return ctx
	}
	return context.WithValue(ctx, guardrailKeyContextKey{}, keyID)
}

// guardrailKeyIDFromContext returns the dispatching key id, or "".
func guardrailKeyIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(guardrailKeyContextKey{}).(string)
	return v
}

// guardrailTarget is the scope this response is judged under.
func guardrailTarget(ctx context.Context) guardrails.Target {
	return guardrails.Target{
		APIKeyID: guardrailKeyIDFromContext(ctx),
		Model:    translator.RequestedModelFromContext(ctx),
	}
}

// withOutboundPolicy resolves the response policy and installs it on the
// context, which is how it reaches the provider executors.
//
// The executors are shared by every provider, so a per-key policy cannot live
// in their signatures. The context is the one thing a request already carries
// into all of them, so the resolved policy travels on it and each response
// path picks it up where it writes.
func (h *ChatHandler) withOutboundPolicy(ctx context.Context) context.Context {
	// The same kill-switch the inbound tap reads. It is installed first and
	// unconditionally: an operator who turns guardrails off expects the
	// response path to stop too, and neither tap should be able to outlive the
	// other's decision.
	ctx = guardrails.WithSwitch(ctx, h.guardrailSwitch())
	target := guardrailTarget(ctx)
	engine := h.resolveGuardrailEngine(target.APIKeyID, target.Model)
	if engine == nil {
		return ctx
	}
	return guardrails.WithPolicy(ctx, &guardrails.Policy{
		Engine: engine,
		Audit:  h.guardrailAudit(),
		Target: target,
	})
}

// guardrailSwitch reads the global kill-switch from settings. It defaults to on
// when the row cannot be read: an operator who configured a policy would rather
// eat a transient lookup error than silently lose their filter.
func (h *ChatHandler) guardrailSwitch() guardrails.Switch {
	if h == nil || h.Repo == nil {
		return nil
	}
	return func() bool { return h.Repo.GetGuardrailsEnabled() }
}

// guardrailAudit records an outbound decision. The row is written best-effort:
// a failure here must not turn a filter into a client error.
func (h *ChatHandler) guardrailAudit() guardrails.Audit {
	if h == nil || h.Repo == nil {
		return nil
	}
	return func(d guardrails.Decision, target guardrails.Target) {
		start := time.Now()
		observ.RecordGuardrailDecision(
			detectorsOf(d),
			string(d.Action),
			string(guardrails.DirectionOutbound),
			time.Since(start),
		)
		findings, err := json.Marshal(d.Findings)
		if err != nil {
			findings = []byte("[]")
		}
		row := &db.GuardrailLog{
			TenantID:  db.DefaultGuardrailTenant,
			APIKeyID:  target.APIKeyID,
			Model:     target.Model,
			Detector:  detectorsOf(d),
			Direction: string(guardrails.DirectionOutbound),
			Action:    string(d.Action),
			Reason:    d.Reason,
			Findings:  string(findings),
			Severity:  worstSeverity(d),
		}
		if err := h.Repo.InsertGuardrailLog(row); err != nil {
			log.Warn("guardrails", "outbound audit write failed", "error", err)
		}
	}
}

// streamFormat names the wire format a response stream is written in, which is
// what decides the frames a guardrail block has to close the stream with.
func streamFormat(ctx context.Context, endpoint string) guardrails.StreamFormat {
	switch {
	case translator.IsResponsesClient(ctx):
		return guardrails.FormatResponses
	case endpoint == "/v1/messages" || endpoint == "/v1/v1/messages":
		return guardrails.FormatClaude
	default:
		return guardrails.FormatOpenAI
	}
}

// requestKeyFromContext returns the authenticated client key for a request in
// flight, or nil on a route with no API-key middleware.
//
// The metering path needs it to reconcile a token reservation against the key
// that was charged, and the key is already on the context from RequireApiKey.
func requestKeyFromContext(ctx context.Context) *models.APIKey {
	if ctx == nil {
		return nil
	}
	key, _ := ctx.Value(middleware.ApiKeyContextKey).(*models.APIKey)
	return key
}

// rateLimitDefaults reads the operator's global rate-limit fallback.
//
// The TPM settle hook needs the same limit the limiter charged against, or a
// reconcile would compare the real usage to a different number.
func (h *ChatHandler) rateLimitDefaults() middleware.Defaults {
	if h == nil || h.Repo == nil {
		return nil
	}
	return func() middleware.Limits {
		d := h.Repo.GetRateLimitDefaults()
		if !d.Enabled {
			return middleware.Limits{}
		}
		return middleware.Limits{RPM: d.RPM, TPM: d.TPM, Concurrency: d.Concurrency}
	}
}

// newGuardrailTap wraps a streamed response's writer in an outbound tap.
//
// A stream is tapped at the writer, because there is no point after its first
// byte at which the answer could be taken back. A non-streamed response is not
// tapped: it is buffered whole before anything is written, so the response path
// can judge it as a complete document and answer with a real status instead of
// a cut stream nobody asked to be cut.
func guardrailTap(ctx context.Context, w http.ResponseWriter, endpoint string, isStream bool) (http.ResponseWriter, *guardrails.Outbound) {
	if !isStream {
		return w, nil
	}
	out := guardrails.ApplyOutbound(ctx, w, streamFormat(ctx, endpoint))
	if out == nil {
		return w, nil
	}
	return out, out
}

// guardrailStreamOutcome turns a cut stream into the failure the fallback layer
// and the usage log should see.
//
// Once a stream has been blocked the transport succeeded — the client was told
// 200 and got a properly terminated event stream — so reporting success would
// bill the turn as served and clear the account cooldown for content nobody
// received. Reporting the upstream's own error instead would be a lie. The
// block gets its own non-retryable failure instead.
func guardrailStreamOutcome(tap *guardrails.Outbound, streamErr error) error {
	if tap == nil || !tap.Blocked() {
		return streamErr
	}
	return guardrailBlockedError()
}

// isGuardrailBlock reports whether err is a policy refusal, which is the one
// upstream-shaped failure that must not be retried or failed over.
//
// It matches on the status rather than on a concrete type because the executor
// layer reports a block through proxy.UpstreamError, which by the time the
// routing layers see it is indistinguishable from any other upstream failure.
// The status is the contract: nothing else in the gateway answers with 451.
func isGuardrailBlock(err error) bool {
	var ue *upstreamError
	// errors.As only assigns on a match, so a non-match leaves ue nil — the
	// type assertion has to be nil-checked before the status is read.
	return errors.As(err, &ue) && ue != nil && ue.StatusCode == http.StatusUnavailableForLegalReasons
}

// guardrailBlockedError is the failure a policy block reports.
//
// Its status is deliberately not one of the 502s the unusable-upstream helpers
// use. 502 is in providers.RetryableStatusCodes, so a blocked answer would lock
// the account it came from and hand the very content the policy refused to the
// next connection or model. A policy decision is not an upstream fault: the
// upstream served correctly and the answer was still refused, so no other
// account will do better and none of them should see it.
func guardrailBlockedError() error {
	body, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": guardrails.BlockedMessage + ".",
			"type":    "guardrail_error",
			"code":    http.StatusUnavailableForLegalReasons,
		},
	})
	if err != nil {
		body = []byte(`{"error":{"message":"` + guardrails.BlockedMessage + `.","type":"guardrail_error","code":451}}`)
	}
	return &upstreamError{
		StatusCode: http.StatusUnavailableForLegalReasons,
		Body:       body,
	}
}

// detectorsOf names every detector that contributed to a decision, deduplicated.
func detectorsOf(d guardrails.Decision) string {
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
	return strings.Join(names, ",")
}

// worstSeverity reports the most severe finding on a decision.
func worstSeverity(d guardrails.Decision) string {
	rank := map[guardrails.Severity]int{
		guardrails.SeverityLow:    1,
		guardrails.SeverityMedium: 2,
		guardrails.SeverityHigh:   3,
	}
	best, bestRank := guardrails.SeverityLow, 0
	for _, f := range d.Findings {
		if r := rank[f.Severity]; r > bestRank {
			best, bestRank = f.Severity, r
		}
	}
	return string(best)
}
