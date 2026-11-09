//go:build windows

package mcodelease

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

// dialWindowsPipe opens the broker's named pipe. A Windows named pipe is not a
// socket, so net.Dial cannot reach it: the handle comes from CreateFile over
// \\.\pipe\..., which is what the official MiniMax client does. There is no
// cgo here — x/sys/windows declares the syscall.
func dialWindowsPipe(ctx context.Context, address string, timeout time.Duration) (net.Conn, error) {
	h, err := windows.CreateFile(
		windows.StringToUTF16Ptr(address),
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		0, // no sharing: the broker serves one client per pipe instance
		nil,
		windows.OPEN_EXISTING,
		0,
		0,
	)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	return &pipeConn{
		file:      os.NewFile(uintptr(h), address),
		closeOnce: sync.Once{},
	}, nil
}

func isWindows() bool { return true }

// pipeConn adapts a Windows named-pipe handle to net.Conn. The handle carries
// no deadline mechanism, so a read/write deadline is enforced by closing the
// handle out from under the blocked call: the pending I/O fails, the goroutine
// returns, and the caller sees the same error a socket deadline would produce.
type pipeConn struct {
	file      *os.File
	closeOnce sync.Once
	closeErr  error
}

func (c *pipeConn) Read(b []byte) (int, error) {
	n, err := c.file.Read(b)
	if err != nil && isPipeClosed(err) {
		return n, os.ErrDeadlineExceeded
	}
	return n, err
}

func (c *pipeConn) Write(b []byte) (int, error) {
	n, err := c.file.Write(b)
	if err != nil && isPipeClosed(err) {
		return n, os.ErrDeadlineExceeded
	}
	return n, err
}

func (c *pipeConn) Close() error {
	c.closeOnce.Do(func() { c.closeErr = c.file.Close() })
	return c.closeErr
}

func (c *pipeConn) LocalAddr() net.Addr  { return pipeAddr(c.file.Name()) }
func (c *pipeConn) RemoteAddr() net.Addr { return pipeAddr(c.file.Name()) }

func (c *pipeConn) SetDeadline(t time.Time) error {
	if t.IsZero() {
		return nil
	}
	d := time.Until(t)
	if d <= 0 {
		// An already-expired deadline means close now: the only way a
		// blocking read on a file-backed pipe returns.
		_ = c.Close()
		return nil
	}
	time.AfterFunc(d, func() { _ = c.Close() })
	return nil
}

func (c *pipeConn) SetReadDeadline(t time.Time) error  { return c.SetDeadline(t) }
func (c *pipeConn) SetWriteDeadline(t time.Time) error { return c.SetDeadline(t) }

type pipeAddr string

func (a pipeAddr) Network() string { return "pipe" }
func (a pipeAddr) String() string  { return string(a) }

// isPipeClosed reports whether an I/O error is the "handle closed" the
// deadline path produces.
func isPipeClosed(err error) bool {
	return os.IsTimeout(err) || errors.Is(err, os.ErrClosed)
}
