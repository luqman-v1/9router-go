package providers

// NodeWire is the wire contract of one custom provider node type: the lane a
// node's baseUrl prefix completes to, and the format its upstream speaks.
type NodeWire struct {
	// Lane is the path appended to a node's baseUrl prefix ("" = the handler
	// derives its own route from this lane, e.g. embeddings).
	Lane string
	// Format is the providers format constant the upstream answers in
	// ("" = OpenAI Chat Completions). Stamped onto the resolved config by
	// getProviderConfig so the request/response translators know the wire.
	Format string
}

// CompatibleNodeWires is the single source of truth for what every custom
// node type speaks — the same contract upstream's executors/base.js buildUrl
// and services/provider.js getTargetFormat carry (isAnthropicCompatible →
// "claude"). Lane and Format are declared together on purpose: they describe
// one wire and must not drift apart. The drift was the "no choices in OpenAI
// response" bug (PR #797) — compatibleNodeLane sent the request to /messages
// while the format machinery still treated the upstream as Chat Completions,
// so a Claude-format reply died in the OpenAI→Claude translator.
//
// Tests pin this table from both directions:
//   - internal/handlers/chat: lane, format and config stamping per node type;
//   - internal/handlers/dashboard: nodeIDPrefixes covers exactly these keys.
//
// Adding a node type means adding one entry here; both test suites then tell
// you what the request path still has to learn about it.
var CompatibleNodeWires = map[string]NodeWire{
	// An OpenAI-compatible node speaks Chat Completions by default; a stored
	// apiType of "responses" moves the lane to /responses (see
	// compatibleNodeLane), the format stays OpenAI either way.
	"openai-compatible": {Lane: "/chat/completions"},
	// An anthropic-compatible node fronts an Anthropic Messages endpoint.
	"anthropic-compatible": {Lane: "/messages", Format: FormatClaude},
	// A custom-embedding node is dialled on the chat lane; the embeddings
	// handler swaps the route to /embeddings off this same URL
	// (buildEmbeddingsURL), which is why no /embeddings lane is declared here.
	"custom-embedding": {Lane: "/chat/completions"},
}
