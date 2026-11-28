package semanticcache

import (
	"context"

	"9router/proxy/internal/translator"
)

type cachedReqKey struct{}

type steerKey struct{}

// WithSteer attaches the request-level provider-routing steer (the
// canonical provider-preferences bytes) to context. It rides the context
// rather than the OpenAIRequest because the steer is a 9router-go client
// hint, not a wire field: carrying it on the request struct would leak it
// into translation and accounting. Every cache key must include it — two
// requests that differ only in the steer can be served by different
// providers, so one key would answer a client from the provider it
// excluded.
func WithSteer(ctx context.Context, steer []byte) context.Context {
	if len(steer) == 0 {
		return ctx
	}
	return context.WithValue(ctx, steerKey{}, steer)
}

// steerFromContext returns the steer set by WithSteer, or nil.
func steerFromContext(ctx context.Context) []byte {
	if steer, ok := ctx.Value(steerKey{}).([]byte); ok {
		return steer
	}
	return nil
}

// WithCachedRequest attaches an OpenAIRequest to context for later cache storage.
func WithCachedRequest(ctx context.Context, req *translator.OpenAIRequest) context.Context {
	return context.WithValue(ctx, cachedReqKey{}, req)
}

// CachedRequestFromContext retrieves the cached OpenAIRequest from context.
func CachedRequestFromContext(ctx context.Context) *translator.OpenAIRequest {
	if req, ok := ctx.Value(cachedReqKey{}).(*translator.OpenAIRequest); ok {
		return req
	}
	return nil
}
