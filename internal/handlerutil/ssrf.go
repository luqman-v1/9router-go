package handlerutil

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
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
	{ip4(0, 0, 0, 0), 8},     // Current network (0.0.0.0/8)
	{ip4(10, 0, 0, 0), 8},    // RFC 1918
	{ip4(100, 64, 0, 0), 10}, // CGNAT (RFC 6598) — also used by some cloud metadata proxies
	{ip4(127, 0, 0, 0), 8},   // Loopback
	{ip4(169, 254, 0, 0), 16}, // Link-local (includes 169.254.169.254 cloud metadata)
	{ip4(172, 16, 0, 0), 12}, // RFC 1918
	{ip4(192, 168, 0, 0), 16}, // RFC 1918
}

func ip4(a, b, c, d byte) uint32 {
	return (uint32(a) << 24) | (uint32(b) << 16) | (uint32(c) << 8) | uint32(d)
}

func ipv4ToInt(host string) *uint32 {
	parts := strings.Split(host, ".")
	if len(parts) != 4 {
		return nil
	}
	var ip uint32
	for _, p := range parts {
		if p == "" {
			return nil
		}
		// Reject leading zeros? Keep simple: allow decimal only (upstream ipv4ToInt does strict)
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 255 {
			return nil
		}
		// Prevent octal/hex bypass: ensure canonical decimal without leading zeros unless "0"
		if len(p) > 1 && p[0] == '0' {
			return nil
		}
		ip = (ip << 8) | uint32(n)
	}
	return &ip
}

func isBlockedIpv4Int(ip uint32) bool {
	for _, r := range blockedIPv4Ranges {
		mask := uint32(0xFFFFFFFF << (32 - r.mask))
		if (ip & mask) == (r.network & mask) {
			return true
		}
	}
	return false
}

func isBlockedIpv4(host string) bool {
	v := ipv4ToInt(host)
	if v == nil {
		return false
	}
	return isBlockedIpv4Int(*v)
}

func parseHextets(s string) ([]int, bool) {
	if s == "" {
		return []int{}, true
	}
	segs := strings.Split(s, ":")
	out := make([]int, 0, len(segs))
	for _, seg := range segs {
		if seg == "" {
			return nil, false
		}
		if len(seg) > 4 {
			return nil, false
		}
		for _, c := range seg {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return nil, false
			}
		}
		v, err := strconv.ParseInt(seg, 16, 32)
		if err != nil {
			return nil, false
		}
		out = append(out, int(v))
	}
	return out, true
}

var ipv4TailRegexp = regexp.MustCompile(`(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})$`)

// parseIPv6ToGroups parses any textual IPv6 representation (including embedded dotted-IPv4 tail,
// "::" compression, full/partial forms) into 8 16-bit groups. Returns nil if not valid IPv6.
// Reasoning about numeric groups rather than string pattern makes it immune to textual-form bypasses
// ("::ffff:127.0.0.1" vs "::ffff:7f00:1").
func parseIPv6ToGroups(rawHost string) []int {
	host := strings.ToLower(rawHost)
	// Handle embedded dotted IPv4 tail
	var v4Groups []int
	if m := ipv4TailRegexp.FindString(host); m != "" {
		v4Int := ipv4ToInt(m)
		if v4Int == nil {
			return nil
		}
		v4Groups = []int{int((*v4Int >> 16) & 0xFFFF), int(*v4Int & 0xFFFF)}
		host = host[:len(host)-len(m)]
		if strings.HasSuffix(host, "::") {
			// "::" compression marker itself — leave both colons
		} else if strings.HasSuffix(host, ":") {
			host = host[:len(host)-1]
		}
	}
	parts := strings.Split(host, "::")
	if len(parts) > 2 {
		return nil
	}
	var groups []int
	if len(parts) == 2 {
		head, ok1 := parseHextets(parts[0])
		tail, ok2 := parseHextets(parts[1])
		if !ok1 || !ok2 {
			return nil
		}
		v4Len := len(v4Groups)
		missing := 8 - len(head) - len(tail) - v4Len
		if missing < 0 {
			return nil
		}
		groups = append(groups, head...)
		for i := 0; i < missing; i++ {
			groups = append(groups, 0)
		}
		groups = append(groups, tail...)
		groups = append(groups, v4Groups...)
	} else {
		all, ok := parseHextets(host)
		if !ok {
			return nil
		}
		groups = append(all, v4Groups...)
	}
	if len(groups) != 8 {
		return nil
	}
	return groups
}

