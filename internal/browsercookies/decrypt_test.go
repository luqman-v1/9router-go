package browsercookies

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"testing"
)

// TestDecryptCookieBytesOpensAGCMValue proves the modern scheme is opened.
//
// The value is sealed the way a browser seals it — AES-256-GCM under a key
// derived from the keyring's secret — and has to come back as it went in.
func TestDecryptCookieBytesOpensAGCMValue(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("read the key: %v", err)
	}
	plain := []byte("the cookie value")

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
	sealed := append([]byte("v11"), nonce...)
	sealed = gcm.Seal(sealed, nonce, plain, nil)

	got := decryptCookieBytes(sealed, key, nil)
	if !bytes.Equal(got, plain) {
		t.Errorf("decryptCookieBytes() = %q, want %q", got, plain)
	}
}

// TestDecryptCookieBytesOpensACBCValue proves the older scheme is opened.
//
// A profile created years ago and never re-encrypted keeps rows in the older
// scheme, and a reader that only opens the newer one leaves the session
// half-read.
func TestDecryptCookieBytesOpensACBCValue(t *testing.T) {
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("read the key: %v", err)
	}
	plain := []byte("an older cookie value")

	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append(append([]byte{}, plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	sealed := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, bytes.Repeat([]byte{0x20}, aes.BlockSize)).CryptBlocks(sealed, padded)
	sealed = append([]byte("v10"), sealed...)

	got := decryptCookieBytes(sealed, nil, key)
	if !bytes.Equal(got, plain) {
		t.Errorf("decryptCookieBytes() = %q, want %q", got, plain)
	}
}

// TestDecryptCookieBytesRefusesTheWrongKey proves a wrong key is refused.
//
// A wrong key produces bytes that are not the cookie, and handing them back
// would be handing back a credential that is simply wrong.
func TestDecryptCookieBytesRefusesTheWrongKey(t *testing.T) {
	key := make([]byte, 32)
	other := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("read the key: %v", err)
	}
	if _, err := rand.Read(other); err != nil {
		t.Fatalf("read the other key: %v", err)
	}
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
	sealed := gcm.Seal(append([]byte("v11"), nonce...), nonce, []byte("secret"), nil)

	if got := decryptCookieBytes(sealed, other, nil); got != nil {
		t.Errorf("decryptCookieBytes() = %q for the wrong key, want nothing", got)
	}
}

// TestStripDomainPrefixRemovesTheHostBinding pins the newer browser's
// binding.
//
// Chrome 130 and later put the SHA-256 of the host in front of the value, so
// that it cannot be moved to another host. The prefix has to come off, or
// the value that is sent is not the cookie.
func TestStripDomainPrefixRemovesTheHostBinding(t *testing.T) {
	host := ".google.com"
	sum := sha256.Sum256([]byte(host))
	value := []byte("the value")

	if got := stripDomainPrefix(append(append([]byte{}, sum[:]...), value...), host); !bytes.Equal(got, value) {
		t.Errorf("stripDomainPrefix() = %q, want %q", got, value)
	}
}

// TestStripDomainPrefixKeepsAValueWithoutTheBinding proves a browser that
// does not add it still reads.
//
// The binding was added in Chrome 130; an older browser has no prefix, and
// stripping 32 bytes off it would cut the value in half.
func TestStripDomainPrefixKeepsAValueWithoutTheBinding(t *testing.T) {
	value := []byte("a value from an older browser")
	if got := stripDomainPrefix(value, ".google.com"); !bytes.Equal(got, value) {
		t.Errorf("stripDomainPrefix() = %q, want %q", got, value)
	}
}

// TestStripPaddingRefusesABadPadding proves a wrong key is not accepted as a
// valid block.
//
// A CBC decryption under the wrong key yields bytes whose last byte is
// usually not a padding length. Taking the last byte at face value would
// hand back a truncated garbage value.
func TestStripPaddingRefusesABadPadding(t *testing.T) {
	out := make([]byte, 16)
	for i := range out {
		out[i] = 0xAB
	}
	out[15] = 0xFF // a padding length that cannot be right
	if _, ok := stripPadding(out); ok {
		t.Error("stripPadding() accepted a block with an impossible padding length")
	}
}
