//go:build !windows

package browsercookies

import "fmt"

// dpapiUnprotect opens a DPAPI blob.
//
// DPAPI is Windows' own protection for secrets, and it only opens under
// Windows. On the other platforms a browser's key is in the platform's secret
// store instead, and this is never reached.
func dpapiUnprotect(_ []byte) ([]byte, error) {
	return nil, fmt.Errorf("DPAPI is only available on Windows")
}
