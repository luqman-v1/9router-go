package browsercookies

import (
	"context"
	"database/sql"
	"time"
)

// Result is what a read of a browser's cookies found.
type Result struct {
	// Header is the session in the canonical wire order.
	Header string

	// Cookies are the rows that were read, one per cookie.
	Cookies []Cookie

	// Decrypted is how many cookies were unsealed.
	Decrypted int

	// Plain is how many cookies were stored unencrypted.
	Plain int

	// Failed is how many cookies were sealed and could not be unsealed.
	Failed int

	// KeyFound reports whether the encryption key was found at all.
	KeyFound bool

	// Expired names the long-lived session cookies that are already expired
	// or expire within the given window.
	Expired []string
}

// Cookie is one row of a browser's cookie database.
type Cookie struct {
	Host      string
	Name      string
	Value     string
	ExpiresAt int64
}

// longLivedSessionCookies are the cookies that say a profile was really
// signed in.
//
// The short-lived rotating ones are left out on purpose: `__Secure-1PSIDTS`
// is rotated every few minutes, so treating a rotation as a sign of a dead
// session would cry wolf on every read.
var longLivedSessionCookies = []string{
	"SID", "HSID", "SSID", "APISID", "SAPISID",
	"__Secure-1PSID", "__Secure-3PSID",
}

// ExpiringSessionCookies names the long-lived session cookies that are
// expired, or expire within the window.
//
// A profile that is not signed in to Gemini Web carries cookies that expire
// almost at once, and that is worth saying out loud rather than letting the
// operator discover it as a session that dies.
func ExpiringSessionCookies(result *Result, within time.Duration) []string {
	if result == nil {
		return nil
	}
	wanted := make(map[string]bool, len(longLivedSessionCookies))
	for _, name := range longLivedSessionCookies {
		wanted[name] = true
	}
	cutoff := time.Now().Add(within).Unix()
	var out []string
	for _, cookie := range result.Cookies {
		if !wanted[cookie.Name] || cookie.ExpiresAt <= 0 {
			continue
		}
		if cookie.ExpiresAt <= cutoff {
			out = append(out, cookie.Name)
		}
	}
	return out
}

// chromiumRow is one row of the cookie table, still sealed.
type chromiumRow struct {
	host        string
	name        string
	encrypted   []byte
	plain       string
	expiresUTC  int64
	expiresUnix int64
}

// readChromiumRows reads the cookies of one domain out of an open database.
//
// The domain is a LIKE pattern rather than an exact host, because Chromium
// stores a cookie for `.example.com` and for `www.example.com` alike, and
// the session cookie the caller is after is on one of them and not the
// other.
func readChromiumRows(ctx context.Context, db *sql.DB, domainPattern string) ([]chromiumRow, error) {
	rows, err := db.QueryContext(ctx, `SELECT host_key, name, encrypted_value, value, COALESCE(has_expires, 0), COALESCE(expires_utc, 0)
		FROM cookies WHERE host_key LIKE ?`, "%"+domainPattern+"%")
	if err != nil {
		// An older table has no expiry columns.
		rows, err = db.QueryContext(ctx, `SELECT host_key, name, encrypted_value, value FROM cookies WHERE host_key LIKE ?`, "%"+domainPattern+"%")
		if err != nil {
			return nil, err
		}
	}
	defer func() { _ = rows.Close() }()

	now := time.Now().Unix()
	var out []chromiumRow
	for rows.Next() {
		var row chromiumRow
		var hasExpires int64
		if err := rows.Scan(&row.host, &row.name, &row.encrypted, &row.plain, &hasExpires, &row.expiresUTC); err != nil {
			continue
		}
		if hasExpires != 0 && row.expiresUTC > 0 {
			unixSec := (row.expiresUTC / 1_000_000) - chromiumEpochOffsetSec
			if unixSec <= now {
				continue
			}
			row.expiresUnix = unixSec
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
