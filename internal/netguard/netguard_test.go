package netguard

import (
	"net"
	"testing"
)

// TestIsPrivateOrReserved pins the ranges that make the guard worth having.
// The four at the end are the ones a hand-written predicate lost: each is a
// real internal service, not a hypothetical, and each was measured reachable
// through the copy of this check that lived in internal/proxy.
func TestIsPrivateOrReserved(t *testing.T) {
	blocked := []string{
		"127.0.0.1",       // loopback
		"10.1.2.3",        // RFC1918
		"172.16.0.1",      // RFC1918
		"192.168.1.1",     // RFC1918
		"169.254.169.254", // AWS/GCP metadata
		"0.0.0.0",         // unspecified
		"224.0.0.1",       // multicast
		"100.100.100.200", // Alibaba Cloud metadata (inside RFC6598 CGNAT)
		"100.64.0.1",      // RFC6598 CGNAT
		"192.0.0.192",     // Oracle Cloud metadata
		"198.18.0.1",      // RFC2544 benchmark
		"::1",             // IPv6 loopback
		"fe80::1",         // IPv6 link-local
		"2001:db8::1",     // documentation
	}
	for _, s := range blocked {
		if ip := net.ParseIP(s); ip == nil {
			t.Fatalf("bad fixture %q", s)
		} else if !IsPrivateOrReserved(ip) {
			t.Errorf("%s must be blocked", s)
		}
	}

	allowed := []string{
		"1.1.1.1",
		"8.8.8.8",
		"93.184.216.34",  // example.com
		"100.63.255.255", // just below the CGNAT block
		"100.128.0.1",    // just above it
		"2606:4700::1111",
	}
	for _, s := range allowed {
		if ip := net.ParseIP(s); ip == nil {
			t.Fatalf("bad fixture %q", s)
		} else if IsPrivateOrReserved(ip) {
			t.Errorf("%s must be reachable, the guard blocked it", s)
		}
	}

	if IsPrivateOrReserved(nil) {
		t.Error("a nil address is not a blocked address")
	}
}

// TestParseIP_StripsZone pins the difference between this helper and net.ParseIP
// on a zoned literal. Every guard in this repository reads a host out of a URL
// or a dial address and asks "is this internal?"; net.ParseIP answers nil for
// "::1%lo", which each of those call sites reads as "not an address, allow it".
// A zone selects the outbound interface — it is not a different destination.
func TestParseIP_StripsZone(t *testing.T) {
	tests := []struct {
		name string
		host string
		want string
	}{
		{name: "plain ipv4", host: "127.0.0.1", want: "127.0.0.1"},
		{name: "plain ipv6", host: "::1", want: "::1"},
		{name: "zoned loopback", host: "::1%lo", want: "::1"},
		{name: "zoned link-local", host: "fd00:ec2::254%eth0", want: "fd00:ec2::254"},
		{name: "empty zone marker still parses", host: "::1%", want: "::1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := ParseIP(tt.host)
			if ip == nil {
				t.Fatalf("ParseIP(%q) = nil; net.ParseIP alone returns nil for a zoned literal, this helper must not", tt.host)
			}
			if ip.String() != tt.want {
				t.Errorf("ParseIP(%q) = %s, want %s", tt.host, ip, tt.want)
			}
		})
	}

	for _, host := range []string{"example.com", "", "not-an-ip"} {
		if ip := ParseIP(host); ip != nil {
			t.Errorf("ParseIP(%q) = %s, want nil: a hostname is not a literal address", host, ip)
		}
	}
}

// TestParseIP_ZonedAddressKeepsItsVerdict pins the two halves that have to agree:
// a zoned private literal must still be classified as private once the zone is
// gone, or stripping it would be cosmetic.
func TestParseIP_ZonedAddressKeepsItsVerdict(t *testing.T) {
	for _, host := range []string{"::1%lo", "fd00:ec2::254%eth0", "fe80::1%eth0"} {
		if ip := ParseIP(host); ip == nil || !IsPrivateOrReserved(ip) {
			t.Errorf("%s parsed to %v, which the guard does not treat as private", host, ip)
		}
	}
	if ip := ParseIP("2606:4700::1111%eth0"); ip == nil || IsPrivateOrReserved(ip) {
		t.Errorf("a public zoned address must stay reachable, got %v", ip)
	}
}
