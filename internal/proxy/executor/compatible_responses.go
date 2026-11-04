package executor

import (
	"context"
	"fmt"
	"net/http"

	"9router/proxy/internal/proxy"
)

// ForwardCompatibleResponses serves a custom openai-compatible node whose
// stored apiType is "responses".
//
// Such a node already has its lane moved to /responses by compatibleNodeLane
// (internal/handlers/chat/connections.go), but CompatibleNodeWires leaves its
// Format as the OpenAI default on purpose — "the lane moves, the format stays
// OpenAI either way". Nothing then translated the body, so a Chat Completions
// payload (messages[] plus nested tools {type:"function",function:{name}}) was
// posted to an endpoint that only accepts a Responses payload (input[] plus
// FLAT tools {type:"function",name}). Upstreams reject that with
// "function tool name is required", and a tool-less request with
// "no system prompt or conversation content" — two symptoms of the same drift,
// and the same class of lane/format bug as PR #797.
//
// Both directions already existed; only the dispatch was missing:
//   - buildResponsesBody converts the request (messages→input, nested→flat);
//   - handleCodexStream reads the Responses reply and emits Chat chunks, or
//     relays it untouched when the client itself speaks Responses.
//
// This is the same three-step shape as ForwardGrokCLI and ForwardCodex, minus
// their provider-specific headers and model rewrites: a custom node carries its
// own base URL and credential, so the generic OpenAI forwarder is correct here.
func ForwardCompatibleResponses(w http.ResponseWriter, req *Request) error {
	transformedBody, _, err := buildResponsesBody(req.Body)
	if err != nil {
		return fmt.Errorf("ForwardCompatibleResponses transform body: %w", err)
	}

	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	// Always dial the event stream, even for a non-streaming client. The
	// Responses lane is consumed frame by frame (handleCodexStream
	// re-aggregates for a non-streaming caller), and buildResponsesBody pins
	// stream:true in the body it produces, so asking for a document here would
	// contradict the payload we just sent.
	resp, err := proxy.ForwardOpenAI(ctx, req.Client, req.Config, req.APIKey, transformedBody, true)
	if err != nil {
		return fmt.Errorf("ForwardCompatibleResponses: %w", err)
	}
	defer resp.Body.Close()

	// A non-200 is the provider's verdict and must surface as an upstreamError
	// so the caller can fail over to the next account rather than streaming an
	// error page to the client as if it were content.
	if err := upstreamStatusError(resp); err != nil {
		return err
	}

	return handleCodexStream(w, req, resp.Body)
}
