package guardrails

import (
	"context"
	"errors"
	"net/http"
)

// policyCtxKey carries the resolved response policy on the request context.
//
// The executors are shared by every provider and every API key, so a policy
// cannot live in their signatures — it would have to be threaded through every
// call site, and each of them would be a place to forget. The context is
// already the one thing a request carries into all of them, so the resolved
// policy travels on it and each response path reads it where it writes.
type policyCtxKey struct{}

// Policy is the response policy governing one request, together with the audit
// sink that records what it decided.
type Policy struct {
	Engine *Engine
	Audit  Audit
	Target Target
}

// disabledCtxKey carries the global kill-switch.
type disabledCtxKey struct{}

// WithSwitch records the global kill-switch on the context so every tap
// downstream — including the executors, which never see the router — obeys
// the same switch.
func WithSwitch(ctx context.Context, sw Switch) context.Context {
	if ctx == nil || sw == nil {
		return ctx
	}
	return context.WithValue(ctx, disabledCtxKey{}, sw)
}

// SwitchFrom returns the recorded kill-switch, or nil (meaning on).
func SwitchFrom(ctx context.Context) Switch {
	if ctx == nil {
		return nil
	}
	sw, _ := ctx.Value(disabledCtxKey{}).(Switch)
	return sw
}

// WithPolicy records the policy that governs this response.
//
// A policy that enables nothing is not recorded at all, so a nil policy is what
// every tap checks for and an install that never configured guardrails pays one
// resolution and nothing after it.
func WithPolicy(ctx context.Context, p *Policy) context.Context {
	if ctx == nil || p == nil || !p.Engine.Enabled() {
		return ctx
	}
	return context.WithValue(ctx, policyCtxKey{}, p)
}

// PolicyFrom returns the policy governing this response, or nil.
func PolicyFrom(ctx context.Context) *Policy {
	if ctx == nil {
		return nil
	}
	p, _ := ctx.Value(policyCtxKey{}).(*Policy)
	return p
}

// ErrBlocked is what a policy refusal reports to the response path. It carries
// no matched value: a message a client can read must not confirm which
// patterns an install runs.
var ErrBlocked = errors.New(BlockedMessage)

// AuditDecision records one decision against the request's policy. A policy
// without a sink still enforces; it just leaves no trail.
func (p *Policy) AuditDecision(d *Decision) {
	if p == nil || p.Audit == nil || d == nil {
		return
	}
	p.Audit(*d, p.Target)
}

// ApplyBuffered applies the request's policy to a whole buffered response body.
//
// The buffered half can be judged completely before a single byte is written,
// so a block here is still able to become a real status code. It returns the
// body to relay, unchanged when no policy applies.
func ApplyBuffered(ctx context.Context, body []byte) ([]byte, error) {
	if !SwitchFrom(ctx).Enabled() {
		return body, nil
	}
	p := PolicyFrom(ctx)
	if p == nil || !p.Engine.Enabled() || len(body) == 0 {
		return body, nil
	}
	filtered, decision := ScanResponseBody(p.Engine, body)
	if !decision.WasMutated() && !decision.Blocked() {
		return filtered, nil
	}
	p.AuditDecision(decision)
	if decision.Blocked() {
		return nil, ErrBlocked
	}
	return filtered, nil
}

// ApplyOutbound is the writer-level tap for a streamed response.
//
// The tap has to sit at the writer: a non-streamed body is buffered whole and
// could be judged before anything is written, but a stream has already put its
// first bytes on the wire, and a PII value split across two frames cannot be
// un-sent. So the stream is filtered frame by frame, with a window of context
// held back so a value straddling a frame boundary is still caught.
func ApplyOutbound(ctx context.Context, w http.ResponseWriter, format StreamFormat) *Outbound {
	if !SwitchFrom(ctx).Enabled() {
		return nil
	}
	p := PolicyFrom(ctx)
	if p == nil || !p.Engine.Enabled() {
		return nil
	}
	return NewOutboundFor(w, p.Engine, p.Audit, format)
}
