// Package fastjson is a thin seam over encoding/json/v2 shared by the proxy
// hot paths: chat request/response translation, upstream payload repair, and
// the semantic cache key builder.
//
// It exists so callers depend on one place for the option set those paths need
// instead of repeating it per call site. Two properties are non-negotiable:
//
//   - Deterministic output. Prompt-prefix caches upstream (DeepSeek, Anthropic)
//     key on the exact request bytes, so serializing the same value twice must
//     produce the same bytes. encoding/json/v2 leaves map members in Go's
//     randomized iteration order unless asked to sort them, so the option is
//     set explicitly here rather than left to each call site's discretion.
//     (The backend this replaced, sonic's ConfigDefault, left them unsorted
//     too — #186 wanted sorting and never got it, which is why marshalStable
//     and the fingerprint/dedupe paths carry their own Deterministic option.)
//   - Identical error surface. Errors are wrapped by the caller, so the
//     underlying encoding/json/v2 error type is what propagates; there is no
//     translation layer.
package fastjson

import (
	"io"

	json "encoding/json/v2"
)

// Deterministic marshaling: map members are sorted by name.
var marshalOpts = json.Deterministic(true)

// Marshal serializes v to JSON, sorting map members by name.
func Marshal(v any) ([]byte, error) {
	return json.Marshal(v, marshalOpts)
}

// Unmarshal deserializes data into v.
func Unmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// UnmarshalRead deserializes a single JSON value read from r into v.
func UnmarshalRead(r io.Reader, v any) error {
	return json.UnmarshalRead(r, v)
}
