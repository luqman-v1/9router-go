package chat

import (
	"context"

	json "9router/proxy/internal/fastjson"
	"9router/proxy/internal/middleware"
)

// A per-key context ceiling.
//
// A key may call a model whose own window is far larger than the operator is
// willing to let that key use — a reseller selling a 200k contract against a
// 1M-token model, for instance. The token quota answers a different question
// (what the key may spend over its whole life), so this is a separate axis: it
// bounds ONE request.
//
// The limit is applied as a cap, never as a raise. A model with a smaller
// window keeps its own smaller ceiling, because the per-member trim that
// already runs for capacity-adapter models applies after this one and only
// ever trims further.
//
// The history is trimmed rather than the request refused: a ceiling that
// rejects a long conversation would make the key unusable exactly when it is
// being used hard. The point of a ceiling is to bound the cost of a turn, not
// to punish a long one.

// keyContextLimit returns the context ceiling the caller's key carries, in
// tokens. 0 means the key has no ceiling and the model's own window applies.
func keyContextLimit(ctx context.Context) int {
	key := middleware.AuthenticatedApiKeyFromContext(ctx)
	if key == nil || key.ContextLimit == nil {
		return 0
	}
	if *key.ContextLimit <= 0 {
		return 0
	}
	return *key.ContextLimit
}

// applyKeyContextLimit trims a request to the ceiling the caller's key
// carries.
//
// The body is returned untouched when the key has no ceiling, when the request
// asks for no output budget, or when the request already fits — so the common
// path pays nothing but a length check.
func applyKeyContextLimit(body []byte, limit int) []byte {
	if limit <= 0 || len(body) == 0 {
		return body
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}
	out, changed := applyKeyContextLimitMap(req, limit)
	if !changed {
		return body
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return body
	}
	return encoded
}

// applyKeyContextLimitMap is applyKeyContextLimit for an already-decoded body.
//
// It reports whether anything changed, so a caller that already holds the
// decoded map does not pay a re-encode when the request fits.
func applyKeyContextLimitMap(req map[string]any, limit int) (map[string]any, bool) {
	if limit <= 0 || req == nil {
		return req, false
	}
	// A request that names no output budget leaves the model's own window to
	// decide; there is nothing to cap against.
	field, maxTokens, ok := outputBudgetField(req)
	if !ok || maxTokens <= 0 {
		return req, false
	}
	// The history trim runs first: it is what makes room for the turn, and the
	// budget left over is what the output may use.
	encoded, err := json.Marshal(req)
	if err != nil {
		return req, false
	}
	trimmed := stripHistoryForContext(encoded, limit)
	trimmed = repairToolPairing(trimmed)
	var trimmedReq map[string]any
	if err := json.Unmarshal(trimmed, &trimmedReq); err != nil {
		return req, false
	}
	// Four characters per token is the estimate the rest of the runtime uses
	// (charsPerToken in capacity_adapter_history.go); a tokenizer dependency
	// for one budget check is not worth it.
	used := len(trimmed) / charsPerToken
	room := limit - used
	if room >= maxTokens {
		if len(trimmed) == len(encoded) {
			return req, false
		}
		return trimmedReq, true
	}
	if room < 1 {
		// The turn alone fills the ceiling. The trim above has already dropped
		// what it could; what is left cannot fit an answer, so the budget goes
		// to the smallest one the API accepts and the upstream decides.
		room = 1
	}
	trimmedReq[field] = room
	return trimmedReq, true
}

// outputBudgetField finds the field that declares how many tokens the answer
// may use, and how large it is.
//
// Claude Messages names it max_tokens; OpenAI has used both max_tokens and
// max_completion_tokens, and the second is the only one an o-series model
// accepts. Whichever the request carries is the one the ceiling caps, so both
// spellings are looked for.
func outputBudgetField(req map[string]any) (string, int, bool) {
	for _, name := range []string{"max_tokens", "max_completion_tokens"} {
		if v, ok := numberField(req, name); ok {
			return name, v, true
		}
	}
	return "", 0, false
}

// numberField reads a numeric field from a decoded request body. A value that
// is not a number (a string, a float) is reported as absent, which leaves the
// request alone rather than guessing at it.
func numberField(req map[string]any, name string) (int, bool) {
	v, ok := req[name]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	default:
		return 0, false
	}
}
