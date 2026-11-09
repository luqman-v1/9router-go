// Package browsercookies reads a Google session out of a browser's own cookie
// database.
//
// A Google session is a set of cookies, and a browser keeps them in a SQLite
// database it encrypts with a key it holds elsewhere: in the login keyring on
// Linux, in the Keychain on macOS, wrapped by DPAPI in the profile's own
// "Local State" file on Windows. Reading a session out of a browser that is
// already signed in means opening that database and unwrapping that key.
//
// The shape of the work comes from azrinaldi/gemini-stealth-bridge
// internal/cookies/chrome_sqlite.go, which is the reference implementation of
// the same job. What this package does differently is stated where it differs.
//
// The database is copied before it is read. A browser writes to it while it
// runs, and reading a live SQLite file can see a half-written page.
package browsercookies

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNoDatabase means no cookie database was found for what was asked for.
var ErrNoDatabase = errors.New("no browser cookie database was found")

// ErrNoKey means the database was found but the key that encrypts it was not.
//
// The database is readable and the cookies are in it, but they are sealed.
// The usual reasons are a browser that uses App-Bound encryption (Chrome 127
// and later on Windows) and a machine where the keyring was never unlocked.
var ErrNoKey = errors.New("the browser's cookie encryption key could not be read")

// knownBrowsers are the Chromium forks whose cookie databases are read.
var knownBrowsers = []string{"chrome", "chromium", "brave", "edge", "vivaldi", "arc"}

// keyringApps are the application names the Linux keyring is asked under.
var keyringApps = []string{"chrome", "brave", "chromium", "microsoft-edge", "vivaldi", "opera", "arc"}

// macSafeStorageServices maps a browser to the macOS Keychain service that
// holds its cookie-encryption secret.
//
// Every Chromium fork stores the key under "<Product> Safe Storage", and
// reading it makes macOS ask the user to approve the item once.
var macSafeStorageServices = map[string]string{
	"chrome":   "Chrome Safe Storage",
	"chromium": "Chromium Safe Storage",
	"brave":    "Brave Safe Storage",
	"edge":     "Microsoft Edge Safe Storage",
	"vivaldi":  "Vivaldi Safe Storage",
	"opera":    "Opera Safe Storage",
	"arc":      "Arc Safe Storage",
}

// Read reads a browser's cookie database and answers with the session in it.
//
// The database is copied first, because a browser writes to it while it runs
// and a live read can see a half-written page.
func Read(ctx context.Context, dbPath string) (*Result, error) {
	return ReadDomain(ctx, dbPath, "google")
}

// ReadDomain reads a browser's cookie database and answers with the cookies
// of one domain in it.
//
// The domain matters because a browser holds the sessions of every site the
// operator uses, and only one of them is the credential the caller is
// after. Reading the whole jar and picking afterwards would put every
// session in the process's memory, which is more exposure than the task
// needs.
func ReadDomain(ctx context.Context, dbPath, domain string) (*Result, error) {
	tmpDir, err := os.MkdirTemp("", "9router-cookies-")
	if err != nil {
		return nil, fmt.Errorf("browsercookies.Read: make a work directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	tmpDB := filepath.Join(tmpDir, "Cookies.db")
	if err := copyFile(dbPath, tmpDB); err != nil {
		return nil, fmt.Errorf("browsercookies.Read: copy the cookie database: %w", err)
	}
	// The write-ahead log holds the rows that have not been folded into the
	// database yet. Without it a cookie written moments ago is missing.
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(dbPath + suffix); err == nil {
			_ = copyFile(dbPath+suffix, tmpDB+suffix)
		}
	}

	db, err := sql.Open("sqlite", tmpDB)
	if err != nil {
		return nil, fmt.Errorf("browsercookies.Read: open the copy: %w", err)
	}
	defer func() { _ = db.Close() }()

	rows, err := readChromiumRows(ctx, db, domain)
	if err != nil {
		return nil, fmt.Errorf("browsercookies.Read: read the rows: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: no %s cookies in %s", ErrNoDatabase, domain, dbPath)
	}

	key256, key128, keyFound := detectBestKey(ctx, rows, dbPath)
	result := &Result{KeyFound: keyFound}
	for _, row := range rows {
		value := row.plain
		if len(row.encrypted) > 0 {
			if !keyFound {
				result.Failed++
				continue
			}
			decrypted := decryptCookieBytes(row.encrypted, key256, key128)
			if decrypted == nil {
				result.Failed++
				continue
			}
			value = string(stripDomainPrefix(decrypted, row.host))
			result.Decrypted++
		} else {
			result.Plain++
		}
		result.Cookies = append(result.Cookies, Cookie{
			Host:      row.host,
			Name:      row.name,
			Value:     value,
			ExpiresAt: row.expiresUnix,
		})
	}
	result.Header = buildDomainHeader(result.Cookies, domain)
	result.Expired = ExpiringSessionCookies(result, 7*24*time.Hour)
	if result.Header == "" && result.Failed > 0 {
		return result, fmt.Errorf("%w: %d of the %d %s cookies in %s are sealed and the key that "+
			"opens them could not be read — the browser's keyring is locked or is not installed, or the "+
			"browser uses App-Bound encryption; sign in through a throwaway browser instead",
			ErrNoKey, result.Failed, result.Failed+result.Decrypted+result.Plain, domain, dbPath)
	}
	return result, nil
}

// buildDomainHeader renders the session cookies of one domain as a Cookie
// header.
//
// Each site names its own session cookies, and a profile holds the cookies
// of every site the operator uses. Reading a site's cookie jar and handing
// every cookie in it to the router would put other sites' credentials in
// the process, so only the names the site itself uses are taken.
func buildDomainHeader(cookies []Cookie, domain string) string {
	switch domain {
	case "google":
		return buildGoogleHeader(cookies)
	case "deepseek":
		return buildHeader(cookies, deepSeekSessionCookieSet, deepSeekSessionCookieNames)
	default:
		// An unknown site gets every cookie that was read: the caller asked
		// for that domain's cookies and nothing else was read.
		allowed := make(map[string]bool, len(cookies))
		order := make([]string, 0, len(cookies))
		for _, cookie := range cookies {
			if cookie.Value == "" {
				continue
			}
			if !allowed[cookie.Name] {
				allowed[cookie.Name] = true
				order = append(order, cookie.Name)
			}
		}
		return buildHeader(cookies, allowed, order)
	}
}

// chromiumEpochOffsetSec is the difference between the Chromium epoch
// (1601-01-01) and the Unix epoch, in seconds.
const chromiumEpochOffsetSec int64 = 11644473600

// localStateFile is the part of a profile's "Local State" file that matters.
type localStateFile struct {
	OSCrypt struct {
		EncryptedKey string `json:"encrypted_key"`
	} `json:"os_crypt"`
}

// readLocalStateMasterKey reads the master key out of a "Local State" file.
//
// On Windows the file holds the key wrapped by DPAPI, and unwrapping it is
// the user's own privilege — the same one that protects their profile.
func readLocalStateMasterKey(path string) ([]byte, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var state localStateFile
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, false
	}
	if state.OSCrypt.EncryptedKey == "" {
		return nil, false
	}
	raw, err := base64.StdEncoding.DecodeString(state.OSCrypt.EncryptedKey)
	if err != nil {
		return nil, false
	}
	if len(raw) <= 5 || string(raw[:5]) != "DPAPI" {
		return nil, false
	}
	key, err := dpapiUnprotect(raw[5:])
	if err != nil {
		return nil, false
	}
	return key, true
}

