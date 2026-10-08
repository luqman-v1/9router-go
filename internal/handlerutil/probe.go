package handlerutil

import "context"

type probeContextKey struct{}

var probeContextKeyVal = probeContextKey{}

// WithProbeContext marks a context as belonging to a model test probe.
func WithProbeContext(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, probeContextKeyVal, true)
}

// IsProbeContext reports whether the context belongs to a model test probe.
func IsProbeContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(probeContextKeyVal).(bool)
	return v
}
