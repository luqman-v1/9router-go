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
//
// The site's own session cookie is sent along when there is one: the WAF
// reads it, and a probe that omits it judges the credential under different
// conditions than the turn will meet.
func validateDeepSeekWeb(ctx context.Context, apiKey string, providerData map[string]any) validateOutcome {
	token := executor.DeepSeekUserToken(apiKey)
	if token == "" {
		return validateOutcome{supported: true,
			message: "Missing userToken — paste the value from chat.deepseek.com DevTools → Application → Local Storage → userToken"}
	}

	headers := deepSeekWebHeaders()
	headers["authorization"] = "Bearer " + token
	if cookie, ok := providerData["deepSeekCookie"].(string); ok && strings.TrimSpace(cookie) != "" {
		headers["cookie"] = strings.TrimSpace(cookie)
	}

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

// validateGeminiWeb checks a pasted cookie against the site's model registry.
//
// `otAQ7b` is the RPC the page itself calls on load to ask which models the
// session may use, so it answers the credential question and nothing else: a
// valid session returns a model list, a rejected one returns the site's
// unauthenticated code. That keeps "Check" read-only rather than a billable
// completion — the same constraint the deepseek-web probe is built to.
//
// The probe is signed the way the executor signs its own registry call, so a
// probe that passes is evidence about the credential rather than about the
// header set. The signature is not what decides the served set — measured
// live on 2026-10-21, the site accepted the unsigned call too — but a signed
// call is the request a signed-in page makes, so it is the one sent.
//
// With no cookie there is nothing to check and nothing to complain about: the
// site answers an anonymous visitor, which is why the executor does not
// require a credential at all.
func validateGeminiWeb(ctx context.Context, apiKey string) validateOutcome {
	cookie := executor.GeminiWebCookieHeader(apiKey)
	if strings.TrimSpace(cookie) == "" {
		return validateOutcome{valid: true, supported: true,
			message: "No cookie configured — gemini-web still works, an anonymous session reaches one model. " +
				"Paste the cookie header of a signed-in gemini.google.com tab to reach the others."}
	}

	body, err := json.Marshal([]any{[]any{[]any{"otAQ7b", "[]", nil, "generic"}}})
	if err != nil {
		return validateOutcome{supported: true, message: "could not build the probe request: " + err.Error()}
	}
	status, response, err := validateProbeDo(ctx, http.MethodPost,
		"https://gemini.google.com/_/BardChatUi/data/batchexecute?rpcids=otAQ7b&source-path=%2Fapp&hl=en&rt=c",
		geminiWebProbeHeaders(cookie), []byte("f.req="+url.QueryEscape(string(body))))
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
	// A registry came back, but a credential with no `__Secure-1PSIDTS` is
	// answered as a visitor's: that cookie is what carries the session to the
	// registry, and without it the session reaches one model however valid
	// the rest of the header is. Reporting that as a working credential sends
	// the user off to paste a cookie they already pasted.
	if !executor.GeminiWebHasSessionTimestamp(cookie) {
		return validateOutcome{supported: true, message: geminiWebUnsignedCookieAdvice}
	}
	return validateOutcome{valid: true, supported: true}
}

// geminiWebUnsignedCookieAdvice names the credential that cannot reach the
// signed-in models.
//
// The cookie that decides it is `__Secure-1PSIDTS`: it is the one the site
// rotates every few minutes, and the one that carries a session into the
// model registry. A header without it answers as an anonymous visitor.
const geminiWebUnsignedCookieAdvice = "this cookie carries no __Secure-1PSIDTS, so gemini.google.com answers it " +
	"as an anonymous visitor and the session reaches one model only — copy the whole cookie header of the " +
	"signed-in tab, including __Secure-1PSIDTS"

// geminiWebStaleCookieAdvice is the one wording for a cookie the site refuses.
const geminiWebStaleCookieAdvice = "gemini.google.com refused this cookie — re-copy the whole cookie header " +
	"(__Secure-1PSID, SAPISID and __Secure-1PSIDTS when present) from a signed-in gemini.google.com tab"

// geminiWebProbeHeaders mirrors what the executor sends, so a probe that passes
// is evidence about the credential rather than about the header set.
//
// The cookie argument is the rendered header, not the raw credential, so the
// probe and the executor read the same value.
func geminiWebProbeHeaders(cookie string) map[string]string {
	headers := map[string]string{
		"Content-Type":  "application/x-www-form-urlencoded;charset=UTF-8",
		"Cookie":        cookie,
		"Origin":        "https://gemini.google.com",
		"Referer":       "https://gemini.google.com/app",
		"X-Same-Domain": "1",
		"User-Agent":    executor.GeminiWebUserAgent(),
	}
	if auth := executor.GeminiWebAuthorization(cookie); auth != "" {
		headers["Authorization"] = auth
	}
	return headers
}
