package executor

import (
	json "encoding/json/v2"
	"errors"
	"net/http"

	"9router/proxy/internal/guardrails"
	"9router/proxy/internal/proxy"
)

// guardrailBlockFailure converts a policy refusal into the failure the routing
// layer sees.
//
// Its status is deliberately not one of the 502s the unusable-upstream helpers
// use. 502 is in providers.RetryableStatusCodes, so a blocked answer would lock
// the account it came from and hand the very content the policy refused to the
// next connection or model. A policy decision is not an upstream fault: the
// upstream served correctly and the answer was still refused, so no other
// account will do better and none of them should see it.
//
// 451 is also outside that set, so the fallback layer surfaces it as-is rather
// than failing the turn over.
func guardrailBlockFailure(err error) error {
	if !errors.Is(err, guardrails.ErrBlocked) {
		return err
	}
	body, mErr := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": guardrails.BlockedMessage + ".",
			"type":    "guardrail_error",
			"code":    http.StatusUnavailableForLegalReasons,
		},
	})
	if mErr != nil {
		body = []byte(`{"error":{"message":"` + guardrails.BlockedMessage + `.","type":"guardrail_error","code":451}}`)
	}
	return &proxy.UpstreamError{StatusCode: http.StatusUnavailableForLegalReasons, Body: body}
}
