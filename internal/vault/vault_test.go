package vault

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func testKey(b byte) []byte {
	key := make([]byte, KeySize)
	for i := range key {
		key[i] = b
	}
	return key
}

func newTestVault(t *testing.T) *Vault {
	t.Helper()
	v, err := New(testKey(1))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return v
}

func TestNewRejectsWrongKeySize(t *testing.T) {
	for _, size := range []int{0, 16, 31, 33, 64} {
		if _, err := New(make([]byte, size)); err == nil {
			t.Errorf("New(%d bytes) accepted a key that is not AES-256", size)
		}
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	v := newTestVault(t)
	const secret = "sk-live-super-secret-value"

	wrapped, ciphertext, err := v.Seal(secret)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if wrapped == "" || ciphertext == "" {
		t.Fatal("Seal returned an empty wrapped DEK or ciphertext")
	}
	if strings.Contains(ciphertext, secret) {
		t.Error("ciphertext leaks the plaintext")
	}

	got, err := v.Open(wrapped, ciphertext)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got != secret {
		t.Errorf("Open = %q, want %q", got, secret)
	}
}

func TestSealIsNonDeterministic(t *testing.T) {
	v := newTestVault(t)
	const secret = "same-input"

	w1, c1, err := v.Seal(secret)
	if err != nil {
		t.Fatalf("Seal 1: %v", err)
	}
	w2, c2, err := v.Seal(secret)
	if err != nil {
		t.Fatalf("Seal 2: %v", err)
	}
	if c1 == c2 {
		t.Error("two Seals of the same plaintext produced identical ciphertext")
	}
	if w1 == w2 {
		t.Error("two Seals reused the same DEK")
	}
	for i, p := range []struct{ w, c string }{{w1, c1}, {w2, c2}} {
		got, err := v.Open(p.w, p.c)
		if err != nil {
			t.Fatalf("Open %d: %v", i, err)
		}
		if got != secret {
			t.Errorf("Open %d = %q, want %q", i, got, secret)
		}
	}
}

func TestOpenRejectsTamperedCiphertext(t *testing.T) {
	v := newTestVault(t)
	wrapped, ciphertext, err := v.Seal("secret")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	raw, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	raw[len(raw)-1] ^= 0xff
	tampered := base64.StdEncoding.EncodeToString(raw)

	if _, err = v.Open(wrapped, tampered); !errors.Is(err, ErrTampered) {
		t.Errorf("Open(tampered) error = %v, want ErrTampered", err)
	}
}

func TestOpenRejectsForeignMasterKey(t *testing.T) {
	owner := newTestVault(t)
	stranger, err := New(testKey(9))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	wrapped, ciphertext, err := owner.Seal("secret")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err = stranger.Open(wrapped, ciphertext); !errors.Is(err, ErrTampered) {
		t.Errorf("Open with a foreign key error = %v, want ErrTampered", err)
	}
}

func TestOpenPassesThroughWhenNotSealed(t *testing.T) {
	v := newTestVault(t)
	const plaintext = "sk-stored-in-the-clear"
	got, err := v.Open("", plaintext)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got != plaintext {
		t.Errorf("Open(\"\", %q) = %q, want the ciphertext unchanged", plaintext, got)
	}
}

func TestDisabledVault(t *testing.T) {
	v := NewDisabled()
	if v.Enabled() {
		t.Fatal("NewDisabled reports itself as enabled")
	}
	if _, _, err := v.Seal("secret"); !errors.Is(err, ErrDisabled) {
		t.Errorf("Seal on a disabled vault = %v, want ErrDisabled", err)
	}
	// A disabled vault still reads plaintext rows so the path stays single.
	got, err := v.Open("", "plain")
	if err != nil || got != "plain" {
		t.Errorf("Open on a disabled vault = (%q, %v), want (%q, nil)", got, err, "plain")
	}
	if err = v.Rotate(testKey(2)); !errors.Is(err, ErrDisabled) {
		t.Errorf("Rotate on a disabled vault = %v, want ErrDisabled", err)
	}
}

func TestRotateKeepsOldCiphertextReadable(t *testing.T) {
	v := newTestVault(t)
	wrapped, ciphertext, err := v.Seal("secret")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	if err = v.Rotate(testKey(2)); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	got, err := v.Open(wrapped, ciphertext)
	if err != nil {
		t.Fatalf("Open after rotate: %v", err)
	}
	if got != "secret" {
		t.Errorf("Open after rotate = %q, want the original secret", got)
	}
	// A freshly sealed row is no longer openable by the retired key alone.
	w2, c2, err := v.Seal("second")
	if err != nil {
		t.Fatalf("Seal after rotate: %v", err)
	}
	oldOnly := &Vault{previous: v.previous}
	if _, err = oldOnly.Open(w2, c2); !errors.Is(err, ErrTampered) {
		t.Errorf("retired key opened a post-rotation row: %v", err)
	}
}

func TestRewrapOnlyTouchesTheDEK(t *testing.T) {
	v := newTestVault(t)
	wrapped, ciphertext, err := v.Seal("secret")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	newKey := testKey(7)
	rewrapped, err := v.Rewrap(wrapped, newKey)
	if err != nil {
		t.Fatalf("Rewrap: %v", err)
	}
	if rewrapped == wrapped {
		t.Error("Rewrap returned the same wrapped DEK")
	}

	// The new key alone opens the row: the ciphertext was never re-encrypted.
	fresh := &Vault{current: newKey}
	got, err := fresh.Open(rewrapped, ciphertext)
	if err != nil {
		t.Fatalf("Open with the new key: %v", err)
	}
	if got != "secret" {
		t.Errorf("Open = %q, want the original secret", got)
	}
	if _, err = fresh.Rewrap(rewrapped, []byte("short")); err == nil {
		t.Error("Rewrap accepted a key that is not AES-256")
	}
}

func TestNewFromEnv(t *testing.T) {
	random := make([]byte, KeySize)
	if _, err := rand.Read(random); err != nil {
		t.Fatalf("rand: %v", err)
	}
	encoded := base64.StdEncoding.EncodeToString(random)

	tests := []struct {
		name        string
		master      string
		previous    string
		wantEnabled bool
	}{
		{"absent degrades to plaintext", "", "", false},
		{"blank degrades to plaintext", "   ", "", false},
		{"short key degrades to plaintext", base64.StdEncoding.EncodeToString(random[:8]), "", false},
		{"non base64 degrades to plaintext", "not base64!!", "", false},
		{"valid key enables sealing", encoded, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(MasterKeyEnv, tt.master)
			t.Setenv(PreviousMasterKeyEnv, tt.previous)
			if got := NewFromEnv().Enabled(); got != tt.wantEnabled {
				t.Errorf("Enabled = %v, want %v", got, tt.wantEnabled)
			}
		})
	}
}

func TestNewFromEnvUsesPreviousKeyForReads(t *testing.T) {
	old := testKey(3)
	v, err := New(old)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	wrapped, ciphertext, err := v.Seal("secret")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	t.Setenv(MasterKeyEnv, base64.StdEncoding.EncodeToString(testKey(4)))
	t.Setenv(PreviousMasterKeyEnv, base64.StdEncoding.EncodeToString(old))
	got, err := NewFromEnv().Open(wrapped, ciphertext)
	if err != nil {
		t.Fatalf("Open with the retired key in %s: %v", PreviousMasterKeyEnv, err)
	}
	if got != "secret" {
		t.Errorf("Open = %q, want the original secret", got)
	}
}

func TestOpenRejectsMalformedBase64(t *testing.T) {
	v := newTestVault(t)
	if _, err := v.Open("!!!not base64!!!", "also not base64"); err == nil {
		t.Error("Open accepted a wrapped DEK that is not base64")
	}
	if _, err := v.Open("aGVsbG8=", "!!!not base64!!!"); err == nil {
		t.Error("Open accepted a ciphertext that is not base64")
	}
}
