package providers

import (
	"runtime"
	"strings"
)

// nodePlatform is the Go runtime's platform in the spelling Node's
// process.platform uses, which is what upstream sends: "win32", not
// "windows".
func nodePlatform() string {
	return nodePlatformFor(runtime.GOOS)
}

// nodePlatformFor maps a Go OS name onto Node's process.platform spelling.
// It is a function of its argument so the mapping can be tested on a host
// that is not the one it describes.
func nodePlatformFor(goos string) string {
	if goos == "windows" {
		return "win32"
	}
	return goos
}

// NodePlatform is the platform name in Node's process.platform spelling
// ("win32" on Windows). Shared with the executors, whose envelopes carry the
// same field.
func NodePlatform() string {
	return nodePlatform()
}

// nodePlatformVersion is the Go runtime's version in the shape Node's
// process.version has: upstream sends a v-prefixed version, and a Go build
// string in that field is a fingerprint no real client sends.
func nodePlatformVersion() string {
	return "v" + strings.TrimPrefix(runtime.Version(), "go")
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
		"X-PLATFORM-VERSION": nodePlatformVersion(),
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
