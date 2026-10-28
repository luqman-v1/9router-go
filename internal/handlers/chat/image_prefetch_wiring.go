package chat

import (
	"context"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/translator"
)

// inlineImageTargets are the providers whose wire format cannot fetch a remote
// URL itself, so an inbound `http(s)` image reference would reach the model as
// a dead string. Ported from open-sse/translator/concerns/prefetch.js
// (TARGETS_NEED_BASE64); anything not listed here keeps remote URLs as-is.
//
// Upstream keys that set by wire *format*; this table keys it by provider id,
// and the difference is deliberate rather than an oversight. ProviderConfig
// .Format in this repository carries only the two Gemini variants and "claude"
// — it has no "kiro" or "commandcode" value to compare against — so the id is
// where those providers' wire format is actually recorded. A provider that
// routes a single model onto a different format is covered separately, by the
// model's own declared TargetFormat in wantsInlineImages.
var inlineImageTargets = map[string]bool{
	"gemini":      true,
	"gemini-cli":  true,
	"vertex":      true,
	"antigravity": true,
	"ollama":      true,
	"kiro":        true,
	"commandcode": true,
}

// imagePrefetchOpts is the policy for every dispatch. A package var so a
// dispatch test can point it at a local image server; production leaves the
// zero value, which means the https-only, SSRF-guarded defaults.
var imagePrefetchOpts = translator.PrefetchOptions{}

// wantsInlineImages reports whether this dispatch target needs the prefetch.
//
// The question is about the format the request is translated INTO, not the
// provider's name: the Gemini translator turns a remote URL into
// fileData.fileUri, which Gemini cannot fetch, so a model that rides that lane
// needs the inline rewrite whichever provider serves it. opencode-go's
// gemini-4-argon-contributor is exactly that case — an id absent from the table
// above, translated by TranslateOpenAIToGemini.
func wantsInlineImages(provider, model string, cfg *providers.ProviderConfig) bool {
	if inlineImageTargets[provider] {
		return true
	}
	if cfg != nil && cfg.IsGeminiNative() {
		return true
	}
	formats, declared := providers.GetModelFormats(provider, model)
	return declared && formats.TargetFormat == providers.FormatGemini
}

// prefetchImagesForTarget rewrites remote image URLs into inline base64 when
// the target cannot fetch them, and returns the body untouched otherwise.
//
// G11's discovery doc places this before translation, which is here: the body
// at this point is still in the client's own format and the provider that will
// receive it is known. Fail-open — every refusal inside returns the body.
func prefetchImagesForTarget(ctx context.Context, provider, model string, cfg *providers.ProviderConfig, body []byte) []byte {
	if !wantsInlineImages(provider, model, cfg) {
		return body
	}
	return translator.PrefetchInlineImages(ctx, body, imagePrefetchOpts)
}
