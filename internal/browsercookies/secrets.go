package browsercookies

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// The secret store.
//
// A Chromium browser derives its cookie key from a secret it keeps in the
// platform's secret store: the login keyring on Linux, the Keychain on
// macOS, and — on Windows — a key wrapped by DPAPI inside the profile's own
// "Local State" file, which is not a secret store at all but is opened under
// the user's own privilege.
//
// The reads are best-effort. A machine with no keyring, a user who declined
// a prompt, and a browser that keeps its key somewhere else all end the
// same way: no secret, and the cookies stay sealed.

// candidatePasswords lists the secrets the cookie key may be derived from.
//
// The order is the platform's own first, then the ones known to work on
// other setups, and an empty one last: an empty password is what Chromium
// uses when the keyring is not in the way.
func candidatePasswords(ctx context.Context, dbPath string) []string {
	var passes []string
	seen := make(map[string]bool)
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		passes = append(passes, p)
	}
	if envPass := os.Getenv("CHROME_PASS"); envPass != "" {
		add(envPass)
	} else {
		switch runtime.GOOS {
		case "darwin":
			for _, service := range keychainServices(dbPath) {
				add(keychainPassword(service))
			}
		case "windows":
			// The key is DPAPI-wrapped inside "Local State"; see
			// readLocalStateMasterKey.
		default:
			for _, app := range keyringApps {
				add(platformKeyringSecret(ctx, app))
			}
		}
	}
	// The password Chromium falls back to when the keyring holds nothing.
	add("peanuts")
	passes = append(passes, "")
	return passes
}

// keychainServices are the macOS Keychain services worth trying for a
// database.
//
// A recognised profile only asks its own browser; an unrecognised path falls
// back to every known fork, because there is nothing else to go on.
func keychainServices(dbPath string) []string {
	if browser := browserFromDBPath(dbPath); browser != "" {
		if service, ok := macSafeStorageServices[browser]; ok {
			return []string{service}
		}
	}
	out := make([]string, 0, len(knownBrowsers))
	for _, browser := range knownBrowsers {
		if service, ok := macSafeStorageServices[browser]; ok {
			out = append(out, service)
		}
	}
	return out
}

// keychainPassword reads a macOS Keychain generic password.
//
// An empty result means the item is absent, or the user declined the access
// prompt. Both are the same thing as far as the caller is concerned.
func keychainPassword(service string) string {
	out, err := runCommand("security", "find-generic-password", "-s", service, "-w")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// runCommand runs a command and answers with its output.
//
// It is a variable so a test can stand in for the platform's tools, which are
// not on a build machine.
var runCommand = func(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}
