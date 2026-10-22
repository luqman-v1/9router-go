package thinking

import "context"

type suffixIntentKey struct{}

// WithSuffixIntent carries the reasoning intent a "(level)" model suffix
// expressed, from the point the model name is read to the point the request
// is dispatched.
//
// The suffix is a request override, not part of the model id, so it comes off
// the name before the name is resolved against the aliases and the combos —
// otherwise "mycombo(high)" resolves to neither. The intent it carries then
// travels beside the request, because the combo path replaces the body's
// model field with the member it is about to call.
func WithSuffixIntent(ctx context.Context, intent *Intent) context.Context {
	if ctx == nil || intent == nil {
		return ctx
	}
	return context.WithValue(ctx, suffixIntentKey{}, intent)
}

// SuffixIntentFromContext returns the intent a "(level)" model suffix
// expressed, or nil when the model name carried none.
func SuffixIntentFromContext(ctx context.Context) *Intent {
	if ctx == nil {
		return nil
	}
	intent, _ := ctx.Value(suffixIntentKey{}).(*Intent)
	return intent
}
