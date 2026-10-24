package translator

import "context"

// geminiSafetyKey carries a request-scoped flag through the forward chain so
// ForwardGemini can add safetySettings to the native request. The handler
// decides; the translator/proxy only read it.
type geminiSafetyKey struct{}

func WithGeminiSafety(ctx context.Context, on bool) context.Context {
	return context.WithValue(ctx, geminiSafetyKey{}, on)
}

func GeminiSafetyRequested(ctx context.Context) bool {
	on, _ := ctx.Value(geminiSafetyKey{}).(bool)
	return on
}
