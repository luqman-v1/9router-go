package media

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
)

// defaultWebSearchModel returns "search-combo" if available, then any webSearch combo, then falls back to tavily/search.
func (h *MediaHandler) defaultWebSearchModel() string {
	if h.Repo != nil {
		if combo, err := h.Repo.GetComboByName("search-combo"); err == nil && combo != nil && combo.Models != "" {
			return "search-combo"
		}
		if combos, err := h.Repo.GetCombos(); err == nil {
			for _, c := range combos {
				if c != nil && c.Kind != nil && *c.Kind == "webSearch" && c.Models != "" {
					return c.Name
				}
			}
		}
	}
	return "gpt-4o"
}

// defaultWebFetchModel returns "fetch-combo" if available, then any webFetch combo, then falls back to jina-reader.
func (h *MediaHandler) defaultWebFetchModel() string {
	if h.Repo != nil {
		if combo, err := h.Repo.GetComboByName("fetch-combo"); err == nil && combo != nil && combo.Models != "" {
			return "fetch-combo"
		}
		if combos, err := h.Repo.GetCombos(); err == nil {
			for _, c := range combos {
				if c != nil && c.Kind != nil && *c.Kind == "webFetch" && c.Models != "" {
					return c.Name
				}
			}
		}
	}
	return "jina-reader"
}

// HandleSearch handles /v1/search.
func (h *MediaHandler) HandleSearch(w http.ResponseWriter, r *http.Request) {
	r = r.WithContext(handlerutil.WithServiceKind(r.Context(), "webSearch"))
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer func() { _ = r.Body.Close() }()
	h.forwardMediaRequest(w, r, body, h.defaultWebSearchModel(), "/v1/search")
}

// HandleScrape handles /v1/scrape.
func (h *MediaHandler) HandleScrape(w http.ResponseWriter, r *http.Request) {
	r = r.WithContext(handlerutil.WithServiceKind(r.Context(), "webFetch"))
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer func() { _ = r.Body.Close() }()
	h.forwardMediaRequest(w, r, body, h.defaultWebFetchModel(), "/v1/scrape")
}

// HandleWebFetch handles /v1/web/fetch.
func (h *MediaHandler) HandleWebFetch(w http.ResponseWriter, r *http.Request) {
	r = r.WithContext(handlerutil.WithServiceKind(r.Context(), "webFetch"))
	body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer func() { _ = r.Body.Close() }()

	var req struct {
		URL      string `json:"url"`
		Model    string `json:"model"`
		Provider string `json:"provider"`
		Format   string `json:"format"`
	}
	if err := json.Unmarshal(body, &req); err != nil || strings.TrimSpace(req.URL) == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing or invalid url")
		return
	}

	parsedURL, err := url.Parse(req.URL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid url format")
		return
	}
	if targetPrivate(r.Context(), parsedURL.Hostname()) {
		handlerutil.WriteJSON(w, http.StatusForbidden, map[string]any{
			"error": map[string]string{"message": "private IP blocked by SSRF protection", "type": "permission_denied", "code": "address_forbidden"},
		})
		return
	}

	// A provider named without a model is the model: /v1/web/fetch carries the
	// extractor in `model`, and a caller that sent only `provider` was being
	// dispatched to the default instead of the one it named.
	if req.Model == "" && req.Provider != "" {
		var rawMap map[string]any
		if err := json.Unmarshal(body, &rawMap); err == nil && rawMap != nil {
			rawMap["model"] = req.Provider
			if rebuilt, mErr := json.Marshal(rawMap); mErr == nil {
				body = rebuilt
			}
		}
	}
	h.forwardMediaRequest(w, r, body, h.defaultWebFetchModel(), "/v1/web/fetch")
}

