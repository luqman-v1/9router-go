package executor

import "encoding/base64"

// Qoder body encoding — port of open-sse/shared/qoder/encoding.js
// (qoderEncodeBody), itself a port of qoder2api's QoderEncoding.java.
//
// Algorithm, in order:
//  1. base64-encode the plaintext bytes (standard alphabet),
//  2. split the result into thirds and reorder as [tail][mid][head],
//  3. substitute each character through a custom alphabet.
//
// The obfuscation exists because Qoder sits behind Alibaba Cloud WAF, which
// pattern-matches the plaintext request body. The server decodes it in reverse
// only because the URL carries &Encode=1 — see QoderChatURL — so the encoded
// bytes are also what the COSY signature must cover.

// qoderStdAlphabet is the standard base64 alphabet.
const qoderStdAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

// qoderCustomAlphabet is the substitution table. Index i of it replaces index
// i of qoderStdAlphabet.
const qoderCustomAlphabet = "_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!"

// qoderCharMap maps a std-base64 byte to its encoded byte, with '-' meaning "not
// in the alphabet" and therefore passed through unchanged — the same role
// upstream's table plays with -1. Read-only after init.
var qoderCharMap = func() [128]byte {
	var table [128]byte
	for i := range table {
		table[i] = '-'
	}
	for i := range len(qoderStdAlphabet) {
		table[qoderStdAlphabet[i]] = qoderCustomAlphabet[i]
	}
	// Padding is a character in its own right upstream (it maps to "$"), not
	// something to leave alone.
	table['='] = '$'
	return table
}()

// QoderEncodeBody encodes a Qoder request body for the wire. The returned
// bytes are what the caller must hash and sign, not the plaintext.
func QoderEncodeBody(plaintext []byte) []byte {
	std := []byte(base64.StdEncoding.EncodeToString(plaintext))
	n := len(std)
	if n == 0 {
		return []byte{}
	}

	// [tail][mid][head]: split into thirds and rotate.
	third := n / 3
	out := make([]byte, 0, n)
	out = append(out, std[n-third:]...)
	out = append(out, std[third:n-third]...)
	out = append(out, std[:third]...)

	for i, c := range out {
		if c < 128 && qoderCharMap[c] != '-' {
			out[i] = qoderCharMap[c]
		}
	}
	return out
}
