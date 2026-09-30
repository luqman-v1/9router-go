package chat

import (
	json "encoding/json/v2"
	"fmt"
	"time"

	"9router/proxy/internal/constants"
)

// writeExhaustedComboError renders the response for a combo pass in which
// every model failed. When the upstreams reported a cooldown, the earliest
// reset is published twice — the Retry-After header and the reset_at /
// retry_after pair in the JSON error object — because a client that never
// surfaces headers (browser SDKs, log-only integrations) has no other way to
// learn when the combo becomes usable again.
//
// Without a usable reset time the upstream body is forwarded untouched: an
// invented reset instant would send clients into a retry loop against a quota
// that has no known end.
func writeExhaustedComboError(cw *committedResponseWriter, lastErr *upstreamError, earliestRetryAfter string) {
	cw.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
	if earliestRetryAfter != "" {
		retryAt := mustParseTime(earliestRetryAfter)
		retryAfterSec := int((time.Until(retryAt) + time.Second - 1) / time.Second)
		if retryAfterSec < 1 {
			retryAfterSec = 1
		}
		cw.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfterSec))
		retryHuman := formatRetryAfter(earliestRetryAfter)
		var errBody map[string]any
		if err := json.Unmarshal(lastErr.Body, &errBody); err == nil {
			if errObj, ok := errBody["error"].(map[string]any); ok {
				if msg, _ := errObj["message"].(string); msg != "" {
					errObj["message"] = msg + " (" + retryHuman + ")"
					// A non-parsable Retry-After still yields a Retry-After
					// header above (it degrades to 1s), but a year-1
					// reset_at would be a lie a client schedules on, so the
					// body fields are only added for a real instant.
					if !retryAt.IsZero() {
						errObj["reset_at"] = retryAt.UTC().Format(time.RFC3339)
						errObj["retry_after"] = retryAfterSec
					}
					updated, _ := json.Marshal(errBody)
					cw.WriteHeader(lastErr.StatusCode)
					cw.Write(updated)
					return
				}
			}
		}
	}
	cw.WriteHeader(lastErr.StatusCode)
	cw.Write(lastErr.Body)
}
