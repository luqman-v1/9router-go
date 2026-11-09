//go:build windows

package browsercookies

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// dpapiUnprotect opens a DPAPI blob.
//
// The blob was sealed by the browser under the user's own privilege, and
// this opens it under the same privilege — the same one that protects the
// profile it came from. Nothing is read that the user could not have read
// by opening the browser themselves.
func dpapiUnprotect(blob []byte) ([]byte, error) {
	if len(blob) == 0 {
		return nil, fmt.Errorf("empty DPAPI blob")
	}
	in := windows.DataBlob{
		Size: uint32(len(blob)),
		Data: &blob[0],
	}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, 0, &out); err != nil {
		return nil, err
	}
	defer func() {
		if out.Data != nil {
			_, _ = windows.LocalFree(windows.Handle(uintptr(unsafe.Pointer(out.Data))))
		}
	}()
	if out.Size == 0 || out.Data == nil {
		return nil, fmt.Errorf("DPAPI returned an empty key")
	}
	raw := unsafe.Slice(out.Data, out.Size)
	key := make([]byte, len(raw))
	copy(key, raw)
	return key, nil
}
