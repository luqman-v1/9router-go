package executor

import (
	"9router/proxy/internal/providers"
)

// Output-budget floor for reasoning models.
//
// Reasoning models spend the same max_tokens ceiling on chain-of-thought as on
// the visible answer. A client asking for a small budget — a health probe, a
// title generator, a one-word reply — can have the entire budget consumed while
// the model is still thinking, and the upstream answers 200 with an empty
// message and finish_reason "length". No error, no content.
//
// Measured on the OpenCode free tier (azrinaldi/opencode-free-bridge v1.4.0
// table, itself derived from OpenRouter's documented shared-budget behaviour and
// the pi coding agent's formula): max_tokens 50/100 returned empty content for
// Muse Spark, 100 for cat-jam, 256 for big-pickle, while 512 and above returned
// content. Our own muse-spark lane carries the same measurement (570-1320 hidden
// reasoning tokens before the first completion token).
const (
	// outputMinAnswerTokens is the visible answer a reasoning model must still
	// be able to produce after its thinking.
	outputMinAnswerTokens = 1024

	// outputThinkingHeadroom is the chain-of-thought a reasoning model spends
	// before it emits any text.
	outputThinkingHeadroom = 4096

	// outputReasoningFloor is the smallest budget a reasoning model can be given
	// and still answer: room to think plus room to reply.
	outputReasoningFloor = outputMinAnswerTokens + outputThinkingHeadroom

	// outputMinPlainTokens is the floor for a model that does not reason: it
	// only needs room for the answer itself.
	outputMinPlainTokens = 512

	// outputBudgetDefaultCeiling bounds the raise when the capability table
	// declares no MaxOutput for the model. It matches the ecosystem default
	// (131072) so a raise can never invent a budget no model honours.
	outputBudgetDefaultCeiling = 131072
)

// clampOutputBudget raises a caller-set output budget to the floor its model
// needs, and returns the fields it changed.
//
// reasoning reports whether the model spends its budget on chain-of-thought;
// ceiling is the model's declared MaxOutput (0 = unknown).
//
// It is a floor, not an addition. Upstream's formula is
// "min(max(requested, 1024) + 4096, modelMaxOutput)", which adds headroom to
// every reasoning request — including one that already asked for 128k, where the
// extra 4k neither helps nor is honoured by most models. Raising only what is
// below the floor fixes the empty-answer failure (where both forms agree) while
// leaving a caller's deliberate large budget exactly as they wrote it. The
// caller-supplied value is never lowered, and a request that set no budget at
// all keeps none: synthesizing a cap the caller never asked for would silently
// shorten real answers.
func clampOutputBudget(m map[string]any, reasoning bool, ceiling int) []string {
	if m == nil {
		return nil
	}
	floor := outputMinPlainTokens
	if reasoning {
		floor = outputReasoningFloor
	}
	if ceiling <= 0 {
		ceiling = outputBudgetDefaultCeiling
	}
	// A model whose declared ceiling is under the floor cannot honour it, so the
	// ceiling wins: asking for more output than the model has would turn a
	// recovered request into a 400.
	if floor > ceiling {
		floor = ceiling
	}
	var raised []string
	for _, field := range museSparkOutputBudgetFields {
		budget, ok := outputBudgetOf(m[field])
		if !ok || budget >= int64(floor) {
			continue
		}
		m[field] = floor
		raised = append(raised, field)
	}
	return raised
}

// clampOpenCodeOutputBudget applies the floor to a body on one of the OpenCode
// lanes. It reports the fields raised, so a caller can log which budget it had
// to widen.
//
// The reasoning flag and the ceiling both come from the capability table rather
// than a hardcoded model list: every free OpenCode id reasons today, but keying
// on the table means a future non-reasoning model is not silently given the
// larger floor.
func clampOpenCodeOutputBudget(m map[string]any, provider, cleanModel string) []string {
	caps := providers.GetCapabilitiesForModel(provider, cleanModel)
	return clampOutputBudget(m, caps.Reasoning, caps.MaxOutput)
}

// includeStreamUsage asks the upstream to report token counts on a streaming
// request.
//
// A streamed Chat Completions response carries no usage unless the caller sets
// stream_options.include_usage, so the free lanes — which always stream — sent
// nothing, and a non-streaming client got a re-aggregated body with no usage
// field at all. The dashboard then billed the turn at zero and TPM settlement
// reconciled against nothing. Upstream answers the flag with a final chunk
// carrying usage and an empty choices array; sseToOpenAIJSON reads that chunk
// before its choices guard.
//
// A caller that already spoke for itself keeps its own setting: overriding
// stream_options could add a key its upstream validates strictly.
func includeStreamUsage(m map[string]any) {
	if m == nil {
		return
	}
	if existing, ok := m["stream_options"]; ok && existing != nil {
		return
	}
	m["stream_options"] = map[string]any{"include_usage": true}
}
