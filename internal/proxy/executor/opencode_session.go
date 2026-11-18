package executor

import "9router/proxy/internal/proxy"

// opencodeSessionHint resolves the upstream session a free-lane request
// travels with.
//
// The client's own session wins — BuildOpenCodeHeaders translates it into the
// canonical descending form. Without one, every request would mint a fresh
// upstream session, and the free tier accounts its quota per session: a fresh
// id per request burns through that budget and surfaces as 429
// FreeUsageLimitError with a growing retry-after, while the official CLI
// reuses one long-lived session per conversation (decolua/9router
// stableSessionId; dsh-our-free-model sessionForConversation). A hint-less
// request therefore falls back to one stable session per connection,
// recycled after an idle TTL.
//
// Keyed lanes keep their previous per-request behavior: their quota is
// accounted per key, not per session.
func opencodeSessionHint(req *Request, free bool) string {
	if !free || req.SessionID != "" {
		return req.SessionID
	}
	return proxy.StableOpenCodeSessionID(opencodeStableSessionKey(req, free))
}

// opencodeStableSessionKey reports the stable-session key a free-lane request
// resolves to when the client sent no session of its own, or "" — the marker
// that the upstream session is the router's to rotate when its budget is
// spent. The free flag must be the same one passed to opencodeSessionHint: a
// keyed lane's quota is accounted per key, so its session is never rotated.
//
// When a request routes through an assigned proxy, the proxy URL scopes the key
// so each egress IP keeps its own independent session rather than sharing one
// session ID across hundreds of distinct proxy IPs.
func opencodeStableSessionKey(req *Request, free bool) string {
	if !free || req.SessionID != "" {
		return ""
	}
	key := "opencode:default"
	if req.ConnectionID != "" {
		key = "opencode:conn:" + req.ConnectionID
	}
	if proxyURL := opencodeProxyFrom(req.Client); proxyURL != "" {
		key += ":proxy:" + proxyURL
	}
	return key
}

// opencodeSessionRotation returns the spent-session rotation hook when the
// stable session has already served prior requests. When a session was freshly
// minted for this attempt (zero prior turns), a 429 FreeUsageLimitError is an
// egress-IP refusal rather than a spent session, so skipping the second
// round-trip lets the proxy-pool walk advance immediately.
func opencodeSessionRotation(req *Request, free bool, failed string, install func(session string)) func() {
	stableKey := opencodeStableSessionKey(req, free)
	if stableKey == "" || !proxy.StableOpenCodeSessionEstablished(stableKey, failed) {
		return nil
	}
	return rotateOpencodeStableSession(stableKey, failed, install)
}

// rotateOpencodeStableSession builds the rotation hook forwardOpenCodeRotating
// calls when the gate reports the stable session's budget spent: expire the
// session that just failed and install a freshly minted one via install. A
// nil key (a client-supplied session) yields a nil hook, so no rotation is
// attempted. The expiry is conditional on failed so a rotation that loses a
// race against an already-rotated request keeps the newer session instead of
// discarding its fresh budget.
func rotateOpencodeStableSession(stableKey, failed string, install func(session string)) func() {
	if stableKey == "" {
		return nil
	}
	return func() {
		proxy.ExpireStableOpenCodeSession(stableKey, failed)
		if fresh := proxy.StableOpenCodeSessionID(stableKey); fresh != failed {
			install(fresh)
		}
	}
}
