package semanticcache

import (
	"context"

	"9router/proxy/internal/translator"
)

type cachedReqKey struct{}

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
