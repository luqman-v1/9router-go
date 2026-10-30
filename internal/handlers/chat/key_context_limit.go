package chat

import (
	"context"
	"fmt"

	json "9router/proxy/internal/fastjson"
	"9router/proxy/internal/middleware"
	"9router/proxy/internal/providers"
)

// A per-key context ceiling.
//
// A key may call a model whose own window is far larger than the operator is
// willing to let that key use — a reseller selling a 200k contract against a
// 1M-token model, for instance. The token quota answers a different question
// (what the key may spend over its whole life), so this is a separate axis: it
// bounds ONE request.
//
// The ceiling is a total: the input the request carries plus the answer it may
// produce. The output allowance is reserved first — the model needs room to
// answer — and the input gets what is left.
//
// The history is trimmed rather than the request refused: a ceiling that
// rejects a long conversation would make the key unusable exactly when it is
// being used hard. A request that still does not fit after the trim IS
// refused, because sending it anyway breaks the operator's promise silently.

// ContextLimitError says a request does not fit the key's ceiling, even after
// the history was trimmed as far as it can go.
type ContextLimitError struct {
	// Window is the ceiling in tokens.
	Window int
	// Used is the request's own size in tokens.
	Used int
	// Reason names what did not fit, for the message.
	Reason string
}

func (e *ContextLimitError) Error() string {
	if e.Reason != "" {
		return e.Reason
	}
	return fmt.Sprintf("request context is %d tokens, above this API key's %d-token context limit", e.Used, e.Window)
}

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

// contextAllowance splits a window into the input it may carry and the output
// it may produce.
//
// The window is the smaller of what the key may use and what the model offers:
// a ceiling below the model's own window is the binding one.
//
// The output the request asked for is reserved out of it first — the model needs
// room to answer, and the input gets what is left. A reserve that would leave
// no room for input at all is given back: the output is clamped to the room
// the input leaves instead, because the ceiling is the operator's rule and the
// output budget is the client's wish.
func contextAllowance(limit, modelWindow, modelMaxOutput, declaredOutput int) (window, maxInput, reservedOutput int) {
	window = modelWindow
	if limit > 0 && (window == 0 || limit < window) {
		window = limit
	}
	reservedOutput = modelMaxOutput
	if declaredOutput > 0 && (reservedOutput == 0 || declaredOutput < reservedOutput) {
		reservedOutput = declaredOutput
	}
	if reservedOutput > window {
		reservedOutput = window
	}
	maxInput = window - reservedOutput
	if maxInput <= 0 {
		reservedOutput = 0
		maxInput = window
	}
	return window, maxInput, reservedOutput
}

// applyKeyContextLimit trims a request to the ceiling the caller's key
// carries, and returns the request unchanged when there is no ceiling.
//
// A request that cannot be made to fit is refused with a ContextLimitError.
func applyKeyContextLimit(body []byte, limit, modelWindow, modelMaxOutput int) ([]byte, error) {
	if limit <= 0 || len(body) == 0 {
		return body, nil
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body, nil
	}
	out, err := applyKeyContextLimitMap(req, limit, modelWindow, modelMaxOutput)
	if err != nil {
		return body, err
	}
	if out == nil {
		return body, nil
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return body, nil
	}
	return encoded, nil
}

// applyKeyContextLimitMap is applyKeyContextLimit for an already-decoded body.
//
// It returns nil when the body needs no change, so a caller that already holds
// the decoded map does not pay a re-encode.
func applyKeyContextLimitMap(req map[string]any, limit, modelWindow, modelMaxOutput int) (map[string]any, error) {
	if limit <= 0 || req == nil {
		return nil, nil
	}

	_, declaredOutput, _ := outputBudgetField(req)
	window, maxInput, _ := contextAllowance(limit, modelWindow, modelMaxOutput, declaredOutput)

	used := middleware.EstimatePromptTokensFromMap(req)
	trimmed := false
	if used > maxInput {
		encoded, err := json.Marshal(req)
		if err != nil {
			return nil, nil
		}
		cut := trimToContextBudget(encoded, maxInput)
		cut = repairToolPairing(cut)
		var cutReq map[string]any
		if err := json.Unmarshal(cut, &cutReq); err != nil {
			return nil, nil
		}
		used = middleware.EstimatePromptTokensFromMap(cutReq)
		if used > maxInput {
			return nil, &ContextLimitError{Window: window, Used: used}
		}
		req = cutReq
		trimmed = true
	}

	// The answer may not push the request past the ceiling either. Every
	// spelling the client used is clamped, so whichever one the normalizer
	// keeps on the wire is the clamped one.
	room := window - used
	if room < 1 {
		room = 1
	}
	for _, field := range outputBudgetFields(req) {
		if v, ok := numberField(req, field); ok && v > room {
			req[field] = room
			trimmed = true
		}
	}
	if !trimmed {
		return nil, nil
	}
	return req, nil
}

// trimToContextBudget drops as much of the conversation's middle as it takes
// to fit the budget, and gives up when dropping more would not help.
//
// The trim is the one the capacity adapter already uses for a member with a
// smaller window, so a key ceiling and a small model trim a conversation the
// same way. It cannot shrink the turn being answered or the system prompt, so
// a request whose own turn is over the budget never fits.
func trimToContextBudget(body []byte, budgetTokens int) []byte {
	trimmed := body
	used := middleware.EstimatePromptTokensFromMap(mustDecode(body))
	for window := budgetTokens; window > 0 && used > budgetTokens; window /= 2 {
		candidate := stripHistoryForContext(trimmed, window)
		candidateUsed := middleware.EstimatePromptTokensFromMap(mustDecode(candidate))
		if candidateUsed >= used {
			// Nothing more can be dropped; a narrower window would only drop
			// turns that are already gone.
			break
		}
		trimmed, used = candidate, candidateUsed
	}
	return trimmed
}

// mustDecode decodes a request body, returning an empty map when it does not
// parse. Its only use is measuring, and an unparseable body measures as zero.
func mustDecode(body []byte) map[string]any {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return nil
	}
	return req
}

// outputBudgetFields names the fields that declare how many tokens the answer
// may use, across the dialects the gateway speaks.
//
// Claude Messages and OpenAI Chat use max_tokens; OpenAI also has
// max_completion_tokens, which is the only spelling an o-series model
// accepts; the Responses dialect uses max_output_tokens.
func outputBudgetFields(req map[string]any) []string {
	return []string{"max_tokens", "max_completion_tokens", "max_output_tokens"}
}

// outputBudgetField finds the field that declares how many tokens the answer
// may use, and how large it is.
func outputBudgetField(req map[string]any) (string, int, bool) {
	for _, name := range outputBudgetFields(req) {
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

// modelContextLimits reads the context window and the output allowance of a
// model, the two numbers a ceiling is split against.
//
// The values are the ones the model catalogue publishes, so the ceiling and
// the window the client was told about are the same numbers.
func modelContextLimits(provider, model string) (window, maxOutput int) {
	caps := providers.GetCapabilitiesDetailForModel(provider, model)
	return caps.ContextWindow, caps.MaxOutput
}
