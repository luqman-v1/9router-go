package dashboard

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"net/http"
	"net/url"
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

const (
	deepSeekUsersCurrentURL = "https://chat.deepseek.com/api/v0/users/current"
	deepSeekWebUserAgent    = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36"
)

// deepSeekWebHeaders is the same fingerprint the executor sends, so a probe
// that passes is evidence about the credential rather than about the header set.
func deepSeekWebHeaders() map[string]string {
	return map[string]string{
		"Accept":             "*/*",
		"Accept-Language":    "en-US,en;q=0.9",
		"Origin":             "https://chat.deepseek.com",
		"Referer":            "https://chat.deepseek.com/",
		"User-Agent":         deepSeekWebUserAgent,
		"X-Client-Bundle-Id": "com.deepseek.chat",
		"X-Client-Locale":    "en-US",
		"X-Client-Platform":  "web",
		"X-Client-Version":   "2.0.0",
	}
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

// validateGeminiWeb checks a pasted cookie against the site's model registry.
//
// `otAQ7b` is the RPC the page itself calls on load to ask which models the
// session may use, so it answers the credential question and nothing else: a
// valid session returns a model list, a rejected one returns the site's
// unauthenticated code. That keeps "Check" read-only rather than a billable
// completion — the same constraint the deepseek-web probe is built to.
//
// With no cookie there is nothing to check and nothing to complain about: the
// site answers an anonymous visitor, which is why the executor does not
// require a credential at all.
func validateGeminiWeb(ctx context.Context, apiKey string) validateOutcome {
	if strings.TrimSpace(apiKey) == "" {
		return validateOutcome{valid: true, supported: true,
			message: "No cookie configured — gemini-web still works, an anonymous session reaches one model. " +
				"Paste __Secure-1PSID from a signed-in gemini.google.com tab to reach the others."}
	}

	body, err := json.Marshal([]any{[]any{[]any{"otAQ7b", "[]", nil, "generic"}}})
	if err != nil {
		return validateOutcome{supported: true, message: "could not build the probe request: " + err.Error()}
	}
	status, response, err := validateProbeDo(ctx, http.MethodPost,
		"https://gemini.google.com/_/BardChatUi/data/batchexecute?rpcids=otAQ7b&source-path=%2Fapp&hl=en&rt=c",
		geminiWebProbeHeaders(apiKey), []byte("f.req="+url.QueryEscape(string(body))))
	if err != nil {
		return validateOutcome{supported: true, message: err.Error()}
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return validateOutcome{supported: true, message: geminiWebStaleCookieAdvice}
	}
	if status != http.StatusOK {
		return validateOutcome{supported: true,
			message: "gemini.google.com returned HTTP " + http.StatusText(status)}
	}
	// The site reports a rejected session inside an HTTP-200 body, so the status
	// alone proves nothing. What distinguishes the two is the content: a usable
	// session gets a model registry back. The body is not scanned for a status
	// code, because 1016 also occurs as a constant inside a healthy registry and
	// matching on it reported every working session as a refused one.
	if !bytes.Contains(response, []byte("wrb.fr")) {
		return validateOutcome{supported: true, message: geminiWebStaleCookieAdvice}
	}
	return validateOutcome{valid: true, supported: true}
}

// geminiWebStaleCookieAdvice is the one wording for a cookie the site refuses.
const geminiWebStaleCookieAdvice = "gemini.google.com refused this cookie — re-copy __Secure-1PSID " +
	"(and __Secure-1PSIDTS when present) from a signed-in gemini.google.com tab"

// geminiWebProbeHeaders mirrors what the executor sends, so a probe that passes
// is evidence about the credential rather than about the header set.
func geminiWebProbeHeaders(apiKey string) map[string]string {
	return map[string]string{
		"Content-Type":  "application/x-www-form-urlencoded;charset=UTF-8",
		"Cookie":        executor.GeminiWebCookieHeader(apiKey),
		"Origin":        "https://gemini.google.com",
		"Referer":       "https://gemini.google.com/app",
		"X-Same-Domain": "1",
		"User-Agent":    executor.GeminiWebUserAgent(),
	}
}
