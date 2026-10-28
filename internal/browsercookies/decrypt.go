package browsercookies

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"crypto/sha256"
	"runtime"

	"golang.org/x/crypto/pbkdf2"
)

// The cookie decryption.
//
// A Chromium browser seals its cookies with a key it keeps elsewhere, and
// the sealed value carries the version of the scheme it was sealed with:
// "v10" for the older scheme and "v11" for the newer one. Both are opened
// here, because a browser on a given machine may hold a mixture — a profile
// that was created years ago and has not been re-encrypted keeps the older
// scheme's rows.

// decryptCookieBytes opens a sealed cookie value.
//
// The two schemes are tried in turn: AES-256-GCM first, which is what the
// modern keyring-derived key is used with, and AES-128-CBC second, which is
// the older scheme. A nil result means this key does not open this value.
func decryptCookieBytes(buf, key256, key128 []byte) []byte {
	if len(buf) < 4 {
		return nil
	}
	prefix := string(buf[:3])
	if prefix != "v10" && prefix != "v11" {
		// A value that is not versioned is a DPAPI blob, which only
		// Windows can open and only under the user's own privilege.
		if plain, err := dpapiUnprotect(buf); err == nil && len(plain) > 0 {
			return plain
		}
		return nil
	}
	rest := buf[3:]

	if len(rest) >= 12+aes.BlockSize && len(key256) == 32 {
		if block, err := aes.NewCipher(key256); err == nil {
			if gcm, err := cipher.NewGCM(block); err == nil {
				nonce := rest[:12]
				ciphertext := rest[12:]
				if plain, err := gcm.Open(nil, nonce, ciphertext, nil); err == nil {
					return plain
				}
			}
		}
	}
	if len(rest) >= aes.BlockSize && len(rest)%aes.BlockSize == 0 && len(key128) == 16 {
		if block, err := aes.NewCipher(key128); err == nil {
			iv := bytes.Repeat([]byte{0x20}, aes.BlockSize)
			out := make([]byte, len(rest))
			cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, rest)
			if plain, ok := stripPadding(out); ok {
				return plain
			}
		}
	}
	return nil
}

// stripPadding removes the PKCS#7 padding from a decrypted block.
//
// The padding is checked rather than trusted: a wrong key produces bytes
// that are not a valid padded block, and accepting them would hand back
// garbage as a cookie.
func stripPadding(out []byte) ([]byte, bool) {
	if len(out) == 0 {
		return nil, false
	}
	pad := int(out[len(out)-1])
	if pad == 0 || pad > aes.BlockSize || pad > len(out) {
		return nil, false
	}
	for _, b := range out[len(out)-pad:] {
		if int(b) != pad {
			return nil, false
		}
	}
	return out[:len(out)-pad], true
}

// stripDomainPrefix removes the host binding a newer browser puts in front
// of a cookie's value.
//
// Chrome 130 and later prepend the SHA-256 of the cookie's host to the
// sealed value, so that a value cannot be moved to another host. The prefix
// is removed when it is there and kept when it is not, because a browser
// that does not add it must still be readable.
func stripDomainPrefix(buf []byte, host string) []byte {
	if len(buf) >= sha256.Size {
		sum := sha256.Sum256([]byte(host))
		if bytes.Equal(buf[:sha256.Size], sum[:]) {
			return buf[sha256.Size:]
		}
	}
	return buf
}

// pbkdf2Key derives a key from a password the way Chromium's keyring does.
func pbkdf2Key(password string, length int) []byte {
	return pbkdf2KeyRounds(password, length, 1)
}

// pbkdf2KeyRounds derives a key from a password with a given round count.
//
// The macOS Keychain's own derivation uses more rounds than the keyring's,
// and both are in play on a Mac.
func pbkdf2KeyRounds(password string, length, rounds int) []byte {
	return pbkdf2.Key([]byte(password), []byte("saltysalt"), rounds, length, sha1.New)
}

// keyCandidate is one possible key.
type keyCandidate struct {
	key256 []byte
	key128 []byte
}

// detectBestKey finds the key that unseals the most cookies.
//
// A browser may hold several keys — one per profile, one per past profile —
// and only one of them opens these cookies. The right one is the one that
// unseals the most rows, so every candidate is tried and the best is kept.
func detectBestKey(ctx context.Context, rows []chromiumRow, dbPath string) (best256, best128 []byte, ok bool) {
	var candidates []keyCandidate
	localStateKeyFound := false
	for _, path := range findLocalStatePaths(dbPath) {
		key, ok := readLocalStateMasterKey(path)
		if !ok || len(key) != 32 {
			continue
		}
		candidates = append(candidates, keyCandidate{key256: key})
		localStateKeyFound = true
	}
	for _, password := range candidatePasswords(ctx, dbPath) {
		candidates = append(candidates, keyCandidate{
			key256: pbkdf2Key(password, 32),
			key128: pbkdf2Key(password, 16),
		})
		if runtime.GOOS == "darwin" && password != "" {
			candidates = append(candidates, keyCandidate{key128: pbkdf2KeyRounds(password, 16, 1003)})
		}
	}

	encrypted := 0
	for _, row := range rows {
		if len(row.encrypted) > 0 {
			encrypted++
		}
	}
	if encrypted == 0 {
		return nil, nil, true
	}

	bestScore := 0
	for _, candidate := range candidates {
		score := 0
		for _, row := range rows {
			if len(row.encrypted) == 0 {
				continue
			}
			if decryptCookieBytes(row.encrypted, candidate.key256, candidate.key128) != nil {
				score++
			}
		}
		if score > bestScore {
			bestScore = score
			best256 = candidate.key256
			best128 = candidate.key128
			ok = true
			if score == encrypted {
				break
			}
		}
	}
	if !ok && localStateKeyFound && len(candidates) > 0 {
		return candidates[0].key256, nil, true
	}
	return best256, best128, ok
}
