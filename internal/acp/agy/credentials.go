package agy

import (
	"crypto/rand"
	"encoding/hex"
	encodingjson "encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Credentials is an OAuth token pair for one account. Tokens are shared with
// the HTTP antigravity provider by referencing its connection; this struct is
// the snapshot written into an isolated agy account directory at spawn time.
type Credentials struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

// AccountDir is the isolated HOME for one agy-acp account. agy reads its
// file-backed OAuth token from <dir>/.gemini/antigravity-cli/antigravity-oauth-token
// and its ACP auth method from <dir>/.gemini/antigravity-cli/cache/acp-auth-state.json.
type AccountDir struct {
	Root string // absolute isolated HOME directory
}

// NewAccountDir creates (or reuses) an isolated directory with 0700 perms.
// root should be unique per connection/account. Token files are written 0600.
func NewAccountDir(root string) (*AccountDir, error) {
	if root == "" {
		return nil, fmt.Errorf("empty account dir")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve account dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(abs, ".gemini", "antigravity-cli", "cache"), 0o700); err != nil {
		return nil, fmt.Errorf("create account dir: %w", err)
	}
	return &AccountDir{Root: abs}, nil
}

// WriteCredentials atomically writes agy's file-backed OAuth credential. The
// payload shape matches agy 1.5.9's FileTokenStorage (sidkang/agy-acp reference).
func (d *AccountDir) WriteCredentials(creds Credentials) error {
	if creds.AccessToken == "" || creds.RefreshToken == "" {
		return fmt.Errorf("incomplete credentials: access and refresh tokens are required")
	}
	appDataDir := filepath.Join(d.Root, ".gemini", "antigravity-cli")
	cacheDir := filepath.Join(appDataDir, "cache")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return fmt.Errorf("create credential cache dir: %w", err)
	}

	expiry := creds.ExpiresAt
	if expiry.IsZero() || expiry.Before(time.Now()) {
		expiry = time.Now().Add(time.Hour)
	}
	expiresIn := int(time.Until(expiry).Seconds())
	if expiresIn < 60 {
		expiresIn = 60
	}
	payload, err := encodingjson.Marshal(map[string]any{
		"token": map[string]any{
			"access_token":  creds.AccessToken,
			"token_type":    "Bearer",
			"refresh_token": creds.RefreshToken,
			"expiry":        expiry.UTC().Format(time.RFC3339),
			"expires_in":    expiresIn,
		},
		"auth_method": "consumer",
	})
	if err != nil {
		return fmt.Errorf("marshal credential payload: %w", err)
	}
	tokenFile := filepath.Join(appDataDir, "antigravity-oauth-token")
	if err := writeFileAtomic(tokenFile, payload, 0o600); err != nil {
		return fmt.Errorf("write credential: %w", err)
	}
	state, err := encodingjson.Marshal(map[string]string{"methodId": "oauth-personal"})
	if err != nil {
		return fmt.Errorf("marshal auth state: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(cacheDir, "acp-auth-state.json"), state, 0o600); err != nil {
		return fmt.Errorf("write auth state: %w", err)
	}
	return nil
}

// Clear removes credential material from the account dir once a turn ends.
// The directory itself is kept so repeated spawns can reuse the layout.
func (d *AccountDir) Clear() error {
	appDataDir := filepath.Join(d.Root, ".gemini", "antigravity-cli")
	for _, p := range []string{
		filepath.Join(appDataDir, "antigravity-oauth-token"),
		filepath.Join(appDataDir, "cache", "acp-auth-state.json"),
	} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("clear credential: %w", err)
		}
	}
	return nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + "." + randomHex(6) + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Chmod(path, mode)
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
