package handlerutil

import "context"

type serviceKindContextKey struct{}

var serviceKindKeyVal = serviceKindContextKey{}

// WithServiceKind returns a copy of ctx annotated with the service kind (e.g. "image", "video", "tts", "stt", "embedding", "webSearch", "webFetch").
func WithServiceKind(ctx context.Context, kind string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, serviceKindKeyVal, kind)
}

// GetServiceKind returns the service kind stored in ctx, or empty string if unset.
func GetServiceKind(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(serviceKindKeyVal).(string)
	return v
}
