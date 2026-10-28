package browsercookies

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadSealsAndUnsealsAWholeDatabase proves the whole read.
//
// A database is created with the shape a browser writes, a session cookie is
// sealed the way the browser seals it, and the read has to come back with
// that cookie in the header. The parts in between — the copy, the row
// reading, the key derivation, the unsealing, the ordering — are what this
// exercises, and none of them are proved by testing them one at a time.
func TestReadSealsAndUnsealsAWholeDatabase(t *testing.T) {
	const password = "a-password-only-this-suite-knows"
	key256 := pbkdf2Key(password, 32)

	dbPath := filepath.Join(t.TempDir(), "Cookies")
	sealed := sealValue(t, key256, "v11", "the-session-value")
	plain := []byte("a cookie stored without a seal")
	writeCookieDatabase(t, dbPath, []cookieRow{
		{host: ".google.com", name: "__Secure-1PSIDTS", encrypted: sealed},
		{host: ".google.com", name: "NID", plain: plain},
	})

	t.Setenv("CHROME_PASS", password)
	result, err := Read(t.Context(), dbPath)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if !strings.Contains(result.Header, "__Secure-1PSIDTS=the-session-value") {
		t.Errorf("the header carries no unsealed session: %q", result.Header)
	}
	if !strings.Contains(result.Header, "NID=a cookie stored without a seal") {
		t.Errorf("the header carries no plain cookie: %q", result.Header)
	}
	if result.Decrypted != 1 {
		t.Errorf("Decrypted = %d, want 1", result.Decrypted)
	}
	if result.Plain != 1 {
		t.Errorf("Plain = %d, want 1", result.Plain)
	}
	if !result.KeyFound {
		t.Error("KeyFound = false, want true")
	}
}

// TestReadReportsAKeyItCannotFind proves a sealed database is reported as
// sealed.
//
// A browser that uses App-Bound encryption keeps its key where the user
// cannot reach it, and the cookies stay sealed. The answer has to say that,
// because "no session" and "a session I cannot open" are different problems
// with different fixes.
func TestReadReportsAKeyItCannotFind(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "Cookies")
	sealed := sealValue(t, make([]byte, 32), "v11", "a value nobody can open")
	writeCookieDatabase(t, dbPath, []cookieRow{
		{host: ".google.com", name: "__Secure-1PSIDTS", encrypted: sealed},
	})

	t.Setenv("CHROME_PASS", "a-password-that-opens-nothing")
	result, err := Read(t.Context(), dbPath)
	if err == nil {
		t.Fatalf("Read() accepted a database whose key it could not find (header: %q)", result.Header)
	}
	if !errors.Is(err, ErrNoKey) {
		t.Errorf("Read() error = %v, want it to name the missing key", err)
	}
	if result == nil {
		t.Fatal("Read() answered with no result and no error")
	}
	if result.Failed != 1 {
		t.Errorf("Failed = %d, want the cookie that could not be opened to be counted", result.Failed)
	}
}

// TestReadReportsAnEmptyDatabase proves a browser with no Google cookies is
// reported as such.
//
// A browser that has never visited Google holds no session, and that is not
// the same thing as a browser whose cookies could not be read.
func TestReadReportsAnEmptyDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "Cookies")
	writeCookieDatabase(t, dbPath, nil)

	if _, err := Read(t.Context(), dbPath); err == nil {
		t.Error("Read() accepted a database with no Google cookies")
	}
}

// cookieRow is one row to write into a test database.
type cookieRow struct {
	host      string
	name      string
	encrypted []byte
	plain     []byte
}

// writeCookieDatabase writes a database with the shape a browser writes.
//
// The schema is the browser's own, so the reader is exercised against the
// columns it will really find.
func writeCookieDatabase(t *testing.T, path string, rows []cookieRow) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open the database: %v", err)
	}
	defer func() { _ = db.Close() }()

	statement := `CREATE TABLE cookies (
		host_key TEXT, name TEXT, value TEXT, encrypted_value BLOB,
		has_expires INTEGER DEFAULT 0, expires_utc INTEGER DEFAULT 0,
		is_persistent INTEGER DEFAULT 1)`
	if _, err := db.Exec(statement); err != nil {
		t.Fatalf("create the table: %v", err)
	}
	for _, row := range rows {
		insert := `INSERT INTO cookies (host_key, name, value, encrypted_value, has_expires, expires_utc)
			VALUES (?, ?, ?, ?, 0, 0)`
		if _, err := db.Exec(insert, row.host, row.name, string(row.plain), row.encrypted); err != nil {
			t.Fatalf("insert the row: %v", err)
		}
	}
}

// sealValue seals a value the way a browser seals a cookie.
//
// The scheme is the modern one: a version prefix, a nonce, and the value
// under AES-256-GCM.
func sealValue(t *testing.T, key []byte, version, value string) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatalf("nonce: %v", err)
	}
	sealed := gcm.Seal(append([]byte(version), nonce...), nonce, []byte(value), nil)
	if !bytes.HasPrefix(sealed, []byte(version)) {
		t.Fatal("the sealed value lost its version prefix")
	}
	return sealed
}
