// Package observ exposes the gateway's Prometheus metrics.
//
// Every Metrics instance owns a private registry: the default global registry
// is never touched, so two instances (parallel tests, an embedded second
// gateway) cannot collide with each other or with collectors the host process
// registered on its own.
package observ

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Token kinds recorded on TokensTotal. The set is closed on purpose: a free
// form kind would multiply the series count by whatever the caller invents.
const (
	TokenKindPrompt     = "prompt"
	TokenKindCompletion = "completion"
)

// Fallback reasons recorded on Fallbacks. Same reasoning as the token kinds.
const (
	FallbackReasonUpstreamError = "upstream_error"
	FallbackReasonNoConnection  = "no_connection"
	FallbackReasonUnhealthy     = "unhealthy"
)

// Metrics owns one registry and the collectors registered in it.
type Metrics struct {
	reg *prometheus.Registry

	RequestsTotal      *prometheus.CounterVec
	RequestDuration    *prometheus.HistogramVec
	TimeToFirstToken   *prometheus.HistogramVec
	TokensTotal        *prometheus.CounterVec
	CostMicros         *prometheus.CounterVec
	Fallbacks          *prometheus.CounterVec
	UpstreamErrors     *prometheus.CounterVec
	RateLimitRejects   *prometheus.CounterVec
	RateLimitQueue     *prometheus.HistogramVec
	GuardrailDecisions *prometheus.CounterVec
	GuardrailEval      *prometheus.HistogramVec
}

// latencyBuckets span a fast cached token (50ms) to a very slow streamed turn
// (2min). TTFT stops at 30s: anything past that is a stuck socket, and the
// +Inf bucket catches it without needing a bucket per minute.
var (
	latencyBuckets = []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120}
	ttftBuckets    = []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 15, 30}
	// queueBuckets cover a concurrency wait from an instant to a couple of
	// minutes. The default queue timeout is 60s, so the buckets straddle it and
	// an operator can see how close the fleet runs to the ceiling.
	queueBuckets = []float64{0.001, 0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120}
)

// New builds a Metrics with a private registry. Registration into a registry
// that was just created cannot collide, so the Must* panics are unreachable
// by construction rather than by hope.
func New() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		reg: reg,
		RequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "router_requests_total",
			Help: "Completed gateway requests by provider, model, endpoint and outcome.",
		}, []string{"provider", "model", "endpoint", "status"}),
		RequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "router_request_duration_seconds",
			Help:    "End-to-end gateway request duration by provider and model.",
			Buckets: latencyBuckets,
		}, []string{"provider", "model"}),
		TimeToFirstToken: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "router_time_to_first_token_seconds",
			Help:    "Time from request start to the first streamed chunk; only observed for streaming turns.",
			Buckets: ttftBuckets,
		}, []string{"provider", "model"}),
		TokensTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "router_tokens_total",
			Help: "Tokens billed through the gateway, split by kind.",
		}, []string{"provider", "model", "kind"}),
		CostMicros: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "router_cost_micros_total",
			Help: "Estimated spend in micro-units of the billing currency, by provider and model.",
		}, []string{"provider", "model"}),
		Fallbacks: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "router_fallbacks_total",
			Help: "Requests that moved to another upstream connection or provider.",
		}, []string{"provider", "model", "reason"}),
		UpstreamErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "router_upstream_errors_total",
			Help: "Upstream responses the gateway treated as errors.",
		}, []string{"provider", "model", "status"}),
		RateLimitRejects: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "router_rate_limit_rejects_total",
			Help: "Requests rejected by a rate limiter before reaching an upstream, by limiter scope.",
		}, []string{"scope", "key"}),
		RateLimitQueue: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "router_rate_limit_queue_seconds",
			Help:    "Time a request spent waiting for a free per-key concurrency slot.",
			Buckets: queueBuckets,
		}, []string{"scope"}),
		GuardrailDecisions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "router_guardrail_decisions_total",
			Help: "Guardrail decisions by detector, action and direction.",
		}, []string{"detector", "action", "direction"}),
		GuardrailEval: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "router_guardrail_eval_seconds",
			Help:    "Time spent scanning content against the guardrail detectors.",
			Buckets: []float64{0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.1, 1},
		}, []string{"direction"}),
	}
	reg.MustRegister(
		m.RequestsTotal, m.RequestDuration, m.TimeToFirstToken,
		m.TokensTotal, m.CostMicros, m.Fallbacks, m.UpstreamErrors,
		m.RateLimitRejects, m.RateLimitQueue, m.GuardrailDecisions, m.GuardrailEval,
	)
	return m
}

