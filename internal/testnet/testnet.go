// Package testnet seals the test process against outbound network access.
//
// P0 Step 4 of the program plan requires the suite to be safe with outbound
// blocked, and before this package that requirement was enforced by asking the
// person running the tests to export proxy variables. Nothing caught a test
// that quietly called the internet: TestHandleChangelog passed for months
// because the machine it ran on had egress, and only failed once the whole
// suite ran behind a dead proxy.
//
// Seal replaces the default transport's dialer. A dial to a loopback or
// unspecified address — what httptest servers listen on — proceeds normally;
// anything else fails with the same connection error an unreachable host gives,
// which is the failure a test would already have to handle. The four proxy
// environment variables come along too, so a machine that has them set does not
// route a test's httptest traffic somewhere real.
package testnet

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"syscall"
)

// dialer is the loopback-only replacement for the transport dialer.
var dialer = &net.Dialer{
	Control: func(network, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("testnet: refusing %q: %w", address, err)
		}
		ip := net.ParseIP(host)
		if ip == nil || (!ip.IsLoopback() && !ip.IsUnspecified()) {
			return fmt.Errorf("testnet: outbound blocked to %s", address)
		}
		return nil
	},
}

// Seal installs the guard. Call it from TestMain before m.Run().
//
// It is deliberately not reversible: a test that wants to talk to a fake
// upstream already has https/httptest for it, and a test that wants real
// egress is a test that belongs behind an explicit opt-in, not behind
// "unseal".
func Seal() {
	http.DefaultTransport.(*http.Transport).DialContext = dialer.DialContext
	// Proxy variables from the ambient environment would defeat the dialer:
	// the transport would dial the proxy's address, which is not loopback.
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
		"http_proxy", "https_proxy", "all_proxy", "no_proxy"} {
		_ = os.Unsetenv(key)
	}
}
