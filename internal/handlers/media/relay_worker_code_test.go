package media

import (
	"strings"
	"testing"
)

func TestCloudflareRelayCode_ForwardsClientIP(t *testing.T) {
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

func TestVercelRelayCode_ForwardsClientIP(t *testing.T) {
	requiredSubstrings := []string{
		`x-vercel-forwarded-for`,
		`x-forwarded-for`,
		`x-real-ip`,
		`rawHeaders["x-forwarded-for"] = clientIP`,
		`rawHeaders["x-real-ip"] = clientIP`,
	}

	for _, sub := range requiredSubstrings {
		if !strings.Contains(vercelRelayCode, sub) {
			t.Errorf("vercelRelayCode missing required substring %q", sub)
		}
	}
}

func TestNetlifyRelayCode_ForwardsClientIP(t *testing.T) {
	requiredSubstrings := []string{
		`x-nf-client-connection-ip`,
		`client-ip`,
		`forwardHeaders["x-forwarded-for"] = clientIP`,
		`forwardHeaders["x-real-ip"] = clientIP`,
	}

	for _, sub := range requiredSubstrings {
		if !strings.Contains(netlifyRelayCode, sub) {
			t.Errorf("netlifyRelayCode missing required substring %q", sub)
		}
	}
}