// targetPrivate reports whether host names an address the gateway must not
// fetch on a caller's behalf. A literal IP is judged directly; a name is
// resolved once, before dispatch. ALLOW_PRIVATE_NETWORKING is the operator's
// own switch and is answered first, so both branches agree: a literal IP and a
// name that resolves to the same address get the same verdict.
//
// This check is a refusal the caller can see, not the boundary itself: while
// ALLOW_PRIVATE_NETWORKING=false the dialer's socket-level guard is what
// actually stops the connection, and it also covers the rebind between this
// lookup and the dial. With the default (on) that guard is skipped, so this
// pre-flight is the only check. Checking here means the common case answers
// 403 with a reason instead of surfacing as a transport error from a provider
// several layers down.
func targetPrivate(ctx context.Context, host string) bool {
	if host == "" {
		return true
	}
	if proxy.IsPrivateNetworkingAllowed() {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return proxy.IsPrivateIP(ip)
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		// Unresolvable is not this check's call to make: let the dispatch
		// produce the transport error, which names the real problem.
		return false
	}
	for _, ip := range ips {
		if proxy.IsPrivateIP(ip) {
			return true
		}
	}
	return false
}

// maxRedirectHops restates net/http's default ceiling: CheckRedirect replaces
// the stdlib policy, so a chain that would have been cut at ten hops would
// otherwise loop until the request context died.
const maxRedirectHops = 10

// checkRedirectPublic is the CheckRedirect policy for every media client. A
// public entry point must not be able to walk the gateway into a private
// address with a 302: resolveSearchURL refuses a private search base URL before
// the first request, but the host it dialled can answer with a redirect to
// 127.0.0.1, 169.254.169.254 or an RFC1918 address, and with
// ALLOW_PRIVATE_NETWORKING on (the default) the dialer's socket-level guard is
// skipped, so this hop is the only refusal. Every hop is checked, not just the
// first: a chain of public hosts ending in loopback is the same fetch.
func checkRedirectPublic(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirectHops {
		return fmt.Errorf("stopped after %d redirects", maxRedirectHops)
	}
	if err := handlerutil.AssertPublicURL(req.URL.String()); err != nil {
		return fmt.Errorf("redirect refused: %s: %w", req.URL.Redacted(), err)
	}
	return nil
}

// guardMediaRedirects returns a copy of client that refuses a redirect hop
// landing on an internal address. A copy, because the client the chat handler
// hands out is shared by every lane, and one lane may not change the policy
// the others dial with.
func guardMediaRedirects(client *http.Client) *http.Client {
	clone := *client
	clone.CheckRedirect = checkRedirectPublic
	return &clone
}

// resolveSearchURL picks the search endpoint a provider dials. The provider's
// own configured base URL (registry default) is trusted as admin-controlled;
// anything else — a per-connection override stored by the dashboard, or a
// client-supplied baseUrl — is client-controlled and must be public http(s)
// only, otherwise a custom SearXNG (or any web search) base URL is an SSRF
// primitive: an attacker points it at 127.0.0.1:11434, 169.254.169.254 or a
// LAN host and the gateway fetches it on their behalf.
//
// Matches upstream resolveBaseUrl / assertPublicUrl: invalid URL, non-http(s)
// scheme, internal/private/loopback/link-local/metadata addresses are all
// refused. The guard runs here so a rejected override answers with a clear
// error instead of reaching the dialer (which under ALLOW_PRIVATE_NETWORKING=1
// — the default — lets every address class through).
func resolveSearchURL(cfg *providers.ProviderConfig, psd map[string]any, targetURL string) (string, error) {
	override := psdStr(psd, "baseUrl")
	if override != "" {
		u, err := url.Parse(override)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return "", fmt.Errorf("invalid search base URL: %q", override)
		}
		if u.Hostname() == "" {
			return "", fmt.Errorf("invalid search base URL: empty host")
		}
		if handlerutil.AssertPublicURL(override) != nil {
			return "", fmt.Errorf("search base URL is not allowed: %q", override)
		}
		return strings.TrimRight(override, "/"), nil
	}
	if cfg.SearchURL != "" {
		return cfg.SearchURL, nil
	}
	// No override and no configured search lane: keep the derived
	// targetURL (baseURL + endpoint) so generic search providers still
	// reach baseURL + /v1/search instead of an empty string.
	return targetURL, nil
}

// psdStr reads a string field from provider-specific data, mirroring the
// dashboard's copy so the media handler does not import the dashboard package.
func psdStr(psd map[string]any, keys ...string) string {
	if psd == nil {
		return ""
	}
	for _, k := range keys {
		if v, ok := psd[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
