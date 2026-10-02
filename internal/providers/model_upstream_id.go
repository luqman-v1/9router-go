package providers

import "strings"

// The codex catalog publishes ids that are not wire ids. Two shapes exist
// upstream (open-sse/providers/registry/codex.js models entries):
//
//   - the `[1m]` extended-context variants declare
//     `upstreamModelId: "gpt-6-sol"`; the backend has never heard of the
//     bracketed id and answers 400 if it is forwarded verbatim;
//   - the synthesized `-review` variants declare the base model, except
//     `codex-auto-review`, which upstream forwards as-is because it is not
//     derived from a base model (#1398).
//
// Go's catalog is a flat id list, so the mapping is derived from the id shape
// rather than read from a per-entry field. Upstream reaches the same answer
// through getModelUpstreamId, which resolves `upstreamModelId || id` from the
// catalog and only then falls back to stripping the review suffix — for the
// ids upstream publishes, those two paths agree.

const (
	// codexExtendedContextSuffix marks the extended-context catalog variants.
	codexExtendedContextSuffix = "[1m]"
	// codexReviewSuffix is stripped from a synthesized review id.
	codexReviewSuffix = "-review"
	// CodexAutoReviewModel is the CLI's virtual auto-review model. It is not
	// derived from a base model, so its suffix is NOT stripped (#1398).
	CodexAutoReviewModel = "codex-auto-review"
)

// CodexUpstreamModelID maps a catalog model id to the id the codex backend
// expects. A trailing "(level)" thinking override is split off first and
// re-appended, so downstream code that re-applies thinking still sees it.
//
// Anything unrecognized is returned unchanged: the gateway forwards arbitrary
// model strings, and a client holding a model the catalog has not caught up
// with must still be able to reach it.
func CodexUpstreamModelID(model string) string {
	base, suffix := splitThinkingSuffix(model)

	// A vendor prefix names a different provider, so only a bare id is a codex
	// catalog id. Rewriting "cx/gpt-5.6-sol-review" would strip a suffix the
	// serving provider may legitimately use.
	if base == CodexAutoReviewModel {
		return base + suffix
	}
	if strings.Contains(base, "/") {
		return base + suffix
	}
	if trimmed, ok := strings.CutSuffix(base, codexExtendedContextSuffix); ok {
		return trimmed + suffix
	}
	if trimmed, ok := strings.CutSuffix(base, codexReviewSuffix); ok {
		return trimmed + suffix
	}
	return base + suffix
}

// splitThinkingSuffix separates the trailing "(level)" thinking override from a
// model id. It is a 9router request modifier, never part of the wire id.
func splitThinkingSuffix(model string) (base, suffix string) {
	if open := strings.LastIndex(model, "("); open != -1 && strings.HasSuffix(model, ")") {
		return strings.TrimSpace(model[:open]), model[open:]
	}
	return model, ""
}