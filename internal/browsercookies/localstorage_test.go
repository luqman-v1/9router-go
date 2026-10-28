package browsercookies

import (
	"os"
	"path/filepath"
	"testing"
)

// TestProfileFromCookieDBFindsTheLocalStorageProfile pins the directory
// derivation against a real Chrome layout.
//
// Current Chrome keeps the cookie database in the profile's Network/
// subdirectory, while localStorage lives in Local Storage/leveldb beside
// it. Deriving the profile with a plain filepath.Dir lands on Network/,
// and the localStorage read then finds nothing on every current Chrome.
func TestProfileFromCookieDBFindsTheLocalStorageProfile(t *testing.T) {
	root := t.TempDir()
	profile := filepath.Join(root, "User Data", "Default")
	netDir := filepath.Join(profile, "Network")
	lsDir := filepath.Join(profile, "Local Storage", "leveldb")
	for _, dir := range []string{netDir, lsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	cookieDB := filepath.Join(netDir, "Cookies")
	if err := os.WriteFile(cookieDB, []byte("SQLite format 3"), 0o644); err != nil {
		t.Fatal(err)
	}
	record := []byte(`https://chat.deepseek.com` + "\x00" + `userToken` + "\x01" + `{"value":"THE-REAL-TOKEN","__version":"0"}`)
	if err := os.WriteFile(filepath.Join(lsDir, "000005.log"), record, 0o644); err != nil {
		t.Fatal(err)
	}

	derived := ProfileFromCookieDB(cookieDB)
	if derived != profile {
		t.Errorf("ProfileFromCookieDB(%q) = %q, want the profile directory %q",
			cookieDB, derived, profile)
	}

	token, err := ReadLocalStorage(derived, "https://chat.deepseek.com", "userToken")
	if err != nil {
		t.Fatalf("ReadLocalStorage: %v", err)
	}
	if token != "THE-REAL-TOKEN" {
		t.Errorf("token = %q, want %q", token, "THE-REAL-TOKEN")
	}
}

// TestReadLocalStorageSignedOutProfileHasNoCredential pins the signed-out
// shape. The site stores {"value":null,…} on a page that is not signed in,
// and the reader must report "no credential" rather than the text of some
// other key — a credential invented out of a key name is a credential the
// caller will send upstream.
func TestReadLocalStorageSignedOutProfileHasNoCredential(t *testing.T) {
	root := t.TempDir()
	lsDir := filepath.Join(root, "Local Storage", "leveldb")
	if err := os.MkdirAll(lsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	record := []byte(`https://chat.deepseek.com` + "\x00" + `userToken` + "\x01" + `{"value":null,"__version":"0"}`)
	if err := os.WriteFile(filepath.Join(lsDir, "000005.log"), record, 0o644); err != nil {
		t.Fatal(err)
	}

	token, err := ReadLocalStorage(root, "https://chat.deepseek.com", "userToken")
	if err == nil {
		t.Errorf("a signed-out profile returned a credential %q and no error", token)
	}
	if token != "" {
		t.Errorf("a signed-out profile yielded a bogus credential %q", token)
	}
}
