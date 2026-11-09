//go:build windows || darwin

package browsercookies

import "context"

// platformKeyringSecret reads a secret out of the platform's secret store.
//
// On macOS the secret is read from the Keychain by the command that asks for
// it, because the Keychain's own prompt is part of the read. On Windows the
// key is not in a secret store at all: it is wrapped by DPAPI inside the
// profile's "Local State" file, and is opened there instead.
func platformKeyringSecret(_ context.Context, _ string) string { return "" }
