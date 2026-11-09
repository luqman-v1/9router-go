package translator

import (
	"encoding/base64"
	"strings"
)

const maxClaudeSignatureLen = 32 * 1024 * 1024
const claudeSignatureMarker = 0x12

func stripCachePrefix(rawSignature string) string {
	sig := strings.TrimSpace(rawSignature)
	if sig == "" {
		return ""
	}
	if idx := strings.Index(sig, "#"); idx >= 0 {
		return strings.TrimSpace(sig[idx+1:])
	}
	return sig
}

// HasClaudeSignaturePrefix reports whether a signature starts with 'E' or 'R' prefix.
func HasClaudeSignaturePrefix(rawSignature string) bool {
	sig := stripCachePrefix(rawSignature)
	return len(sig) > 0 && (sig[0] == 'E' || sig[0] == 'R')
}

// IsValidClaudeSignature validates base64 layers and Claude signature marker byte (0x12).
// Port of upstream open-sse/utils/claudeSignature.js.
func IsValidClaudeSignature(rawSignature string) bool {
	sig := stripCachePrefix(rawSignature)
	if sig == "" || len(sig) > maxClaudeSignatureLen {
		return false
	}

	if sig[0] == 'E' {
		decoded, err := base64.StdEncoding.DecodeString(sig)
		if err != nil {
			return false
		}
		return len(decoded) > 0 && decoded[0] == claudeSignatureMarker
	}

	if sig[0] == 'R' {
		outer, err := base64.StdEncoding.DecodeString(sig)
		if err != nil || len(outer) == 0 || outer[0] != 0x45 { // 0x45 == 'E'
			return false
		}
		inner, err := base64.StdEncoding.DecodeString(string(outer))
		if err != nil {
			return false
		}
		return len(inner) > 0 && inner[0] == claudeSignatureMarker
	}

	return false
}
