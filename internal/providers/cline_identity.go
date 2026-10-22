package providers

import (
	"runtime"
)

// nodePlatform is the Go runtime's platform in the spelling Node's
// process.platform uses, which is what upstream sends: "win32", not
// "windows".
func nodePlatform() string {
	if runtime.GOOS == "windows" {
		return "win32"
	}
	return runtime.GOOS
}

// ClineIdentityHeaders is the port of upstream buildClineHeaders
// (open-sse/shared/clineAuth.js) — the identity 9router presents to
// api.cline.bot. Platform values are the Go runtime's own. version is
// passed in (never imported): providers cannot import the version owner
// without an import cycle (db → providers → updater → daemon → auth → db),
// so callers pass updater.CurrentVersion.
func ClineIdentityHeaders(version string, extra map[string]string) map[string]string {
	headers := map[string]string{
		"HTTP-Referer":       "https://cline.bot",
		"X-Title":            "Cline",
		"User-Agent":         "9Router/" + version,
		"X-PLATFORM":         nodePlatform(),
		"X-PLATFORM-VERSION": runtime.Version(),
		"X-CLIENT-TYPE":      "9router",
		"X-CLIENT-VERSION":   version,
		"X-CORE-VERSION":     version,
		"X-IS-MULTIROOT":     "false",
	}
	for k, val := range extra {
		headers[k] = val
	}
	return headers
}