// Registry exposes the private registry so a caller can register its own
// collectors into it or gather from it in tests.
func (m *Metrics) Registry() *prometheus.Registry { return m.reg }

var defaultMetrics = New()

// Default returns the process-wide instance. Recording helpers are exported as
// package functions over it, so an instrumentation point does not have to
// thread a *Metrics through its call chain.
func Default() *Metrics { return defaultMetrics }

// RecordRequest counts one completed request. status is the HTTP status the
// client saw; callers that only know success/failure pass 200 or 502.
func RecordRequest(provider, model, endpoint string, status int) {
	Default().RecordRequest(provider, model, endpoint, status)
}

func (m *Metrics) RecordRequest(provider, model, endpoint string, status int) {
	m.RequestsTotal.WithLabelValues(
		Label(provider), Label(model), Label(endpoint), StatusLabel(status),
	).Inc()
}

// RecordDuration observes the end-to-end latency of one request.
func RecordDuration(provider, model string, d time.Duration) {
	Default().RecordDuration(provider, model, d)
}

func (m *Metrics) RecordDuration(provider, model string, d time.Duration) {
	m.RequestDuration.WithLabelValues(Label(provider), Label(model)).Observe(d.Seconds())
}

// RecordTTFT observes time-to-first-token. A non-positive value means the turn
// did not stream (or never produced a first chunk) and is dropped rather than
// recorded as a suspiciously instant response.
func RecordTTFT(provider, model string, millis int64) {
	Default().RecordTTFT(provider, model, millis)
}

func (m *Metrics) RecordTTFT(provider, model string, millis int64) {
	if millis <= 0 {
		return
	}
	m.TimeToFirstToken.WithLabelValues(Label(provider), Label(model)).
		Observe(float64(millis) / 1000)
}

// AddTokens adds n tokens of the given kind. Non-positive counts are dropped.
func AddTokens(provider, model, kind string, n int) {
	Default().AddTokens(provider, model, kind, n)
}

func (m *Metrics) AddTokens(provider, model, kind string, n int) {
	if n <= 0 {
		return
	}
	m.TokensTotal.WithLabelValues(Label(provider), Label(model), Label(kind)).Add(float64(n))
}

// AddCostMicros adds estimated spend. Float accumulation is deliberate: cost
// estimates carry fractions far below one currency unit.
func AddCostMicros(provider, model string, micros float64) {
	Default().AddCostMicros(provider, model, micros)
}

func (m *Metrics) AddCostMicros(provider, model string, micros float64) {
	if micros <= 0 {
		return
	}
	m.CostMicros.WithLabelValues(Label(provider), Label(model)).Add(micros)
}

// IncFallback counts one fallback move. reason is one of the FallbackReason
// constants.
func IncFallback(provider, model, reason string) {
	Default().IncFallback(provider, model, reason)
}

func (m *Metrics) IncFallback(provider, model, reason string) {
	m.Fallbacks.WithLabelValues(Label(provider), Label(model), Label(reason)).Inc()
}

// IncUpstreamError counts one upstream error, tagged with the upstream status
// code (0 when the failure happened before a response arrived).
func IncUpstreamError(provider, model string, status int) {
	Default().IncUpstreamError(provider, model, status)
}

