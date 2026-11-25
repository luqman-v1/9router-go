package tokensaver

import (
	"strings"
	"testing"
)

// buildPing builds a ping run: one header line, n echo replies, and the
// statistics block a reader actually wants.
func buildPing(echoes int) string {
	var b strings.Builder
	b.WriteString("PING example.com (93.184.216.34): 56 data bytes\n")
	for range echoes {
		b.WriteString("64 bytes from 93.184.216.34: icmp_seq=1 ttl=56 time=11.2 ms\n")
	}
	b.WriteString("\n--- example.com ping statistics ---\n")
	b.WriteString("3 packets transmitted, 3 packets received, 0.0% packet loss\n")
	b.WriteString("round-trip min/avg/max/mdev = 11.108/11.208/11.292/0.090 ms\n")
	return b.String()
}

// TestPingFilter_MatchesCommand is the wiring proof: a ping command resolves to
// the ping filter, so the catalog owns this output rather than a second
// hand-rolled compressor.
func TestPingFilter_MatchesCommand(t *testing.T) {
	f := MatchFilter(buildPing(200), "ping -c 3 example.com", DefaultRTKConfig())
	if f == nil || f.ID != "ping" {
		t.Fatalf("MatchFilter = %v, want the ping filter", f)
	}
	if got := NormalizeFilterCategory(f.Category); got != "docker" {
		t.Errorf("category normalizes to %q, want docker (cloud → docker)", got)
	}
}

// TestPingFilter_ContentMatchWithoutCommand is the path the request body
// actually takes: tool output carries no command, so the filter must claim the
// body from its patterns alone. A body that merely mentions a transcript must
// not be claimed, and the patterns are anchored so it is not.
func TestPingFilter_ContentMatchWithoutCommand(t *testing.T) {
	cfg := DefaultRTKConfig()
	if f := MatchFilter(buildPing(200), "", cfg); f == nil || f.ID != "ping" {
		t.Fatalf("content-only MatchFilter = %v, want the ping filter", f)
	}
	if f := MatchFilter("the user asked me to run a ping and it said 100% packet loss", "", cfg); f != nil && f.ID == "ping" {
		t.Error("prose that merely mentions packet loss must not be claimed by the ping filter")
	}
}

// TestPingFilter_FoldsEchoesKeepsStatistics runs the filter the way the
// pipeline does and proves the outcome the filter exists for.
func TestPingFilter_FoldsEchoesKeepsStatistics(t *testing.T) {
	cfg := DefaultRTKConfig()
	f := MatchFilter(buildPing(200), "ping -c 3 example.com", cfg)
	if f == nil {
		t.Fatal("no ping filter matched")
	}
	maxLines, _ := resolveLimits(cfg)
	out, techniques := ApplyRTKFilter(f, strings.TrimSpace(buildPing(200)), maxLines)
	if out == strings.TrimSpace(buildPing(200)) {
		t.Fatal("the filter did not change a 200-echo ping run")
	}
	if len(techniques) == 0 || techniques[0] != "ping" {
		t.Errorf("techniques = %v, want to lead with the filter id", techniques)
	}
	for _, keep := range []string{
		"PING example.com (93.184.216.34): 56 data bytes",
		"--- example.com ping statistics ---",
		"3 packets transmitted, 3 packets received, 0.0% packet loss",
		"round-trip min/avg/max/mdev = 11.108/11.208/11.292/0.090 ms",
	} {
		if !strings.Contains(out, keep) {
			t.Errorf("fold dropped a line the reader needs: %q", keep)
		}
	}
	if n := strings.Count(out, "icmp_seq="); n > 20 {
		t.Errorf("echoes were not folded, %d remain", n)
	}
}

// TestPingFilter_KeepsFailure is the preserve contract: an unreachable host is
// the one ping result an operator must never have folded away.
func TestPingFilter_KeepsFailure(t *testing.T) {
	cfg := DefaultRTKConfig()
	f := MatchFilter(pingUnreachable, "ping -c 1 nosuchhost.invalid", cfg)
	if f == nil {
		t.Fatal("no ping filter matched")
	}
	maxLines, _ := resolveLimits(cfg)
	out, _ := ApplyRTKFilter(f, strings.TrimSpace(pingUnreachable), maxLines)
	for _, keep := range []string{"100% packet loss", "ping statistics"} {
		if !strings.Contains(out, keep) {
			t.Errorf("failure detail %q was dropped:\n%s", keep, out)
		}
	}
}

const pingUnreachable = "PING nosuchhost.invalid (1.2.3.4): 56 data bytes\n" +
	"Request timeout for icmp_seq 0\n" +
	"\n--- nosuchhost.invalid ping statistics ---\n" +
	"1 packets transmitted, 0 packets received, 100% packet loss\n"
