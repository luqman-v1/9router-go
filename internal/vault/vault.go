// Package vault implements envelope encryption for provider credentials.
//
// A random data-encryption key (DEK) encrypts the secret; the master key —
// a KEK — encrypts the DEK. Rotating the master key therefore only re-wraps
// the (small) wrapped-DEK column: no secret is decrypted and re-encrypted, and
// ciphertext stays byte-identical.
//
// # Losing the master key
//
// Losing ROUTER_MASTER_KEY with no usable fallback makes every sealed
// credential permanently unrecoverable. That is inherent to envelope
// encryption without an external escrow, and the design keeps it recoverable
// in the only case a local operator can actually fix:
//
//   - ROUTER_MASTER_KEY_PREVIOUS holds the retired key. Open accepts a DEK
//     wrapped under either key, so a rotation can be reverted by moving the
//     new key back into ROUTER_MASTER_KEY and the old one back into
//     ROUTER_MASTER_KEY_PREVIOUS.
//   - Rows are never rewritten by a failed decrypt: a hydration pass leaves
//     that row's sealed columns and its plaintext data untouched and records
//     the failure, so restoring the right key recovers them with no data loss.
//
// There is no recovery path when the key itself is gone. Back ROUTER_MASTER_KEY
// up the same way as the database file it protects.
package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"

	"9router/proxy/internal/log"
)

// MasterKeyEnv names the base64 32-byte key-encryption key.
// PreviousMasterKeyEnv holds the retired key so a rotation stays reversible.
const (
	MasterKeyEnv         = "ROUTER_MASTER_KEY"
	PreviousMasterKeyEnv = "ROUTER_MASTER_KEY_PREVIOUS"
)

// KeySize is the required master-key and DEK length: AES-256.
const KeySize = 32

// Errors callers branch on. Everything else is wrapped with an op prefix.
var (
	// ErrDisabled is returned by Seal when no master key is configured. The
	// install then keeps writing plaintext rather than refusing to boot.
	ErrDisabled = errors.New("vault: no master key configured")
	// ErrTampered is returned by Open when GCM authentication fails: a
	// tampered ciphertext, a DEK wrapped under a different master key, or a
	// wrong master key.
	ErrTampered = errors.New("vault: authentication failed")
	// errNoMasterKey separates "not configured" (normal, degraded) from
	// "configured but broken" (a mistake worth reporting).
	errNoMasterKey = errors.New("no master key configured")
)

// Vault seals and opens credentials with envelope encryption. The zero value
// is not usable; build one with New, NewFromEnv or NewDisabled.
type Vault struct {
	current  []byte
	previous []byte
}

// New builds an enabled vault from a 32-byte master key.
func New(masterKey []byte) (*Vault, error) {
	if len(masterKey) != KeySize {
		return nil, fmt.Errorf("vault.New: master key must be %d bytes, got %d", KeySize, len(masterKey))
	}
	key := make([]byte, KeySize)
	copy(key, masterKey)
	return &Vault{current: key}, nil
}

// NewDisabled returns a vault that never seals. Reads pass through, so an
// install without a master key keeps working exactly as it did before.
func NewDisabled() *Vault { return &Vault{} }

// NewFromEnv builds a vault from ROUTER_MASTER_KEY (and optionally
// ROUTER_MASTER_KEY_PREVIOUS). It never fails: an absent, blank or malformed
// key degrades to a disabled vault plus a warning, because refusing to boot
// over an env var would take a working install offline.
func NewFromEnv() *Vault {
	key, err := masterKeyFromEnv(MasterKeyEnv)
	if err != nil {
		if errors.Is(err, errNoMasterKey) {
			log.Warn("vault", "no master key configured; provider credentials stay in plaintext",
				"env", MasterKeyEnv)
		} else {
			log.Warn("vault", "master key unusable; provider credentials stay in plaintext",
				"env", MasterKeyEnv, "error", err)
		}
		return NewDisabled()
	}
	v := &Vault{current: key}
	// The retired key is optional: without it a rotation still works, it just
	// stops being reversible until the operator restores the old value.
	prev, err := masterKeyFromEnv(PreviousMasterKeyEnv)
	switch {
	case err == nil:
		v.previous = prev
	case !errors.Is(err, errNoMasterKey):
		log.Warn("vault", "previous master key unusable; rotations are not reversible",
			"env", PreviousMasterKeyEnv, "error", err)
	}
	return v
}

// Enabled reports whether Seal actually encrypts.
func (v *Vault) Enabled() bool { return v != nil && len(v.current) == KeySize }

// Seal encrypts plaintext under a fresh random DEK and wraps that DEK under
// the master key. Both return values are base64 text; the ciphertext decodes
// to nonce || sealed. A fresh nonce and DEK per call make two Seals of the
// same plaintext differ.
func (v *Vault) Seal(plaintext string) (string, string, error) {
	if !v.Enabled() {
		return "", "", ErrDisabled
	}
	dek := make([]byte, KeySize)
	if _, err := rand.Read(dek); err != nil {
		return "", "", fmt.Errorf("vault.Seal: read DEK: %w", err)
	}
	sealed, err := sealWith(dek, []byte(plaintext))
	if err != nil {
		return "", "", fmt.Errorf("vault.Seal: %w", err)
	}
	wrapped, err := sealWith(v.current, dek)
	if err != nil {
		return "", "", fmt.Errorf("vault.Seal: wrap DEK: %w", err)
	}
	return base64.StdEncoding.EncodeToString(wrapped),
		base64.StdEncoding.EncodeToString(sealed), nil
}

