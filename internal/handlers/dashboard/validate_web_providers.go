package dashboard

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"net/http"
	"strings"

	"9router/proxy/internal/proxy/executor"
)

// Probes for the browser-login providers, mirroring OmniRoute's
// src/lib/providers/validation/webProvidersA.ts and webProvidersB.ts.
//
// Neither probe issues a real completion. Both call the cheapest endpoint that
// still distinguishes a valid session from a dead one, because a probe that
// spends a model's worth of tokens to test a credential makes "Check" a
// billable action the operator did not ask for.

const deepSeekUsersCurrentURL = "https://chat.deepseek.com/api/v0/users/current"

// deepSeekWebHeaders is the executor's own fingerprint, not a copy of it.
//
// A probe is only evidence about the credential if it sends what the real turn
// sends: a header set maintained separately drifts, and a probe that passes with
// a stale `X-Client-Version` — or without the client-attestation headers —
// reports a credential usable when the executor would be judged differently.
// So the fingerprint comes from the one place that defines it.
func deepSeekWebHeaders() map[string]string {
	return executor.DeepSeekBrowserHeaders()
}

// validateDeepSeekWeb checks a pasted userToken against /api/v0/users/current,
// the one call that answers whether the credential can be exchanged for a
// working session.
func validateDeepSeekWeb(ctx context.Context, apiKey string) validateOutcome {
	token := executor.DeepSeekUserToken(apiKey)
	if token == "" {
		return validateOutcome{supported: true,
			message: "Missing userToken — paste the value from chat.deepseek.com DevTools → Application → Local Storage → userToken"}
	}

	headers := deepSeekWebHeaders()
	headers["Authorization"] = "Bearer " + token

	status, body, err := validateProbeDo(ctx, http.MethodGet, deepSeekUsersCurrentURL, headers, nil)
	if err != nil {
		return validateOutcome{supported: true, message: err.Error()}
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return validateOutcome{supported: true,
			message: "userToken is invalid or expired — get a fresh one from chat.deepseek.com localStorage"}
	}
	if status != http.StatusOK {
		return validateOutcome{supported: true,
			message: "chat.deepseek.com returned HTTP " + http.StatusText(status)}
	}

	// The account-level enforcement is the answer a suspended account gets, and
	// it is not an auth failure: the credential is fine and the account is
	// punished. Reported as "invalid token" a user re-pastes a token that was
	// never the problem, so the enforcement is named for what it is.
	if enforcement := executor.DeepSeekEnforcementFromBody(body); enforcement != nil {
		return validateOutcome{supported: true,
			message: "chat.deepseek.com refused this account: " + enforcement.Message}
	}

	// This API reports an auth failure as HTTP 200 with a code in the body, so a
	// 200 alone proves nothing. Upstream's observed rejection code is 40003.
	code, msg := deepSeekEnvelopeCode(body)
	switch code {
	case deepSeekAuthFailedCode:
		return validateOutcome{supported: true,
			message: "userToken is invalid or expired — get a fresh one from chat.deepseek.com localStorage"}
	case 0:
	default:
		return validateOutcome{supported: true,
			message: "chat.deepseek.com rejected the credential: " + msg}
	}

	if !deepSeekEnvelopeHasToken(body) {
		return validateOutcome{supported: true,
			message: "chat.deepseek.com returned no access token for this userToken"}
	}
	return validateOutcome{valid: true, supported: true}
}

// deepSeekAuthFailedCode is the code chat.deepseek.com returns for a rejected
// credential, alongside HTTP 200.
const deepSeekAuthFailedCode = 40003

// deepSeekEnvelope reads the {code, msg, data} envelope every chat.deepseek.com
// endpoint answers with. An unparseable body reads as code 0 with an empty
// message, so an unreadable answer is never mistaken for a rejected credential.
func deepSeekEnvelope(body []byte) (code int, msg string, data jsontext.Value) {
	var envelope struct {
		Code int            `json:"code"`
		Msg  string         `json:"msg"`
		Data jsontext.Value `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return 0, "", nil
	}
	return envelope.Code, envelope.Msg, envelope.Data
}

func deepSeekEnvelopeCode(body []byte) (int, string) {
	code, msg, _ := deepSeekEnvelope(body)
	return code, msg
}

// deepSeekEnvelopeHasToken reports whether the exchange produced a usable
// access token. The web API nests it under data.biz_data, with a flat
// data.token as the other shape seen in the wild.
func deepSeekEnvelopeHasToken(body []byte) bool {
	_, _, data := deepSeekEnvelope(body)
	if len(data) == 0 {
		return false
	}

	var nested struct {
		BizData struct {
			Token string `json:"token"`
		} `json:"biz_data"`
	}
	if err := json.Unmarshal(data, &nested); err == nil && nested.BizData.Token != "" {
		return true
	}

	var flat struct {
		Token string `json:"token"`
	}
	return json.Unmarshal(data, &flat) == nil && flat.Token != ""
}

// validateGeminiWeb reports why gemini-web cannot be validated here.
//
// gemini.google.com has no credential endpoint to call: it authenticates the
// page with __Secure-1PSID and issues its API tokens from inside the browser,
// so the only honest answer to "is this cookie usable" is a completed request
// through a real browser — which this build does not ship. Probing the site
// here would report a session as invalid when it may be perfectly valid, which
// is worse than saying so.
func validateGeminiWeb(_ context.Context, apiKey string) validateOutcome {
	if strings.TrimSpace(apiKey) == "" {
		return validateOutcome{supported: true,
			message: "Missing Gemini cookies — paste __Secure-1PSID (and __Secure-1PSIDTS when available) from gemini.google.com"}
	}
	return validateOutcome{supported: true,
		message: "gemini-web cannot be validated: gemini.google.com issues its tokens from inside the web client, " +
			"which this build does not ship. The cookie may still be valid — a chat request will be refused with the same reason."}
}