// findLocalStatePaths lists the "Local State" files that may hold the key.
func findLocalStatePaths(dbPath string) []string {
	var out []string
	seen := make(map[string]bool)
	addIfFile := func(path string) {
		if path == "" || seen[path] {
			return
		}
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			seen[path] = true
			out = append(out, path)
		}
	}
	if dbPath != "" {
		dir := filepath.Dir(dbPath)
		for range 4 {
			addIfFile(filepath.Join(dir, "Local State"))
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	for _, browser := range knownBrowsers {
		for _, dir := range browserUserDataDirs(browser) {
			addIfFile(filepath.Join(dir, "Local State"))
		}
	}
	return out
}

// browserFromDBPath infers which browser owns a cookie database from its path.
//
// It is used to pick the one Keychain service to ask on macOS, so the user is
// prompted for one item rather than one per browser on the machine.
func browserFromDBPath(dbPath string) string {
	s := strings.ReplaceAll(strings.ToLower(dbPath), `\`, "/")
	s = "/" + strings.TrimPrefix(s, "/")
	has := func(marker string) bool { return strings.Contains(s, marker) }
	switch {
	case has("/thebrowsercompany.arc_"), has("/application support/arc/"), has("/local/arc/"):
		return "arc"
	case has("/bravesoftware/"), has("/brave-browser/"):
		return "brave"
	case has("/microsoft edge/"), has("/microsoft-edge/"), has("/microsoft/edge/"):
		return "edge"
	case has("/vivaldi/"):
		return "vivaldi"
	case has("/opera software/"), has("/opera/"):
		return "opera"
	case has("/chromium/"):
		return "chromium"
	case has("/google/chrome/"), has("/google-chrome/"):
		return "chrome"
	}
	return ""
}

// copyFile copies a file, creating the destination if it is not there.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	_, err = ioCopy(out, in)
	return err
}

// DetectProfileLock reports a browser lock in the profile a database is in.
//
// A lock is the browser saying it is running with that profile. It matters
// because a browser that is running and a router that renews the same session
// rotate it from two sides, and the two cancel each other out — which signs
// the account out.
//
// The check is read-only and advisory.
func DetectProfileLock(dbPath string) string {
	if strings.TrimSpace(dbPath) == "" {
		return ""
	}
	profileDir := filepath.Dir(dbPath)
	if strings.EqualFold(filepath.Base(profileDir), "Network") {
		profileDir = filepath.Dir(profileDir)
	}
	userDataDir := filepath.Dir(profileDir)
	for _, dir := range []string{profileDir, userDataDir} {
		if dir == "" || dir == "." || dir == "/" {
			continue
		}
		for _, name := range []string{"SingletonLock", "lockfile", "parent.lock", ".parentlock"} {
			candidate := filepath.Join(dir, name)
			if _, err := os.Lstat(candidate); err == nil {
				return candidate
			}
		}
	}
	return ""
}
