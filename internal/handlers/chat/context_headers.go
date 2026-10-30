package chat

import (
	"context"
	"net/http"
	"strconv"

	"9router/proxy/internal/middleware"
	"9router/proxy/internal/providers"
)

// Context-budget response headers.
//
// A client that manages its own conversation needs two numbers only the
// gateway knows: how big the window of the model that served the request is,
// and how much of it the request used. The headers carry both, so a client can
// size its next turn without asking the gateway again.
//
// The window is the effective one — the model's own window, lowered to the
// calling key's context ceiling when the key sets one. That is the budget the
// request actually had to fit in, and the gateway enforces it by trimming.

// contextBudget is the token budget of one request.
type contextBudget struct {
	// window is the total context the served model offers, in tokens.
	window int
	// maxOutput is the output allowance of the served model, in tokens.
	maxOutput int
	// input is the input the request carries, in tokens.
	input int
}

// maxInput is the part of the window that may carry input: the window minus the
// output the model has to be allowed to produce.
func (b contextBudget) maxInput() int {
	if b.maxOutput <= 0 || b.maxOutput >= b.window {
		return b.window
	}
	return b.window - b.maxOutput
}

// remainingInput is the room left in the input budget.
func (b contextBudget) remainingInput() int {
	if room := b.maxInput() - b.input; room > 0 {
		return room
	}
	return 0
}

// resolveContextBudget reads the window of the model that will serve the
// request and the input the request carries.
//
// The window is the same one the ceiling enforces, so the numbers the client
// reads back are the numbers the request was actually held to.
func resolveContextBudget(ctx context.Context, provider, model string, body []byte) contextBudget {
	caps := providers.GetCapabilitiesDetailForModel(provider, model)
	limit := 0
	if key := middleware.AuthenticatedApiKeyFromContext(ctx); key != nil && key.ContextLimit != nil {
		limit = *key.ContextLimit
	}
	window, _, maxOutput := contextAllowance(limit, caps.ContextWindow, caps.MaxOutput, 0)
	return contextBudget{
		window:    window,
		maxOutput: maxOutput,
		input:     middleware.EstimatePromptTokens(body),
	}
}

// injectContextHeaders writes the context budget of the request being served.
//
// The output allowance is reported whole: the headers are written before the
// upstream answers, so no output token has been produced yet.
func injectContextHeaders(w http.ResponseWriter, budget contextBudget) {
	if budget.window <= 0 {
		return
	}
	h := w.Header()
	h.Set("X-9router-Context-Window", strconv.Itoa(budget.window))
	h.Set("X-9router-Max-Input-Tokens", strconv.Itoa(budget.maxInput()))
	h.Set("X-9router-Input-Tokens", strconv.Itoa(budget.input))
	h.Set("X-9router-Remaining-Input-Tokens", strconv.Itoa(budget.remainingInput()))
	if budget.maxOutput > 0 {
		h.Set("X-9router-Max-Output-Tokens", strconv.Itoa(budget.maxOutput))
		h.Set("X-9router-Remaining-Output-Tokens", strconv.Itoa(budget.maxOutput))
	}
}

// clearContextHeaders removes the context-budget headers, so a failed attempt's
// numbers do not leak into an error response.
func clearContextHeaders(w http.ResponseWriter) {
	if w == nil {
		return
	}
	h := w.Header()
	h.Del("X-9router-Context-Window")
	h.Del("X-9router-Max-Input-Tokens")
	h.Del("X-9router-Max-Output-Tokens")
	h.Del("X-9router-Input-Tokens")
	h.Del("X-9router-Remaining-Input-Tokens")
	h.Del("X-9router-Remaining-Output-Tokens")
}
