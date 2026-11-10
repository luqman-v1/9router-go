// Package errclass classifies an upstream LLM error into a small,
// provider-agnostic taxonomy: a Kind (what failed) and a Scope (the narrowest
// routing resource it implicates). It is the deterministic half of failover
// policy — the same status code and body always land in the same bucket, so
// whatever routing decision reads the classification is reproducible and
// testable.
//
// The connection-rotation layer (internal/providers) answers a different
// question: "how long should this connection back off, and does this failure
// fall back at all". It already carries its own cooldown classifier
// (providers.ClassifyError) plus focused detectors for the two model-level
// conditions it acts on directly (providers.IsModelFitError,
// providers.IsModelDeprecation) and for a provider-injected error event inside
// an HTTP 200 stream (proxy.DetectInbandSSEError). None of those need the
// vocabulary here, so nothing in the request path calls into this package yet.
//
// errclass exists for the routing decision that layer deliberately does not
// make: not "how long", but "what kind of failure is this, and what does it
// implicate". A context-window overflow, a content-filter rejection and a dead
// credential can all read as "some 4xx" to a cooldown table, yet they demand
// different routing — advance to a model with a bigger window, advance to a
// provider with a looser safety policy, or stop dialling this account at all.
// The intended first consumers are therefore the chain/failover policy and the
// observability layer, which want a stable reason code to bucket and count
// failures by. The call sites to wire first are listed in the package report
// that landed this port; wiring them is deliberately deferred so this lands
// additive and behaviour-preserving.
//
// Every exported function is pure: string and JSON-body matching, no I/O, no
// globals mutated after init, no cgo. The phrase tables are package-level vars
// only so they are addressable slices; nothing writes to them at run time, and
// the package pulls in nothing beyond the standard library.
package errclass

// Kind names what kind of upstream failure occurred. It is the routing verb:
// each kind maps to one decision about whether to retry the same target,
// advance the chain, or surface the error to the client.
//
// The names mirror the vocabulary the wider codebase already uses for these
// conditions (rate limit, quota, context window, model fit) so a classification
// needs no translation before it is logged, counted or compared.
type Kind string

const (
	// KindAuth: the credential was rejected or has expired. Retrying the same
	// account cannot succeed; the chain advances to the next account.
	KindAuth Kind = "auth"
	// KindRateLimit: the account is being asked to slow down. A short wait
	// clears it, so cool the account down and let another serve the turn.
	KindRateLimit Kind = "rate_limit"
	// KindQuotaExhausted: the subscription or paid balance is spent. Waiting
	// does not help — only a top-up does — so park the account for a long
	// horizon rather than re-dialling a guaranteed-failing upstream.
	KindQuotaExhausted Kind = "quota_exhausted"
	// KindUpstream: a 5xx or transient provider fault. Worth one retry, then
	// fall back; nothing is wrong with the account or the request.
	KindUpstream Kind = "upstream"
	// KindTimeout: the stream stalled or the request ran past its deadline.
	KindTimeout Kind = "timeout"
	// KindBadRequest: a 4xx caused by the request itself. Falling back to
	// another account cannot change the answer, so surface it.
	KindBadRequest Kind = "bad_request"
	// KindContextWindow: the prompt does not fit the model's context window.
	// The request is valid for a model with a larger window, so the chain
	// advances to the next target without cooling anything down.
	KindContextWindow Kind = "context_window"
	// KindContentFilter: the upstream safety system rejected the prompt or
	// output. Another provider may accept it, so the chain advances; nothing
	// is cooled down.
	KindContentFilter Kind = "content_filter"
	// KindModelUnavailable: the selected model or endpoint is gone or not
	// served here. Skip the model without penalising credentials that may
	// still serve other models.
	KindModelUnavailable Kind = "model_unavailable"
)

// Scope names the narrowest resource a failure implicates. Cooldowns and
// health signals use it to avoid disabling healthy siblings: an account-scoped
// 429 says nothing about the model, and a model-scoped 404 says nothing about
// the credential.
type Scope string

const (
	// ScopeRequest: the failure is about this specific request, not about any
	// reusable resource. Nothing downstream should be cooled down.
	ScopeRequest Scope = "request"
	// ScopeModel: the model (its size, its availability) is the problem.
	ScopeModel Scope = "model"
	// ScopeAccount: the credential or its quota is the problem.
	ScopeAccount Scope = "account"
	// ScopeProvider: the provider as a whole is unhealthy or overloaded.
	ScopeProvider Scope = "provider"
)

// Classification is the outcome of classifying one upstream error: what failed,
// the narrowest resource it implicates, and the upstream status the frame
// carried (so telemetry can tell a network fault from a 5xx). StatusCode is
// zero when the source carried no usable status.
type Classification struct {
	Kind Kind
	// Scope is always populated by the classifiers in this package; a caller
	// that builds a Classification by hand can leave it empty and read it back
	// through EffectiveScope for the conservative default.
	Scope Scope
	// StatusCode is the upstream HTTP status, if any.
	StatusCode int
	// Message is the provider's own summary, kept for diagnosis.
	Message string
}

// EffectiveScope returns the failure scope, falling back to a conservative
// default derived from the Kind when Scope was left empty. A hand-built
// Classification with no Scope still needs a routing answer, and "request" is
// the one that cools nothing down.
func (c Classification) EffectiveScope() Scope {
	if c.Scope != "" {
		return c.Scope
	}
	switch c.Kind {
	case KindModelUnavailable, KindContextWindow:
		return ScopeModel
	case KindAuth, KindRateLimit, KindQuotaExhausted:
		return ScopeAccount
	case KindUpstream, KindTimeout:
		return ScopeProvider
	default:
		return ScopeRequest
	}
}
