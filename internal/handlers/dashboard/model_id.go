package dashboard

import "strings"

// Custom model ids are the half of an address a client copies into its own config:
// "<node-prefix>/<id>". Nothing splits them, nothing quotes them, and /v1/models
// publishes them verbatim — so a character that a client, a URL path or a shell would
// treat as a boundary cannot be part of one.
//
// This is the check that was missing when a paste of "model1 model2:free model3-ext"
// was saved as a single model with that whole string as its id: the dashboard sent one
// id, the endpoint stored it, and the row was unusable — it could only be addressed by
// reproducing the spaces exactly. The web form now splits a paste before sending, but
// the server is the authority, because any other client can post the same body.
const (
	// modelIDMaxLength keeps an id short enough to be readable in a config file.
	modelIDMaxLength = 200
	// modelIDSeparators are the characters a reader (and the paste parser) treats as a
	// boundary between ids. An id containing one is a paste that was never split.
	modelIDSeparators = " \t\r\n,;"
	// modelIDReserved are the characters with a meaning in the kv key or syntax:
	// "|" separates key fields. Slashes are allowed for namespaced models
	// (e.g. "deepseek/deepseek-v4-1-flash" or "meta-llama/llama-3.1-70b-instruct").
	modelIDReserved = `|"'` + "`" + `\<>`
)

// invalidModelIDReason reports why id cannot be stored as a custom model id, or "" when
// it can. The reason is written for the operator who pasted it, not for a log.
func invalidModelIDReason(id string) string {
	if id == "" {
		return "empty id"
	}
	if strings.ContainsAny(id, modelIDSeparators) {
		return "whitespace, comma and semicolon separate ids; this id was never split"
	}
	if strings.ContainsAny(id, modelIDReserved) {
		return `an id cannot contain | " ' \ < > or a backtick`
	}
	if strings.HasPrefix(id, "/") || strings.HasSuffix(id, "/") {
		return "an id cannot start or end with a slash"
	}
	if strings.Contains(id, "//") {
		return "an id cannot contain consecutive slashes"
	}
	if len(id) > modelIDMaxLength {
		return "an id cannot be longer than 200 characters"
	}
	return ""
}
