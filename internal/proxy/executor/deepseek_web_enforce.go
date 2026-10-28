package executor

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/proxy"
)

// Account-level enforcement: the way this site says "not this account, not
// now".
//
// Everything here arrives as HTTP 200 with the refusal in the payload, so a
// reader that trusts the status reads a muted account as a cut-off session and
// the router keeps sending turns to it — which is how an account that was
// briefly muted earns a longer mute. Three shapes are known:
//
//	{"code":0,"data":{"biz_code":5,"biz_msg":"user is muted",
//	                  "biz_data":{"is_muted":1,"mute_until":1780000000}}}
//	{"type":"error","content":"消息发送过于频繁，请稍后重试",
//	 "finish_reason":"rate_limit_reached"}
//	{"type":"error","content":"…","finish_reason":"unsupported_client_by_model"}
//
// The first is a plain JSON body (no event stream at all); the other two arrive
// as the payload of an `event: hint` / `event: toast` frame. All three have to
// reach the router as a status it can act on — 429 so the account is cooled
// down and the next connection serves the turn, rather than 502 which is
// retried against the same punished account.

// deepSeekEnforcementKind is what the upstream refused on.
type deepSeekEnforcementKind string

const (
	// deepSeekMuted is an account-level suspension. It is the state an abusive
	// or automated client gets, and the answer carries how long it lasts.
	deepSeekMuted deepSeekEnforcementKind = "muted"

	// deepSeekBanned is an account-level ban: the upstream refuses the
	// account itself, and no credential and no address reaches it.
	deepSeekBanned deepSeekEnforcementKind = "banned"

	// deepSeekIPRestricted is an address-level refusal: the account is fine
	// and this egress is not. Another address reaches it.
	deepSeekIPRestricted deepSeekEnforcementKind = "ip_restricted"

	// deepSeekRateLimited is a burst limit: the same account may be usable
	// again shortly, but not on this turn.
	deepSeekRateLimited deepSeekEnforcementKind = "rate_limited"

	// deepSeekClientRejected is a client-version or capability refusal (the
	// "update to the latest version" toast). It is not the account's fault, so
	// it must not cool the account down — but it is also not a served turn.
	deepSeekClientRejected deepSeekEnforcementKind = "client_rejected"

	// deepSeekEnforced is any other in-stream error frame.
	deepSeekEnforced deepSeekEnforcementKind = "enforced"
)

// deepSeekEnforcement is one refusal, with the wait the upstream named when it
// named one.
type deepSeekEnforcement struct {
	Kind    deepSeekEnforcementKind
	Message string

	// retryAfter is how long the upstream said the account is out for. Zero
	// when the payload named no end.
	retryAfter time.Duration
}

// deepSeekEnforcementStatus maps a refusal onto the HTTP status the router acts
// on.
//
// A mute and a burst limit are both 429 — the account must be cooled down and
// another connection tried. A ban is 403: the account is refused, and cooling
// it down for a minute would only send the next turn to the same refusal. An
// address-level refusal is 403 as well, and the difference is which one the
// operator is told about: a ban is the account, an address refusal is the
// egress. A client refusal is the gateway's own problem to report and not a
// reason to punish an account.
func (e *deepSeekEnforcement) status() int {
	switch e.Kind {
	case deepSeekMuted, deepSeekRateLimited:
		return http.StatusTooManyRequests
	case deepSeekBanned, deepSeekIPRestricted:
		return http.StatusForbidden
	default:
		return http.StatusBadGateway
	}
}

// error builds the failure the router sees. The wait travels as a Retry-After
// header, which is where the cooldown path reads it from, and the message names
// it too so an operator reading a log does not have to consult the header table.
func (e *deepSeekEnforcement) error() error {
	message := "deepseek-web: " + e.Message
	if e.retryAfter > 0 {
		message += " (available again after " + e.retryAfter.Round(time.Second).String() + ")"
	}

	body, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "upstream_error",
			"code":    e.status(),
		},
	})
	if err != nil {
		body = []byte(`{"error":{"message":"deepseek-web: upstream refused the turn","type":"upstream_error","code":502}}`)
	}

	upstreamErr := &proxy.UpstreamError{StatusCode: e.status(), Body: body}
	if e.retryAfter > 0 {
		upstreamErr.Header = http.Header{
			"Retry-After": []string{strconv.Itoa(int(e.retryAfter.Round(time.Second).Seconds()))},
		}
	}
	return upstreamErr
}

