package executor

import (
	"time"
)

// The header set and order chat.deepseek.com's own web client sends.
//
// Both were captured from a real Chrome's netlog, on the wire, against the
// live site: the order is what Chrome wrote, not what the endpoint requires.
// Order is part of the fingerprint, and Go's net/http sorts headers
// alphabetically before writing — so a request that goes out in the wrong
// order is a request no browser made.
//
// The capture produced one finding that a single "one header set for every
// call" model gets wrong, and it is the reason this file exists:
//
//   - The site runs TWO different header sets. The `x-client-*` block
//     (bundle id, platform, version, locale, timezone offset) is attached by
//     the app's request interceptor, which runs for the CLIENT calls — the
//     settings and telemetry ones. The chat calls — the token exchange, the
//     proof-of-work challenge, the completion — are issued by a different
//     layer and carry NO `x-client-*` block at all. A request that carries
//     the block on a chat call is a request no browser makes.
//
// The per-endpoint split, as captured:
//
//	GET  /api/v0/client/settings   x-client block + x-device-id + x-device-model
//	POST /api/v0/telemetry         x-device-id, x-ds-compression, no x-client block
//	GET  /api/v0/users/current     neither
//	POST /api/v0/chat/create_pow_challenge   neither
//
// Two more observations from the capture shaped this file:
//
//   - The API requests carry NO `sec-ch-ua*` client hints. Only the telemetry
//     request carries them, and that one is the site's own instrumentation
//     rather than its API client. Sending client hints on every API call is a
//     fingerprint of its own: no browser sends them there.
//   - `x-device-model` is sent with an EMPTY value, which is itself part of
//     the fingerprint. A request that omits it, or fills it in, is not the
//     request the site sent.

// deepSeekAPIHeaderOrder is the order Chrome wrote an API request in.
//
// The order is fixed: the capture sent the same header set with two
// different literal insertion orders and Chrome wrote both the same way, so
// the order below is the client's, not the caller's.
//
// This is the order of a CHAT call — the shape the token exchange, the
// proof-of-work challenge and the completion all share. The client-block
// calls carry the same order with the `x-client-*` names inserted where the
// capture put them; see deepSeekClientHeaderOrder.
var deepSeekAPIHeaderOrder = []string{
	"content-length",
	"authorization",
	"accept-language",
	"user-agent",
	"content-type",
	"accept",
	"origin",
	"sec-fetch-site",
	"sec-fetch-mode",
	"sec-fetch-dest",
	"referer",
	"accept-encoding",
	"cookie",
	"priority",
}

// deepSeekAPIBaseHeaders are the headers every /api/v0/* call carries.
//
// The values are the ones the live client sends.
//
// `origin` is only sent on the calls the site sends it on: the capture shows
// it on the POSTs and not on the GETs, because a GET without a body is a
// same-origin navigation-shaped fetch that the browser does not mark. It is
// set per call by the caller, not here.
func deepSeekAPIBaseHeaders(origin string) map[string]string {
	return map[string]string{
		"accept":          "*/*",
		"accept-encoding": "gzip, deflate, br, zstd",
		// The value Chrome itself serializes. The duplicated `;q=0.9;q=0.9`
		// this provider used to send is the constant every scripted client
		// copied from the same reference implementation — Chrome cannot even
		// produce it — so it reads as a badge that says scripted.
		"accept-language": "en-US,en;q=0.9",
		"content-type":    "application/json",
		// The referer of a logged-in client is the app page it stands on.
		// The calls that name a specific conversation override this with that
		// conversation's page; see deepSeekChatPageURL.
		"referer":        origin + "/",
		"sec-fetch-dest": "empty",
		"sec-fetch-mode": "cors",
		"sec-fetch-site": "same-origin",
		"user-agent":     deepSeekWebUserAgent,
		// The site's own client sends the fetch priority on every API call.
		// It is what a browser writes for a fetch it wants scheduled
		// promptly, and it is the last header before the cookie in the
		// captured order.
		"priority": "u=1, i",
	}
}

// deepSeekTimezoneOffsetSeconds is the value `x-client-timezone-offset`
// carries.
//
// The site sends the offset EAST of UTC, in seconds: a machine on UTC+7
// sends 25200, which is what the live capture showed. The previous
// implementation negated Go's offset and so sent -25200 for the same
// machine — the one value a real client can never send, and a free signal
// that the request did not come from a browser.
func deepSeekTimezoneOffsetSeconds() int {
	_, offsetSeconds := time.Now().Zone()
	return offsetSeconds
}

// deepSeekChatPageURL is the page the app stands on while it talks about one
// conversation: https://chat.deepseek.com/a/chat/s/<session id>.
//
// A browser sets Referer to the current page's URL on every same-origin
// fetch, so the calls that carry a conversation — the completion and the
// proof-of-work challenge it precedes — carry this page, not the site root.
// The previous implementation sent /sign_in on every call, which no
// logged-in client can produce: the sign-in page is where a user without a
// session lands, and an Authorization header names a session.
func deepSeekChatPageURL(origin, sessionID string) string {
	if sessionID == "" {
		return origin + "/"
	}
	return origin + "/a/chat/s/" + sessionID
}
