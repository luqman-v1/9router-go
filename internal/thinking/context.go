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

type providerThinkingKey struct{}

// WithProviderThinkingModes carries the operator's providerThinking overrides
// for the request, keyed by provider.
//
// The overrides are read once per request and travel on the context, so a
// request that tries several connections does not re-read the settings row
// once per attempt. An empty map is still a snapshot: it says the settings
// were read and carry no override.
func WithProviderThinkingModes(ctx context.Context, modes map[string]string) context.Context {
	if ctx == nil {
		return ctx
	}
	if modes == nil {
		modes = map[string]string{}
	}
	return context.WithValue(ctx, providerThinkingKey{}, modes)
}

// ProviderThinkingMode returns the operator override for a provider. The bool
// reports whether the request carries a snapshot: false means the caller has
// to read the settings itself.
func ProviderThinkingMode(ctx context.Context, provider string) (string, bool) {
	if ctx == nil {
		return "", false
	}
	modes, ok := ctx.Value(providerThinkingKey{}).(map[string]string)
	if !ok {
		return "", false
	}
	return modes[provider], true
}
