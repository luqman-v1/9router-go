package executor

import (
	"crypto/sha1"
	"encoding/hex"
	json "encoding/json/v2"
	"fmt"
	"strings"
	"time"
)

// GeminiWebCookieHeader renders a pasted Gemini Web credential as a Cookie
// header. Exported so the dashboard probe reads the credential exactly the way
// the executor does — a probe that accepted a spelling the executor rejected
// would pass while every chat request failed.
func GeminiWebCookieHeader(apiKey string) string {
	return geminiWebCookieHeader(&Request{APIKey: apiKey})
}

// GeminiWebUserAgent is the client string the executor sends. Exported so the
// probe does not carry a second copy that drifts.
func GeminiWebUserAgent() string { return geminiWebUserAgent }

// geminiWebPlaceholderKeys are the values the router substitutes for a provider
// with no credential configured. They reach the executor as a real APIKey,
// because `NoAuth` providers are dispatched with their catalog `DefaultAPIKey`
// — so without this the placeholder is rendered into a cookie and sent to the
// site as `__Secure-1PSID=anonymous`, which is not a bug the user can see: the
// session still works, anonymously, and the wording of any refusal then blames
// a credential that was never configured.
//
// The check is deliberately on the whole value rather than a prefix: a real
// `__Secure-1PSID` never equals one of these, and a cookie that merely contains
// the word "anonymous" is a credential to pass through, not one to drop.
var geminiWebPlaceholderKeys = map[string]bool{
	"anonymous": true,
	"public":    true,
}

// geminiWebCookieHeader renders a configured credential as a Cookie header.
//
// Three spellings reach here, and all three are in the dashboard's own hint: a
// full cookie header, a bare `__Secure-1PSID` value, and a Cookie-Editor JSON
// export. A credential that is none of them is passed through unchanged, which
// is the only reading left.
func geminiWebCookieHeader(req *Request) string {
	raw := strings.TrimSpace(req.APIKey)
	if raw == "" || geminiWebPlaceholderKeys[raw] {
		return ""
	}
	if strings.HasPrefix(raw, "[") || strings.HasPrefix(raw, "{") {
		if expanded, ok := geminiWebExpandCookieJSON(raw); ok {
			return expanded
		}
	}
	if strings.Contains(raw, "=") {
		return raw
	}
	return "__Secure-1PSID=" + raw
}

// geminiWebExpandCookieJSON reads a Cookie-Editor style export into a header.
func geminiWebExpandCookieJSON(raw string) (string, bool) {
	var entries []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(raw), &entries); err != nil || len(entries) == 0 {
		return "", false
	}
	parts := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Name == "" {
			continue
		}
		parts = append(parts, entry.Name+"="+entry.Value)
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, "; "), true
}

// geminiWebSAPISIDHash renders the `authorization` header Google's batchexecute
// endpoint expects from a signed-in caller.
//
// The scheme is `SAPISIDHASH <unix-seconds>_<sha1("<seconds> <SAPISID> <origin>")>`,
// and it is what distinguishes a call made by a signed-in page from one made by
// a visitor: the batchexecute RPC that answers "which models may this session
// use" is the call that decides it, so a cookie presented without this header can
// still be answered as anonymous.
//
// An empty value means the credential carries no SAPISID to sign with, which the
// caller must treat as "send no header" rather than as a failure: an anonymous
// session has nothing to sign and works without it.
func geminiWebSAPISIDHash(cookie, origin string) string {
	sapisid := geminiWebCookieValue(cookie, "SAPISID")
	if sapisid == "" {
		return ""
	}
	if origin == "" {
		origin = geminiWebOrigin
	}
	seconds := time.Now().Unix()
	sum := sha1.Sum([]byte(fmt.Sprintf("%d %s %s", seconds, sapisid, origin)))
	return fmt.Sprintf("SAPISIDHASH %d_%s", seconds, hex.EncodeToString(sum[:]))
}

// geminiWebCookieValue reads one cookie's value out of a Cookie header.
//
// The match is on the whole name rather than a substring, because the names this
// is used for are prefixes of others: SAPISID is a prefix of nothing here, but
// __Secure-1PSID is a prefix of __Secure-1PSIDTS, and signing with the wrong
// one produces a header the site rejects with no diagnostic.
func geminiWebCookieValue(header, name string) string {
	for _, part := range strings.Split(header, ";") {
		key, value, found := strings.Cut(part, "=")
		if !found {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(key), name) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
