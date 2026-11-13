package executor

import (
	"regexp"
	"strings"
	"sync"

	json "encoding/json/v2"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// Nonce binding and JSON-Schema validation for tool calls.
//
// The prompt tells the model to answer with one JSON object per call, and to
// carry a per-request `_nonce` binding in it. That binding is what stops a model
// from executing a `<tool>` block quoted out of the conversation — a client
// pastes a tool envelope into the chat to show the model an example, and without
// a binding the model repeats it as a call.
//
// The contract is upstream's (open-sse/translator/deepseekWebToolBinding.js):
//
//   - a call whose arguments carry `_nonce` is bound: the nonce must match and
//     the remaining arguments must validate against the declared schema;
//   - a call whose arguments carry no `_nonce` passes through untouched, as
//     upstream tolerates the omission for backward compatibility;
//   - any failure rejects the whole answer, because a partial list of calls is
//     worse than none: the client would run the surviving calls and report the
//     answer as complete.

// deepSeekNonceAlphabet is the alphabet upstream's Math.random().toString(36)
// slice draws from: 0-9 and a-z.
const deepSeekNonceAlphabet = "0123456789abcdefghijklmnopqrstuvwxyz"

// deepSeekToolNonce returns the binding nonce for one request's tool list.
//
// The nonce is per request and stable for the request: the prompt carries it and
// the parser compares against it, so minting twice would reject every call the
// prompt had told the model to make. A request that declared no tools has no
// nonce, which is upstream's getToolNonce returning "" for an empty list.
func deepSeekToolNonce(tools []deepSeekToolSpec) string {
	if len(tools) == 0 {
		return ""
	}
	nonce := make([]byte, 8)
	for i := range nonce {
		nonce[i] = deepSeekNonceAlphabet[deepSeekFastRand(int64(len(deepSeekNonceAlphabet)))]
	}
	return string(nonce)
}

// deepSeekFastRand is a cheap per-call randomness source.
//
// crypto/rand would be correct and needless here: the nonce is a prompt binding,
// not a secret, and upstream derives it from Math.random for the same reason.
// A package-level xorshift keeps it allocation-free on the hot path.
func deepSeekFastRand(n int64) int64 {
	deepSeekRandStateMu.Lock()
	state := deepSeekRandState
	state ^= state << 13
	state ^= state >> 7
	state ^= state << 17
	deepSeekRandState = state
	deepSeekRandStateMu.Unlock()
	if n <= 0 {
		return 0
	}
	// The state is signed and its sign bit carries no entropy: shift it out
	// before the remainder, because a signed remainder of a negative state is
	// negative — an index out of range.
	return (state & 0x7fffffffffffffff) % n
}

var (
	deepSeekRandState   int64 = 1
	deepSeekRandStateMu sync.Mutex
)

// deepSeekFinalizeBoundCalls applies the binding contract to the calls of one
// answer, mirroring upstream's finalizeBoundCalls.
//
// A call without `_nonce` in its arguments is returned unchanged and is not
// schema-validated — upstream's tolerance for a model that predates the binding.
// A call with one must match `nonce` and validate against the declared schema;
// the first failure rejects the whole answer, which is what makes the binding
// meaningful rather than advisory.
func deepSeekFinalizeBoundCalls(
	calls []deepSeekToolRequest,
	tools []deepSeekToolSpec,
	nonce string,
) ([]deepSeekToolRequest, bool) {
	cleaned := make([]deepSeekToolRequest, 0, len(calls))
	for _, call := range calls {
		args, ok := deepSeekArgumentsObject(call.Arguments)
		if !ok {
			return nil, false
		}
		bound, present := args["_nonce"]
		if !present {
			cleaned = append(cleaned, call)
			continue
		}
		if nonce == "" || bound != nonce {
			return nil, false
		}
		trimmed, ok := deepSeekWithoutNonce(args)
		if !ok {
			return nil, false
		}
		rebuilt, ok := deepSeekArgumentsObject(trimmed)
		if !ok || !deepSeekValidateToolArguments(tools, call.Name, rebuilt) {
			return nil, false
		}
		call.Arguments = trimmed
		cleaned = append(cleaned, call)
	}
	if len(cleaned) == 0 {
		return nil, false
	}
	return cleaned, true
}

// deepSeekArgumentsObject reads a call's arguments as a JSON object.
//
// The extractor guarantees valid JSON, so this is a shape check rather than a
// parse: upstream's finaliser rejects the answer when a call's arguments are not
// an object, and the Go extractor has no content field to return instead — so
// the caller falls back to treating the answer as ordinary text when this
// reports false.
func deepSeekArgumentsObject(encoded string) (map[string]any, bool) {
	trimmed := strings.TrimSpace(encoded)
	if trimmed == "" || trimmed[0] != '{' {
		return nil, false
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(trimmed), &args); err != nil {
		return nil, false
	}
	return args, true
}

// deepSeekWithoutNonce strips the binding property and re-encodes the object.
func deepSeekWithoutNonce(args map[string]any) (string, bool) {
	trimmed := make(map[string]any, len(args))
	for key, value := range args {
		if key == "_nonce" {
			continue
		}
		trimmed[key] = value
	}
	encoded, err := json.Marshal(trimmed)
	if err != nil {
		return "", false
	}
	return string(encoded), true
}

// deepSeekValidateToolArguments reports whether `args` validate against the
// schema the client declared for `name`, mirroring upstream's
// validateToolArguments.
//
// A tool the client never declared is invalid rather than merely unknown: the
// extractor rejects those names already, and the validator refusing them keeps
// the two checks from drifting. A declaration with no schema imposes nothing.
func deepSeekValidateToolArguments(tools []deepSeekToolSpec, name string, args map[string]any) bool {
	var schema string
	found := false
	for _, tool := range tools {
		if tool.Function.Name != name {
			continue
		}
		found = true
		schema = string(tool.Function.Parameters)
		break
	}
	if !found {
		return false
	}
	if len(strings.TrimSpace(schema)) == 0 {
		return true
	}
	compiled, ok := deepSeekCompiledSchema(schema)
	if !ok {
		return false
	}
	return compiled(args) == nil
}

// deepSeekCompiledSchema compiles one declared schema, caching by its bytes.
//
// A schema that cannot compile is a permanent property of the declaration, not
// of the call, so it is cached as a failure too: upstream's Ajv does the same
// and reports the tool as invalid rather than letting an unreadable schema
// impose nothing.
func deepSeekCompiledSchema(raw string) (func(map[string]any) error, bool) {
	key := raw
	deepSeekSchemaCacheMu.RLock()
	compiled, ok := deepSeekSchemaCache[key]
	deepSeekSchemaCacheMu.RUnlock()
	if ok {
		return compiled, compiled != nil
	}

	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("schema.json", strings.NewReader(key)); err != nil {
		deepSeekSchemaCacheMu.Lock()
		deepSeekSchemaCache[key] = nil
		deepSeekSchemaCacheMu.Unlock()
		return nil, false
	}
	compiledSchema, err := compiler.Compile("schema.json")
	if err != nil {
		deepSeekSchemaCacheMu.Lock()
		deepSeekSchemaCache[key] = nil
		deepSeekSchemaCacheMu.Unlock()
		return nil, false
	}
	validate := func(args map[string]any) error {
		return compiledSchema.Validate(args)
	}
	deepSeekSchemaCacheMu.Lock()
	deepSeekSchemaCache[key] = validate
	deepSeekSchemaCacheMu.Unlock()
	return validate, true
}

var (
	deepSeekSchemaCache   = map[string]func(map[string]any) error{}
	deepSeekSchemaCacheMu sync.RWMutex
)

// deepSeekDSMLMarkup matches the DSML dialect in every spelling the site emits:
// the bars may be full-width (U+FF5C) or ASCII, padded or not, and the marker
// may name `calls`, `invoke` or `parameter`.
var deepSeekDSMLMarkup = regexp.MustCompile(`(?i)</?(?:｜｜|\|\|?)DSML(?:｜｜|\|\|?)\s*(?:calls|invoke|parameter)\b`)

// deepSeekToolMarkup matches a bare `<tool>` or `<tool_call>` opener.
var deepSeekToolMarkup = regexp.MustCompile(`(?i)</?(?:tool|tool_call)(?::|\s|>)`)

// deepSeekStrayToolTag matches a tool tag left outside an accepted call — the
// empty `<tool></tool>` shell a JSON-body wrapper leaves behind once the inner
// object has been promoted, the unmatched opener of a doubled wrapper, and the
// `</tool_call>` of a block the parser rejected for its name only.
var deepSeekStrayToolTag = regexp.MustCompile(`(?i)</?(?:tool|tool_call)(?::[A-Za-z0-9_.+-]+)?(?:\s[^>]*)?>`)

// deepSeekStripStrayToolTags removes those shells from the text around accepted
// calls. Upstream applies the same filter to content after a successful parse
// (`<\/?(?:parameter|invoke|tool|calls|tool_calls|function_calls)\b[^>]*>`); a
// wrapper that carried a real call is markup, not prose, and handing it to the
// client shows an empty tag pair under a turn that did call a tool.
func deepSeekStripStrayToolTags(text string) string {
	return deepSeekStrayToolTag.ReplaceAllString(text, "")
}

// deepSeekHasMalformedToolMarkup reports whether the answer still carries tool
// markup after extraction.
//
// Upstream's hasMalformedDeepSeekToolMarkup. This is the retry trigger: a model
// that emitted a tool envelope the parser could not read has not answered, and
// the turn is worth one repair request on a fresh session.
func deepSeekHasMalformedToolMarkup(text string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	return deepSeekDSMLMarkup.MatchString(text) || deepSeekToolMarkup.MatchString(text)
}

// deepSeekHasMalformedToolIntent reports whether the answer expresses a tool
// call that failed safe parsing: markup, or a JSON object naming a declared
// tool with an arguments field.
//
// Upstream's hasMalformedDeepSeekToolIntent. The prose test is deliberately
// narrow — the whole answer must be the object, and the name must be one the
// client declared — because a JSON object sitting inside prose is the model
// talking about a call, not making one.
func deepSeekHasMalformedToolIntent(text string, tools []deepSeekToolSpec) bool {
	if deepSeekHasMalformedToolMarkup(text) {
		return true
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || trimmed[0] != '{' {
		return false
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(trimmed), &object); err != nil {
		return false
	}
	if _, present := object["arguments"]; !present {
		return false
	}
	name, _ := object["name"].(string)
	if name == "" {
		name, _ = object["command"].(string)
	}
	if name == "" {
		return false
	}
	for _, tool := range tools {
		if tool.Function.Name == name {
			return true
		}
	}
	return false
}