func (m *Metrics) IncUpstreamError(provider, model string, status int) {
	m.UpstreamErrors.WithLabelValues(Label(provider), Label(model), StatusLabel(status)).Inc()
}

// IncRateLimitReject counts one request a rate limiter refused. scope names the
// limiter ("api_key", "connection"); key is a KeyRef, never the key itself.
func IncRateLimitReject(scope, keyID string) {
	Default().IncRateLimitReject(scope, keyID)
}

func (m *Metrics) IncRateLimitReject(scope, keyID string) {
	m.RateLimitRejects.WithLabelValues(Label(scope), KeyRef(keyID)).Inc()
}

// ObserveRateLimitQueueWait records how long a request waited for a free slot in
// the per-key limiter. It is the package-level convenience over the default
// registry; the per-registry form is below.
func ObserveRateLimitQueueWait(seconds float64) {
	Default().ObserveRateLimitQueueWait(seconds)
}

// ObserveRateLimitQueueWait records how long a request waited for a free slot in
// the per-key limiter, on this registry.
func (m *Metrics) ObserveRateLimitQueueWait(seconds float64) {
	m.ObserveRateLimitQueueWaitFor("api_key", seconds)
}

// ObserveRateLimitQueueWaitFor records a wait in the named limiter scope
// ("api_key" for a caller's own limit, "provider" for a custom provider's
// capacity). The scope is a parameter rather than a constant because the two
// waits mean different things to whoever is reading the graph: a key waiting on
// its own budget is the caller's problem, a provider waiting on its endpoint is
// the operator's.
func ObserveRateLimitQueueWaitFor(scope string, seconds float64) {
	Default().ObserveRateLimitQueueWaitFor(scope, seconds)
}

// ObserveRateLimitQueueWaitFor is the per-registry implementation.
func (m *Metrics) ObserveRateLimitQueueWaitFor(scope string, seconds float64) {
	m.RateLimitQueue.WithLabelValues(Label(scope)).Observe(seconds)
}

// RecordGuardrailDecision counts one guardrail decision and observes how long
// the scan took.
//
// The detector, action, and direction labels are all closed vocabularies the
// guardrail package defines, and the buckets span a regex scan (microseconds)
// to a pathological one. Keeping them bounded is the whole point: an operator
// watches these on a VPS, and a cardinality explosion there costs memory the
// gateway needs.
func RecordGuardrailDecision(detector, action, direction string, elapsed time.Duration) {
	Default().RecordGuardrailDecision(detector, action, direction, elapsed)
}

func (m *Metrics) RecordGuardrailDecision(detector, action, direction string, elapsed time.Duration) {
	m.GuardrailDecisions.WithLabelValues(Label(detector), Label(action), Label(direction)).Inc()
	m.GuardrailEval.WithLabelValues(Label(direction)).Observe(elapsed.Seconds())
}

// RecordUsage is the single call the request-completion path makes: one
// request, its latency, its tokens, its cost and — for a streaming turn —
// its time to first token.
func RecordUsage(provider, model, endpoint string, status int, latency time.Duration,
	promptTokens, completionTokens int, costMicros float64, ttftMillis int64) {
	Default().RecordUsage(provider, model, endpoint, status, latency,
		promptTokens, completionTokens, costMicros, ttftMillis)
}

func (m *Metrics) RecordUsage(provider, model, endpoint string, status int, latency time.Duration,
	promptTokens, completionTokens int, costMicros float64, ttftMillis int64) {
	m.RecordRequest(provider, model, endpoint, status)
	m.RecordDuration(provider, model, latency)
	m.RecordTTFT(provider, model, ttftMillis)
	m.AddTokens(provider, model, TokenKindPrompt, promptTokens)
	m.AddTokens(provider, model, TokenKindCompletion, completionTokens)
	m.AddCostMicros(provider, model, costMicros)
}

// StatusLabel renders an HTTP status code as a label value.
func StatusLabel(status int) string {
	if status <= 0 {
		return unknownLabel
	}
	return Label(strconv.Itoa(status))
}
