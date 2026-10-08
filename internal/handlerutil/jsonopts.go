package handlerutil

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
)

// LenientUnmarshal parses upstream and client payloads the way encoding/json v1
// did, which is how this codebase read every payload before the
// encoding/json/v2 migration.
//
// v2 is deliberately stricter: it matches object member names
// case-sensitively, rejects a name repeated inside one object, and rejects
// invalid UTF-8 in strings instead of silently substituting U+FFFD. Those are
// the right defaults for JSON this gateway produces, but a provider body is
// not JSON this gateway produced, and the lenient reading is what the routing
// layer already depends on:
//
//   - UpstreamBody decodes bodies arriving from a provider. A duplicate member
//     or a stray byte in one must not turn a 200 that carries a real answer
//     into the 502 that ends combo fallback (proxy.EmptyUpstreamError), must not
//     drop a chunk that carries a content delta (responses_bridge), and must
//     not end a transcription session (media Gemini Live).
//   - ClientBody decodes bodies a client sent. This router speaks the OpenAI,
//     Claude and Gemini wire formats to models that do not always answer with
//     the canonical casing, so rejecting {"Model": ...} would fail a request
//     v1 served.
//
// Do not use either for JSON the gateway itself writes, nor to read rows it
// stored, nor to decide whether a payload is well-formed — for those, the
// strict defaults are correct.
var (
	// lenient relaxes all three places v2 is stricter than v1: member-name
	// casing, a name repeated inside one object, and invalid UTF-8 in strings.
	lenient = json.JoinOptions(
		json.MatchCaseInsensitiveNames(true),
		jsontext.AllowDuplicateNames(true),
		jsontext.AllowInvalidUTF8(true),
	)

	// UpstreamBody decodes a body received from a provider.
	UpstreamBody = lenient

	// ClientBody decodes a request body received from an API client.
	ClientBody = lenient
)