// deepSeekEnforcementFromBody reads a refusal out of a non-streamed 200 body.
//
// Two carriers are read here: the mute envelope above, and the site's general
// application error (`{"code":<int>,"msg":"…"}`), which also arrives with HTTP
// 200 and which the caller would otherwise have to parse a second time.
func deepSeekEnforcementFromBody(body []byte) *deepSeekEnforcement {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return nil
	}

	var envelope struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			BizCode int    `json:"biz_code"`
			BizMsg  string `json:"biz_msg"`
			BizData struct {
				IsMuted   int     `json:"is_muted"`
				MuteUntil float64 `json:"mute_until"`
			} `json:"biz_data"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(trimmed), &envelope); err != nil {
		return nil
	}

	if muted, ok := deepSeekMuteFromEnvelope(envelope.Data.BizCode, envelope.Data.BizMsg,
		envelope.Data.BizData.IsMuted, envelope.Data.BizData.MuteUntil); ok {
		return muted
	}
	// The general error envelope. The site's own bundle names the codes, and
	// they do not mean what they used to be read as: 40002 is MISSING_TOKEN,
	// not a rate limit, so a request that carried no credential is reported
	// as a credential problem instead of being cooled down and retried
	// against the same account.
	if envelope.Code != 0 {
		return deepSeekEnforcementForCode(envelope.Code, envelope.Msg)
	}
	return nil
}

// deepSeekEnforcementForCode maps one application error code onto the
// refusal the router acts on.
//
// The codes come from the site's own bundle (the enum it ships to the
// browser), not from inference: 40002 MISSING_TOKEN, 40003 INVALID_TOKEN,
// 40006 REQUEST_DECRYPT_FAILED, 40012 USER_IS_BANNED, 40029
// IP_ACCESS_RESTRICTED and 50006 MUTED.
func deepSeekEnforcementForCode(code int, message string) *deepSeekEnforcement {
	switch code {
	case deepSeekCodeAuthFailed, deepSeekCodeMissingToken:
		// Not an enforcement: a dead or absent credential is an auth failure
		// the caller already maps to a 401.
		return nil
	case deepSeekCodeRequestEncrypted:
		// The request body was not in the shape the site expects. That is
		// the gateway's own problem, not the account's, and it is not a
		// credential failure: the token was fine.
		return &deepSeekEnforcement{
			Kind:    deepSeekEnforced,
			Message: "the upstream refused the request body: " + message,
		}
	case deepSeekCodeUserBanned:
		return &deepSeekEnforcement{
			Kind:    deepSeekBanned,
			Message: "the upstream refused this account: " + message,
		}
	case deepSeekCodeIPRestricted:
		return &deepSeekEnforcement{
			Kind:    deepSeekIPRestricted,
			Message: "the upstream refused this address: " + message,
		}
	case deepSeekCodeMuted:
		return &deepSeekEnforcement{
			Kind:    deepSeekMuted,
			Message: "the upstream muted this account: " + message,
		}
	default:
		return &deepSeekEnforcement{
			Kind:    deepSeekEnforced,
			Message: fmt.Sprintf("upstream refused the turn (code %d): %s", code, message),
		}
	}
}

// DeepSeekEnforcementFromBody is the exported reader, for the dashboard probe.
//
// A probe that reported a suspended account as a dead credential sends the user
// to re-paste a token that was never the problem, and hides the one fact they
// need: the account itself is punished.
func DeepSeekEnforcementFromBody(body []byte) *deepSeekEnforcement {
	return deepSeekEnforcementFromBody(body)
}

// deepSeekMuteFromEnvelope reports whether an envelope is the account-mute
// enforcement, and how long it lasts.
//
// Either carrier is enough: `biz_code: 5` is the observed code, and a
// `biz_msg` that says "muted" is the same refusal spelled differently. The
// duration comes from `mute_until`, an epoch timestamp, when the payload
// carries one.
func deepSeekMuteFromEnvelope(bizCode int, bizMsg string, isMuted int, muteUntil float64) (*deepSeekEnforcement, bool) {
	muted := bizCode == deepSeekMuteBizCode || isMuted == 1 || strings.Contains(strings.ToLower(bizMsg), "mute")
	if !muted {
		return nil, false
	}

	message := bizMsg
	if message == "" {
		message = "account is muted by the upstream"
	}
	enforcement := &deepSeekEnforcement{Kind: deepSeekMuted, Message: message}
	if muteUntil > 0 {
		if until := time.Unix(int64(muteUntil), 0); until.After(time.Now()) {
			enforcement.retryAfter = time.Until(until)
			enforcement.Message = message + " until " + until.UTC().Format(time.RFC3339)
		}
	} else {
		// A mute with no announced end is still not a turn to retry, and the
		// router's default backoff for a 429 is the only honest reading of it.
		enforcement.Message = message + " (no release time given)"
	}
	return enforcement, true
}

// deepSeekMuteBizCode is the observed `biz_code` of the mute enforcement.
const deepSeekMuteBizCode = 5

// deepSeekEnforcementFromEvent reads a refusal out of one stream event payload.
//
// The payload of an `event: hint` / `event: toast` frame is a top-level object
// carrying `type: "error"`, sometimes wrapped as `{"v":{…}}`. The scanner
// emits only `data:` payloads, so the event name is not available here and the
// payload's own shape has to carry the meaning.
func deepSeekEnforcementFromEvent(payload []byte) *deepSeekEnforcement {
	var envelope struct {
		Type         string `json:"type"`
		Content      string `json:"content"`
		Msg          string `json:"msg"`
		FinishReason string `json:"finish_reason"`
		V            *struct {
			Type         string `json:"type"`
			Content      string `json:"content"`
			Msg          string `json:"msg"`
			FinishReason string `json:"finish_reason"`
		} `json:"v"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil
	}
	if !strings.EqualFold(envelope.Type, "error") && envelope.V != nil {
		envelope.Type = envelope.V.Type
		envelope.Content = envelope.V.Content
		envelope.Msg = envelope.V.Msg
		envelope.FinishReason = envelope.V.FinishReason
	}
	if !strings.EqualFold(envelope.Type, "error") {
		return nil
	}

	message := envelope.Content
	if message == "" {
		message = envelope.Msg
	}
	if message == "" {
		message = "upstream refused the turn"
	}

	kind := deepSeekEnforced
	switch {
	case strings.Contains(envelope.FinishReason, "rate_limit"):
		// A burst limit. The site answers a repeat of it with the mute
		// enforcement, so it is treated as the same 429 shape.
		kind = deepSeekRateLimited
	case strings.Contains(strings.ToLower(message), "频繁"):
		kind = deepSeekRateLimited
	case strings.Contains(envelope.FinishReason, "unsupported_client"):
		kind = deepSeekClientRejected
	}
	return &deepSeekEnforcement{Kind: kind, Message: message + " (" + envelope.FinishReason + ")"}
}
