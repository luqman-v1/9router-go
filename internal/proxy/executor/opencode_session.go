package executor

import "9router/proxy/internal/proxy"

// opencodeSessionHint resolves the upstream session a free-lane request
// travels with.
//
// The free tier accounts its quota per x-opencode-session. Two cases matter:
//
//   - A client that sends a native `ses_` session owns that conversation: it is
//     forwarded verbatim (BuildOpenCodeHeaders keeps the canonical descending
//     form) and is never rotated behind the client's back.
//   - Every other session hint — an omp UUID, a Claude Code UUID, a bare id —
//     is quoted under a router-minted stable session. Those ids are not the
//     upstream's conversation identifiers: BuildOpenCodeHeaders rewrites them
//     with TranslateOpenCodeSessionID anyway. Treating them as "client-owned"
//     left every such request on one translated session with NO rotation hook,
//     so once its budget was spent every later turn 429'd until restart —
//     while a hint-less request on a different, healthy session still returned
//     200. Quoting them keeps one upstream session per client conversation AND
//     lets the 429 recovery rotate it when it is spent (#5559).
//
// Hint-less requests use one stable session per connection, recycled after an
// idle TTL. Keyed lanes keep their previous per-request behavior: their quota
// is accounted per key, not per session.
func opencodeSessionHint(req *Request, free bool) string {
	if !free {
		return ""
	}
	if req.SessionID != "" && proxy.IsCanonicalOpenCodeSession(req.SessionID) {
		return req.SessionID
	}
	return proxy.StableOpenCodeSessionID(opencodeStableSessionKey(req, free))
}

// opencodeStableSessionKey reports the stable-session key a free-lane request
// resolves to, or "" when the session belongs entirely to the client (a keyed
// lane, or a client-supplied native `ses_` id). A non-empty key is the marker
// that the router owns the session and may rotate it after a 429.
//
// Proxy scope keeps egress IPs independent: hundreds of distinct proxy IPs
// must not share one upstream session.
func opencodeStableSessionKey(req *Request, free bool) string {
	if !free {
		return ""
	}
	var key string
	switch {
	case req.SessionID == "":
		key = "opencode:default"
		if req.ConnectionID != "" {
			key = "opencode:conn:" + req.ConnectionID
		}
	case proxy.IsCanonicalOpenCodeSession(req.SessionID):
		return ""
	default:
		key = "opencode:client:" + req.SessionID
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
// nil key (a client-supplied native session, or a keyed lane) yields no hook at
// all. The expiry is conditional on failed so a rotation that loses a race
// against an already-rotated request keeps the newer session instead of
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

// forgetOpencodeStableSession drops the stable session a failed request traveled
// on, so the next request mints a fresh one. Upstream spends or flags a session
// id after enough traffic, and every later request that reused it failed the
// same way until restart (#5559).
//
// Called by forwardOpenCodeRotating for a 403 or a non-overload 429: those are
// verdicts on the session, not on the request shape. A nil key (a client-
// supplied native session, or a keyed lane) yields no hook — neither is ours to
// drop.
func forgetOpencodeStableSession(req *Request, free bool, failed string) func() {
	stableKey := opencodeStableSessionKey(req, free)
	if stableKey == "" {
		return nil
	}
	return func() { proxy.ExpireStableOpenCodeSession(stableKey, failed) }
}
