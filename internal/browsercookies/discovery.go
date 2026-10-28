package browsercookies

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// browserUserDataDirs are the directories a browser's profiles live in.
//
// The order is the platform's own first and the others after, because a
// machine can have a browser installed for another platform's sake — a
// Windows Chrome in a home directory on Linux, say — and the platform it
// actually runs on is the one that is being used.
func browserUserDataDirs(browser string) []string {
	b := strings.ToLower(strings.TrimSpace(browser))
	home, _ := os.UserHomeDir()
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" && home != "" {
		localAppData = filepath.Join(home, "AppData", "Local")
	}
	appSupport := ""
	if home != "" {
		appSupport = filepath.Join(home, "Library", "Application Support")
	}
	cfgDir := ""
	if home != "" {
		cfgDir = filepath.Join(home, ".config")
	}

	var dirs []string
	switch b {
	case "chrome":
		dirs = []string{
			filepath.Join(localAppData, "Google", "Chrome", "User Data"),
			filepath.Join(appSupport, "Google", "Chrome"),
			filepath.Join(cfgDir, "google-chrome"),
			// The Flatpak layout, where the profile lives inside the
			// sandbox's own directory, under the browser's own name.
			filepath.Join(home, ".var", "app", "com.google.Chrome", "config", "google-chrome"),
			filepath.Join(home, ".var", "app", "com.google.Chrome", "config"),
		}
	case "chromium":
		dirs = []string{
			filepath.Join(localAppData, "Chromium", "User Data"),
			filepath.Join(appSupport, "Chromium"),
			filepath.Join(cfgDir, "chromium"),
			filepath.Join(home, ".var", "app", "org.chromium.Chromium", "config", "chromium"),
			filepath.Join(home, ".var", "app", "org.chromium.Chromium", "config"),
		}
	case "brave":
		dirs = []string{
			filepath.Join(localAppData, "BraveSoftware", "Brave-Browser", "User Data"),
			filepath.Join(appSupport, "BraveSoftware", "Brave-Browser"),
			filepath.Join(cfgDir, "BraveSoftware", "Brave-Browser"),
			filepath.Join(home, ".var", "app", "com.brave.Browser", "config", "brave-browser"),
			filepath.Join(home, ".var", "app", "com.brave.Browser", "config"),
		}
	case "edge":
		dirs = []string{
			filepath.Join(localAppData, "Microsoft", "Edge", "User Data"),
			filepath.Join(appSupport, "Microsoft Edge"),
			filepath.Join(cfgDir, "microsoft-edge"),
		}
	case "vivaldi":
		dirs = []string{
			filepath.Join(localAppData, "Vivaldi", "User Data"),
			filepath.Join(appSupport, "Vivaldi"),
			filepath.Join(cfgDir, "vivaldi"),
		}
	case "arc":
		dirs = []string{
			filepath.Join(localAppData, "Arc", "User Data"),
			filepath.Join(appSupport, "Arc", "User Data"),
		}
	}
	return dirs
}

// FindCookieDB locates a browser's cookie database.
//
// The hint is a browser name ("chrome"), a profile directory ("Profile 4"), a
// profile path, or the path of a database. An empty hint means the first
// browser on this machine that has one.
func FindCookieDB(hint string) (string, error) {
	hint = strings.TrimSpace(hint)
	if hint == "" {
		for _, browser := range knownBrowsers {
			path, err := findInBrowser(browser, "")
			if err == nil {
				return path, nil
			}
		}
		return "", fmt.Errorf("%w for any of %s", ErrNoDatabase, strings.Join(knownBrowsers, ", "))
	}

	lower := strings.ToLower(hint)
	if isKnownBrowser(lower) {
		return findInBrowser(lower, "")
	}
	// "chrome:Profile 4" names a browser and one of its profiles.
	if name, profile, found := strings.Cut(hint, ":"); found && isKnownBrowser(strings.ToLower(strings.TrimSpace(name))) {
		return findInBrowser(strings.ToLower(strings.TrimSpace(name)), strings.TrimSpace(profile))
	}
	// A path to a database, a profile directory, or a user data directory.
	if strings.ContainsAny(hint, `/\`) {
		if path, ok := resolveCookiePath(hint); ok {
			return path, nil
		}
		return "", fmt.Errorf("%w at %s", ErrNoDatabase, hint)
	}
	// A profile directory name under any known browser.
	for _, browser := range knownBrowsers {
		if path, err := findInBrowser(browser, hint); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("%w for %q", ErrNoDatabase, hint)
}

// findInBrowser looks for a cookie database inside one browser's profiles.
func findInBrowser(browser, profile string) (string, error) {
	dirs := browserUserDataDirs(browser)
	if len(dirs) == 0 {
		return "", fmt.Errorf("%w for %q", ErrNoDatabase, browser)
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		names := []string{"Default"}
		if profile != "" {
			names = []string{profile}
		}
		for _, name := range names {
			for _, candidate := range cookiePathCandidates(dir, name) {
				if _, err := os.Stat(candidate); err == nil {
					return candidate, nil
				}
			}
		}
	}
	return "", fmt.Errorf("%w for %q", ErrNoDatabase, browser)
}

// cookiePathCandidates are the places a profile's cookie database may sit.
//
// A Chromium profile keeps its cookies in "Network/Cookies", and older
// versions kept them in the profile directory itself.
func cookiePathCandidates(userDataDir, profile string) []string {
	return []string{
		filepath.Join(userDataDir, profile, "Network", "Cookies"),
		filepath.Join(userDataDir, profile, "Cookies"),
	}
}

// isKnownBrowser reports whether the name is a browser this package reads.
func isKnownBrowser(name string) bool {
	for _, b := range knownBrowsers {
		if b == name {
			return true
		}
	}
	return false
}

// resolveCookiePath turns a path into a cookie database, if it is one.
func resolveCookiePath(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	if info, err := os.Stat(raw); err == nil {
		if !info.IsDir() {
			return raw, true
		}
		for _, candidate := range cookiePathCandidates(raw, "Default") {
			if _, err := os.Stat(candidate); err == nil {
				return candidate, true
			}
		}
		return "", false
	}
	// A user data directory, with its "Default" profile inside it.
	for _, candidate := range cookiePathCandidates(raw, "Default") {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true
		}
	}
	return "", false
}