func isBlockedIpv6Groups(g []int) bool {
	if len(g) != 8 {
		return false
	}
	isZero := func(n int) bool { return g[n] == 0 }
	// loopback ::1
	if isZero(0) && isZero(1) && isZero(2) && isZero(3) && isZero(4) && isZero(5) && isZero(6) && g[7] == 1 {
		return true
	}
	// unspecified ::
	if g[0] == 0 && g[1] == 0 && g[2] == 0 && g[3] == 0 && g[4] == 0 && g[5] == 0 && g[6] == 0 && g[7] == 0 {
		return true
	}
	// link-local fe80::/10
	if (g[0] & 0xFFC0) == 0xFE80 {
		return true
	}
	// unique local fc00::/7
	if (g[0] & 0xFE00) == 0xFC00 {
		return true
	}
	low32 := uint32(g[6])<<16 | uint32(g[7])
	// IPv4-mapped ::ffff:0:0/96
	if isZero(0) && isZero(1) && isZero(2) && isZero(3) && isZero(4) && g[5] == 0xFFFF {
		return isBlockedIpv4Int(low32)
	}
	// NAT64 well-known prefix 64:ff9b::/96
	if g[0] == 0x0064 && g[1] == 0xFF9B && isZero(2) && isZero(3) && isZero(4) && isZero(5) {
		return isBlockedIpv4Int(low32)
	}
	// IPv4-compatible ::a.b.c.d/96 (deprecated)
	if isZero(0) && isZero(1) && isZero(2) && isZero(3) && isZero(4) && isZero(5) && low32 != 0 && low32 != 1 {
		return isBlockedIpv4Int(low32)
	}
	return false
}

func normalizeHost(hostname string) string {
	return strings.ToLower(strings.TrimRight(hostname, "."))
}

func isBlockedHost(host string) bool {
	if blockedHostnames[host] {
		return true
	}
	for _, s := range blockedSuffixes {
		if strings.HasSuffix(host, s) {
			return true
		}
	}
	if isBlockedIpv4(host) {
		return true
	}
	if strings.Contains(host, ":") {
		trimmed := strings.Trim(host, "[]")
		if groups := parseIPv6ToGroups(trimmed); groups != nil {
			if isBlockedIpv6Groups(groups) {
				return true
			}
		}
	}
	return false
}

// AssertPublicURL checks that rawURL does not point to an internal or private address.
// Returns an error if the URL is blocked (SSRF prevention).
// Matches Next.js assertPublicUrl in src/shared/utils/ssrfGuard.js (hardened #3714).
func AssertPublicURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("blocked URL: empty host")
	}
	normalized := normalizeHost(host)
	if isBlockedHost(normalized) {
		return fmt.Errorf("blocked URL: internal host")
	}
	// Also resolve the hostname and check resolved addresses so DNS-rebinding /
	// wildcard DNS hostnames pointing at private IPs are rejected too.
	// Literal IPs were already handled by isBlockedHost; skip DNS for them.
	trimmed := strings.Trim(normalized, "[]")
	if ipv4ToInt(trimmed) != nil || strings.Contains(trimmed, ":") {
		if groups := parseIPv6ToGroups(trimmed); groups != nil {
			// Already checked literal via isBlockedHost, but keep for safety:
			// net.ParseIP path also covers textual bypasses.
			return nil
		}
		if ipv4ToInt(trimmed) != nil {
			return nil
		}
		// If it looks like IP literal but failed to parse as blocked, still check via net.ParseIP
		if ip := net.ParseIP(trimmed); ip != nil {
			// Literal IP already validated; if not blocked, allow
			return nil
		}
	}
	// Non-literal hostname: DNS resolve and check all returned addresses
	addrs, err := net.LookupIP(normalized)
	if err != nil {
		// Resolution failure isn't an SSRF signal by itself — let subsequent fetch fail with clearer error
		return nil
	}
	for _, ip := range addrs {
		// Use Go's built-in checks plus custom ranges
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast() {
			return fmt.Errorf("blocked URL: hostname resolves to an internal host")
		}
		// Check private + custom ranges via IsPrivate fallback and explicit range
		if v4 := ip.To4(); v4 != nil {
			ipInt := ip4ToUint32(v4)
			if isBlockedIpv4Int(ipInt) {
				return fmt.Errorf("blocked URL: hostname resolves to an internal host")
			}
			if ip.IsPrivate() {
				return fmt.Errorf("blocked URL: hostname resolves to an internal host")
			}
		} else {
			h := strings.ToLower(ip.String())
			if groups := parseIPv6ToGroups(h); groups != nil {
				if isBlockedIpv6Groups(groups) {
					return fmt.Errorf("blocked URL: hostname resolves to an internal host")
				}
			} else {
				if ip.IsPrivate() {
					return fmt.Errorf("blocked URL: hostname resolves to an internal host")
				}
			}
		}
	}
	return nil
}

// AssertPublicURLResolved is like AssertPublicURL but explicitly async DNS-resolved variant.
// In Go it is identical (DNS is already done) — kept for parity with Next.js assertPublicUrlResolved.
func AssertPublicURLResolved(rawURL string) error {
	return AssertPublicURL(rawURL)
}

func ip4ToUint32(ip net.IP) uint32 {
	return (uint32(ip[0]) << 24) | (uint32(ip[1]) << 16) | (uint32(ip[2]) << 8) | uint32(ip[3])
}
