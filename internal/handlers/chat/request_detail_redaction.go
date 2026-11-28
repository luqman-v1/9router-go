package chat

import (
	json "encoding/json/v2"

	"9router/proxy/internal/guardrails"
)

// storedSecretEngine masks credential-shaped strings in a request-detail
// payload before the payload reaches the database.
//
// The Recent Requests tab stores the whole conversation verbatim, so a
// credential a client pasted — a provider key in a prompt, a forwarded
// Authorization header, a client_secret in a config snippet — is written to
// disk and outlives every retention window. The debt register's DATA-01 names
// this: retention bounds how long a secret lives, not whether it is written at
// all. The engine is stateless, so one instance is shared.
//
// It is deliberately not the operator's outbound guardrail policy. That policy
// decides what may leave the gateway; this one decides what the gateway keeps
// about itself, so it is always on and not configurable.
var storedSecretEngine = guardrails.NewEngine([]string{"secrets"}, guardrails.ActionMask)

// redactStoredSecrets masks credential-shaped strings in a request-detail
// payload.
//
// The payload is normalised through JSON first: the logging helpers build
// typed values ([]map[string]string for the messages, structs for the fallback
// attempts), and ScanJSON's walker only descends generic maps and slices — a
// typed one is passed through untouched, which is exactly where a pasted key
// lives. Decoding and re-encoding erases the types in one pass, and every value
// here is already JSON-shaped, so nothing is lost.
func redactStoredSecrets(payload map[string]any) map[string]any {
	if len(payload) == 0 {
		return payload
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return payload
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return payload
	}
	mutated, _ := storedSecretEngine.ScanJSON(generic)
	masked, ok := mutated.(map[string]any)
	if !ok {
		return payload
	}
	return masked
}
