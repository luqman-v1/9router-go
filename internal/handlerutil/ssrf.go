package handlerutil

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// blockedHostnames are exact-match hostnames that are never allowed.
var blockedHostnames = map[string]bool{
	"localhost":     true,
	"ip6-localhost": true,
	"ip6-loopback":  true,
}

// blockedSuffixes are hostname suffixes that are never allowed.
var blockedSuffixes = []string{".internal", ".local", ".localhost"}

// blockedIPv4Ranges defines private/reserved IPv4 ranges in CIDR-like form.
// Each entry is (network_int, maskBits).
var blockedIPv4Ranges = []struct {
	network uint32
	mask    uint8
}{
	{ip4(0, 0, 0, 0), 8},    // Current network
	{ip4(10, 0, 0, 0), 8},   // RFC 1918
	{ip4(127, 0, 0, 0), 8},  // Loopback
	{ip4(169, 254, 0, 0), 16}, // Link-local
	{ip4(172, 16, 0, 0), 12}, // RFC 1918
	{ip4(192, 168, 0, 0), 16}, // RFC 1918
}

func ip4(a, b, c, d byte) uint32 {
	return (uint32(a) << 24) | (uint32(b) << 16) | (uint32(c) << 8) | uint32(d)
}

// AssertPublicURL checks that rawURL does not point to an internal or private address.
// Returns an error if the URL is blocked (SSRF prevention).
// Matches Next.js assertPublicUrl in src/shared/utils/ssrfGuard.js.
func AssertPublicURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("blocked URL: empty host")
	}

	lower := strings.ToLower(host)

	// Check exact blocked hostnames
	if blockedHostnames[lower] {
		return fmt.Errorf("blocked URL: internal host")
	}

	// Check blocked suffixes
	for _, suffix := range blockedSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return fmt.Errorf("blocked URL: internal host")
		}
	}

	// Check IPv4 / IPv6 literal, and also resolve the hostname and check the
	// resolved addresses so DNS-rebinding / hostnames pointing at private IPs
	// are rejected too.
	candidates := []net.IP{}
	if ip := net.ParseIP(lower); ip != nil {
		candidates = append(candidates, ip)
	} else {
		addrs, err := net.LookupIP(lower)
		if err != nil {
			return fmt.Errorf("blocked URL: unresolvable host")
		}
		candidates = addrs
	}

	for _, ip := range candidates {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast() {
			return fmt.Errorf("blocked URL: private IP")
		}
		if v4 := ip.To4(); v4 != nil {
			ipInt := ip4ToUint32(v4)
			for _, r := range blockedIPv4Ranges {
				mask := uint32(0xFFFFFFFF << (32 - r.mask))
				if (ipInt & mask) == (r.network & mask) {
					return fmt.Errorf("blocked URL: private IP")
				}
			}
		} else {
			// IPv6: block loopback, link-local, unique-local
			h := ip.String()
			if h == "::1" || h == "::" {
				return fmt.Errorf("blocked URL: private IP")
			}
			if strings.HasPrefix(h, "fe80:") || strings.HasPrefix(h, "fc") || strings.HasPrefix(h, "fd") {
				return fmt.Errorf("blocked URL: private IP")
			}
		}
	}

	return nil
}

func ip4ToUint32(ip net.IP) uint32 {
	return (uint32(ip[0]) << 24) | (uint32(ip[1]) << 16) | (uint32(ip[2]) << 8) | uint32(ip[3])
}
