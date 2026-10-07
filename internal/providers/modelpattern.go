package providers

// MatchModelPattern reports whether a model id matches a single-segment glob
// pattern: `*` stands for any run of characters that does not cross a `/`, the
// whole string must match, and the comparison is case-insensitive.
//
// It is the exported door onto the resolver's own matcher (matchPattern, used
// for the per-model capability patterns) so callers outside this package —
// per-API-key model allowlists, for one — cannot drift into a second glob with
// different semantics. path.Match backs both, which is why `*` is
// segment-local: `openai/*` covers `openai/gpt-4o` but not `openai/x/gpt-4o`.
func MatchModelPattern(pattern, modelID string) bool {
	return matchPattern(pattern, modelID)
}