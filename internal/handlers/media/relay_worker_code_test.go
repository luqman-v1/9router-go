package media

import (
	"strings"
	"testing"
)

func TestCloudflareRelayCode_ForwardsClientIP(t *testing.T) {
	// Cloudflare Workers strip cf-connecting-ip on outbound subrequests.
	// To prevent 429 "Client IP address is required for free usage" on upstreams
	// like OpenCode free tier, the worker template must map incoming client IP
	// to standard x-forwarded-for and x-real-ip headers.
	requiredSubstrings := []string{
		`cf-connecting-ip`,
		`x-forwarded-for`,
		`x-real-ip`,
		`newRequestInit.headers.set("x-forwarded-for", clientIP)`,
		`newRequestInit.headers.set("x-real-ip", clientIP)`,
	}

	for _, sub := range requiredSubstrings {
		if !strings.Contains(cloudflareRelayCode, sub) {
			t.Errorf("cloudflareRelayCode missing required substring %q", sub)
		}
	}
}

func TestDenoRelayCode_ForwardsClientIP(t *testing.T) {
	requiredSubstrings := []string{
		`x-forwarded-for`,
		`x-real-ip`,
		`newHeaders.set("x-forwarded-for", clientIP)`,
		`newHeaders.set("x-real-ip", clientIP)`,
	}

	for _, sub := range requiredSubstrings {
		if !strings.Contains(denoRelayCode, sub) {
			t.Errorf("denoRelayCode missing required substring %q", sub)
		}
	}
}
