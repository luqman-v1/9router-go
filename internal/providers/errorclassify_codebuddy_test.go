package providers

import (
	"testing"
)

// CodeBuddy Intl refuses a request with HTTP 400 "request illegal" or
// "conversation id missing". Those are account/IP blocks, so the connection
// must back off instead of falling into the request-scoped 4xx path
// (upstream 3b137aa6, #5024).
func TestClassifyErrorForProvider_CodebuddyIntlBlocks(t *testing.T) {
	for _, msg := range []string{"11128:request illegal", "conversation id missing"} {
		cls := ClassifyErrorForProvider(400, msg, 0, "codebuddy-intl")
		if !cls.ShouldFallback || cls.CooldownMs != BackoffConfig.BaseMs || cls.NewBackoffLevel != 1 {
			t.Errorf("block %q classified %+v, want backoff at base level 1", msg, cls)
		}
	}
}

func TestClassifyErrorForProvider_CodebuddyExponentialCeiling(t *testing.T) {
	for level, want := range map[int]int{0: BackoffConfig.BaseMs, 1: BackoffConfig.BaseMs * 2, 2: BackoffConfig.BaseMs * 4} {
		if got := ClassifyErrorForProvider(400, "request illegal", level, "codebuddy-intl").CooldownMs; got != want {
			t.Errorf("level %d cooldown = %d, want %d", level, got, want)
		}
	}
	cls := ClassifyErrorForProvider(400, "request illegal", BackoffConfig.MaxLevel, "codebuddy-intl")
	if !cls.ShouldFallback || cls.CooldownMs != BackoffConfig.MaxMs || cls.NewBackoffLevel != BackoffConfig.MaxLevel {
		t.Errorf("capped level classified %+v", cls)
	}
}

// The same texts stay request-scoped everywhere else: no cooldown, no
// fallback, and the backoff level does not move.
func TestClassifyErrorForProvider_OtherProvidersStayRequestScoped(t *testing.T) {
	for _, provider := range []string{"openai", "codebuddy-cn", ""} {
		cls := ClassifyErrorForProvider(400, "request illegal", 0, provider)
		if cls.ShouldFallback || cls.CooldownMs != 0 {
			t.Errorf("provider %q classified %+v, want request-scoped", provider, cls)
		}
	}
	// The legacy signature has no provider context, so the scoped rules
	// cannot fire there either.
	if cls := ClassifyError(400, "request illegal", 0); cls.ShouldFallback {
		t.Errorf("provider-agnostic classify fell back: %+v", cls)
	}
}

// Not every 11128 code is a block: a malformed request is the caller's bug.
func TestClassifyErrorForProvider_MalformedCodebuddyRequest(t *testing.T) {
	cls := ClassifyErrorForProvider(400, "first message is not system prompt", 0, "codebuddy-intl")
	if cls.ShouldFallback || cls.CooldownMs != 0 {
		t.Errorf("malformed request classified %+v, want request-scoped", cls)
	}
}

// The combo member id may arrive as the cbai alias; rules still match.
func TestClassifyErrorForProvider_CbaiAliasResolves(t *testing.T) {
	cls := ClassifyErrorForProvider(400, "request illegal", 0, "cbai")
	if !cls.ShouldFallback || cls.CooldownMs != BackoffConfig.BaseMs || cls.NewBackoffLevel != 1 {
		t.Errorf("cbai alias classified %+v", cls)
	}
}
