package handlerutil

import (
	"bytes"
	"sync"
)

var (
	bufPool = sync.Pool{
		New: func() any {
			return new(bytes.Buffer)
		},
	}
	slicePool = sync.Pool{
		New: func() any {
			b := make([]byte, 0, 4096)
			return &b
		},
	}
)

// AcquireBuffer gets a buffer from the pool and resets it.
func AcquireBuffer() *bytes.Buffer {
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	return buf
}

// ReleaseBuffer returns a buffer to the pool if its capacity is reasonable (< 64KB).
func ReleaseBuffer(buf *bytes.Buffer) {
	if buf == nil || buf.Cap() > 64*1024 {
		return
	}
	buf.Reset()
	bufPool.Put(buf)
}

// AcquireByteSlice gets a reusable byte slice with at least 4KB capacity.
func AcquireByteSlice() *[]byte {
	b := slicePool.Get().(*[]byte)
	*b = (*b)[:0]
	return b
}

// ReleaseByteSlice returns a byte slice to the pool if capacity is reasonable.
func ReleaseByteSlice(b *[]byte) {
	if b == nil || cap(*b) > 64*1024 {
		return
	}
	*b = (*b)[:0]
	slicePool.Put(b)
}