// Open reverses Seal. It unwraps the DEK with the master key (falling back to
// the retired one), then decrypts the ciphertext.
//
// An empty wrappedDEK means the row was never sealed, so the ciphertext comes
// back unchanged. That is what keeps the read path a single code path: the
// caller cannot tell a sealed row from a plaintext one without trying.
func (v *Vault) Open(wrappedDEK, ciphertext string) (string, error) {
	if wrappedDEK == "" {
		return ciphertext, nil
	}
	// A vault carrying only the retired key can still read: that is the state
	// of a process that booted before the new key was exported.
	if v == nil || (len(v.current) != KeySize && len(v.previous) != KeySize) {
		return "", ErrDisabled
	}
	dek, err := v.unwrapDEK(wrappedDEK)
	if err != nil {
		return "", err
	}
	raw, err := decodeBase64(ciphertext)
	if err != nil {
		return "", fmt.Errorf("vault.Open: %w", err)
	}
	plain, err := openWith(dek, raw)
	if err != nil {
		return "", fmt.Errorf("vault.Open: %w", err)
	}
	return string(plain), nil
}

// Rotate installs a new master key and retires the current one into the
// previous slot, so a DEK wrapped under the old key stays openable. Callers
// must re-wrap the stored DEKs separately; see Vault.Rewrap and
// db.Repo.RewrapConnectionDEKs.
func (v *Vault) Rotate(newMasterKey []byte) error {
	if !v.Enabled() {
		return ErrDisabled
	}
	if len(newMasterKey) != KeySize {
		return fmt.Errorf("vault.Rotate: master key must be %d bytes, got %d", KeySize, len(newMasterKey))
	}
	key := make([]byte, KeySize)
	copy(key, newMasterKey)
	v.previous = v.current
	v.current = key
	return nil
}

// Rewrap re-encrypts an already-wrapped DEK under newMasterKey. The ciphertext
// is untouched: rotating a KEK never re-encrypts a secret, which is the entire
// reason this is envelope encryption rather than one-key-at-rest encryption.
func (v *Vault) Rewrap(wrappedDEK string, newMasterKey []byte) (string, error) {
	if !v.Enabled() {
		return "", ErrDisabled
	}
	if len(newMasterKey) != KeySize {
		return "", fmt.Errorf("vault.Rewrap: master key must be %d bytes, got %d", KeySize, len(newMasterKey))
	}
	dek, err := v.unwrapDEK(wrappedDEK)
	if err != nil {
		return "", err
	}
	wrapped, err := sealWith(newMasterKey, dek)
	if err != nil {
		return "", fmt.Errorf("vault.Rewrap: %w", err)
	}
	return base64.StdEncoding.EncodeToString(wrapped), nil
}

// unwrapDEK opens the wrapped DEK with the current master key, then the
// retired one. A row sealed under an older key stays readable across a
// rotation without a database rewrite.
func (v *Vault) unwrapDEK(wrappedDEK string) ([]byte, error) {
	raw, err := decodeBase64(wrappedDEK)
	if err != nil {
		return nil, fmt.Errorf("vault.unwrapDEK: %w", err)
	}
	dek, err := openWith(v.current, raw)
	if err == nil {
		return dek, nil
	}
	if len(v.previous) == KeySize {
		if dek, prevErr := openWith(v.previous, raw); prevErr == nil {
			return dek, nil
		}
	}
	return nil, fmt.Errorf("vault.unwrapDEK: %w", ErrTampered)
}

// sealWith AES-256-GCM encrypts plaintext under key, returning nonce || sealed.
func sealWith(key, plaintext []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("read nonce: %w", err)
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// openWith reverses sealWith. A short or tampered input yields ErrTampered.
func openWith(key, sealed []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, ErrTampered
	}
	plain, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], nil)
	if err != nil {
		return nil, ErrTampered
	}
	return plain, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("new GCM: %w", err)
	}
	return gcm, nil
}

func decodeBase64(s string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("decode base64: %w", err)
	}
	return raw, nil
}

// DecodeMasterKey decodes a base64 32-byte master key supplied by an operator,
// for the rotation endpoint. It applies the same length rule as the
// environment path so a key that would be rejected at boot is rejected here
// too, instead of sealing every future credential under an unusable key.
func DecodeMasterKey(encoded string) ([]byte, error) {
	raw := strings.TrimSpace(encoded)
	if raw == "" {
		return nil, errNoMasterKey
	}
	key, err := decodeBase64(raw)
	if err != nil {
		return nil, fmt.Errorf("masterKey: %w", err)
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("masterKey must decode to %d bytes, got %d", KeySize, len(key))
	}
	return key, nil
}

// masterKeyFromEnv decodes one base64 32-byte key out of the environment. A
// blank or absent variable is errNoMasterKey, not a decode failure.
func masterKeyFromEnv(name string) ([]byte, error) {
	raw, ok := lookupEnv(name)
	if !ok {
		return nil, errNoMasterKey
	}
	key, err := decodeBase64(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("%s must decode to %d bytes, got %d", name, KeySize, len(key))
	}
	return key, nil
}

// lookupEnv reads an env var, treating blank as unset so an exported-but-empty
// variable degrades the same as an absent one.
func lookupEnv(name string) (string, bool) {
	raw := strings.TrimSpace(os.Getenv(name))
	return raw, raw != ""
}
