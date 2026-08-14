package proxy

import (
	"net/url"
	"strings"
)

// ShouldBypassNoProxy checks whether targetURL matches any pattern in noProxy.
func ShouldBypassNoProxy(targetURL, noProxy string) bool {
	noProxy = strings.TrimSpace(noProxy)
	if noProxy == "" {
		return false
	}
	if noProxy == "*" {
		return true
	}

	parsed, err := url.Parse(targetURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		host = strings.ToLower(targetURL)
	}

	patterns := strings.Split(noProxy, ",")
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if p == "*" {
			return true
		}
		if strings.HasPrefix(p, ".") {
			if strings.HasSuffix(host, p) || host == p[1:] {
				return true
			}
		}
		if host == p || strings.HasSuffix(host, "."+p) {
			return true
		}
	}
	return false
}

// BuildEdgeRelayHeaders formats headers for Vercel, Cloudflare, and Deno edge relays.
func BuildEdgeRelayHeaders(targetURL string, existingHeaders map[string]string) map[string]string {
	headers := make(map[string]string, len(existingHeaders)+2)
	for k, v := range existingHeaders {
		headers[k] = v
	}

	parsed, err := url.Parse(targetURL)
	if err == nil {
		headers["x-relay-target"] = parsed.Scheme + "://" + parsed.Host
		path := parsed.Path
		if path == "" {
			path = "/"
		}
		if parsed.RawQuery != "" {
			path += "?" + parsed.RawQuery
		}
		headers["x-relay-path"] = path
	}
	return headers
}
