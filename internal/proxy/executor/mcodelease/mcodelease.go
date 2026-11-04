// Package mcodelease is the auth-lease client for the MiniMax Code desktop
// broker, plus the in-process lease cache. Port of upstream
// open-sse/services/minimaxCodeLease.js (upstream v0.5.112, 85d73bd), which
// was itself adapted from MiniMax-AI/minimax-code
// packages/oauth-lease-protocol (MIT).
//
// Wire protocol: each request/response is a JSON payload prefixed with a
// 4-byte big-endian length, carried over a local named pipe (Windows) or a
// unix domain socket next to the desktop app's data dir. The broker is the
// running desktop app; it owns token refresh. We never read or write
// credential files — only the broker-published capability file and the pipe
// itself.
//
// Lease mode lets a connection ride the desktop app's sign-in instead of
// holding its own refresh token — MiniMax refresh tokens are single-use, so
// borrowing (rather than owning) means the router and the official CLI/desktop
// app can share an account without invalidating each other.
package mcodelease

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	json "encoding/json/v2"
)

// ProtocolVersion is the lease protocol version this client speaks.
const ProtocolVersion = 1

// MaxFrameBytes bounds one frame's JSON payload. The broker enforces the same
// bound; a larger value is treated as a protocol error rather than a body.
const MaxFrameBytes = 64 * 1024

// MaxMinValidityMs caps how long a lease may be asked to stay valid, so a
// caller cannot pin a token the desktop app has already rotated.
const MaxMinValidityMs = 5 * 60 * 1000

// DefaultMCodeDataDir is where the desktop app installs its data when no
// override is given.
var DefaultMCodeDataDir = filepath.Join(homeDir(), ".minimax")

const (
	socketFile      = "mcode-auth-lease-v1.sock"
	capabilityFile  = "mcode-auth-lease-v1.cap"
	windowsPipeGlob = `\\.\pipe\mcode-auth-lease-`
)

// CapabilityLen is the exact length of a capability file's content.
const capabilityLen = 43

// Broker timeout for status/lease calls. The desktop app answers locally, so
// a slow broker is a hung one.
const (
	statusTimeout = 5 * time.Second
	leaseTimeout  = 10 * time.Second
)

// Error codes the broker reports, in its own vocabulary. They map to the
// upstream ERROR_MESSAGES table and are what a dashboard surfaces to an
// operator, so they are preserved verbatim rather than flattened.
const (
	CodeAuthRequired      = "AUTH_REQUIRED"
	CodeBrokerUnavailable = "BROKER_UNAVAILABLE"
	CodeCapabilityReject  = "CAPABILITY_REJECTED"
	CodeProtocolMismatch  = "PROTOCOL_MISMATCH"
	CodeInvalidRequest    = "INVALID_REQUEST"
	CodeInternalError     = "INTERNAL_ERROR"
)

var errorMessages = map[string]string{
	CodeAuthRequired:      "Desktop authentication is required.",
	CodeBrokerUnavailable: "Desktop authentication integration is unavailable.",
	CodeCapabilityReject:  "Desktop authentication capability was rejected.",
	CodeProtocolMismatch:  "Desktop authentication protocol is incompatible.",
	CodeInvalidRequest:    "Desktop authentication request is invalid.",
	CodeInternalError:     "Desktop authentication request failed.",
}

// AuthLeaseError is a lease failure carrying the broker's own error code, so a
// caller can tell "the desktop app is not signed in" (AUTH_REQUIRED, an
// operator fix) from "the broker is not running" (BROKER_UNAVAILABLE, a
// start-the-app fix) instead of collapsing both into one string.
type AuthLeaseError struct {
	Code string
	msg  string
	err  error
}

func (e *AuthLeaseError) Error() string {
	if e.msg != "" {
		return e.msg
	}
	if m, ok := errorMessages[e.Code]; ok {
		return m
	}
	return "auth lease failed"
}

func (e *AuthLeaseError) Unwrap() error { return e.err }

func leaseError(code, format string, args ...any) *AuthLeaseError {
	e := &AuthLeaseError{Code: code}
	if format != "" {
		e.msg = fmt.Sprintf(format, args...)
	}
	return e
}

// Status is what the broker reports about its own sign-in state.
type StatusResult struct {
	Status      string
	Generation  int64
	ExpiresAtMs int64
}

// Lease is one borrowed access token.
type LeaseResult struct {
	AccessToken string
	ExpiresAtMs int64
	Generation  int64
}

// endpoint is the resolved transport for one desktop install.
type endpoint struct {
	transport      string // "pipe" or "unix"
	address        string
	capabilityFile string
}

// IsLeaseConnection reports whether a connection borrows the desktop app's
// sign-in (authType "mcode-lease") instead of holding its own refresh token.
func IsLeaseConnection(providerSpecificData map[string]any) bool {
	if providerSpecificData == nil {
		return false
	}
	s, _ := providerSpecificData["authType"].(string)
	return s == "mcode-lease"
}

// ResolveDataDir resolves the desktop install a connection borrows from.
// Order: per-connection override → env → the default install location.
func ResolveDataDir(providerSpecificData map[string]any) (string, error) {
	if s, ok := providerSpecificData["mcodeDataDir"].(string); ok && strings.TrimSpace(s) != "" {
		return resolveAbsolute(strings.TrimSpace(s))
	}
	if s := os.Getenv("MINIMAX_DESKTOP_DATA_DIR"); strings.TrimSpace(s) != "" {
		return resolveAbsolute(strings.TrimSpace(s))
	}
	return DefaultMCodeDataDir, nil
}

func resolveAbsolute(dir string) (string, error) {
	if !filepath.IsAbs(dir) {
		return "", leaseError(CodeInvalidRequest, "dataDir must be an absolute path.")
	}
	return filepath.Clean(dir), nil
}

// ResolveEndpoint maps a data dir to the broker's transport. The broker names
// its Windows pipe after the lowercased, win32-normalized data dir: a
// forward-slash join hashes to a different pipe, so the path is normalized
// first, exactly like the official client does.
func ResolveEndpoint(dataDir string) (endpoint, error) {
	if isWindows() {
		if !filepath.IsAbs(dataDir) {
			return endpoint{}, leaseError(CodeInvalidRequest, "dataDir must be an absolute path on Windows.")
		}
		canonical := strings.TrimRight(filepath.ToSlash(dataDir), `/\`)
		canonical = filepath.FromSlash(canonical)
		sum := sha256.Sum256([]byte(strings.ToLower(canonical)))
		digest := hex.EncodeToString(sum[:])[:16]
		return endpoint{
			transport:      "pipe",
			address:        windowsPipeGlob + digest,
			capabilityFile: filepath.Join(canonical, "run", capabilityFile),
		}, nil
	}
	if !filepath.IsAbs(dataDir) {
		return endpoint{}, leaseError(CodeInvalidRequest, "dataDir must be an absolute path.")
	}
	canonical := strings.TrimRight(dataDir, `/\`)
	return endpoint{
		transport:      "unix",
		address:        filepath.Join(canonical, "run", socketFile),
		capabilityFile: filepath.Join(canonical, "run", capabilityFile),
	}, nil
}

// readCapability reads the broker-published capability token. The file's
// existence proves the desktop app installed the broker; its exact shape
// (43 base64url characters) proves it is that file.
func readCapability(ep endpoint) (string, error) {
	raw, err := os.ReadFile(ep.capabilityFile)
	if err != nil {
		return "", leaseError(CodeBrokerUnavailable,
			"Cannot read capability file %s. Is the MiniMax Code desktop app installed and logged in on this machine? (%v)",
			ep.capabilityFile, err)
	}
	cap := strings.TrimSpace(string(raw))
	if len(cap) != capabilityLen || !isBase64URL(cap) {
		return "", leaseError(CodeBrokerUnavailable, "Capability file has unexpected content.")
	}
	return cap, nil
}

func isBase64URL(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

type leaseRequest struct {
	Version     int    `json:"version"`
	RequestID   string `json:"requestId"`
	Capability  string `json:"capability"`
	Method      string `json:"method"`
	MinValidity int64  `json:"minValidityMs,omitempty"`
}

type leaseResponse struct {
	OK    bool `json:"ok"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Result *struct {
		Status      string `json:"status"`
		Generation  int64  `json:"generation"`
		ExpiresAtMs int64  `json:"expiresAtMs"`
		AccessToken string `json:"accessToken"`
	} `json:"result"`
}

func encodeFrame(v any) ([]byte, error) {
	payload, err := json.Marshal(v)
	if err != nil {
		return nil, leaseError(CodeInternalError, "marshal request: %v", err)
	}
	if len(payload) == 0 || len(payload) > MaxFrameBytes {
		return nil, leaseError(CodeInvalidRequest, "Frame payload out of bounds.")
	}
	frame := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(payload)))
	copy(frame[4:], payload)
	return frame, nil
}

// sendRequest dials the broker, writes one frame and reads one frame back.
// The dial is context-aware so a request cancelled by the client does not
// wait out the timeout.
func sendRequest(ctx context.Context, ep endpoint, req leaseRequest, timeout time.Duration) (*leaseResponse, error) {
	var (
		conn net.Conn
		err  error
	)
	dialer := &net.Dialer{Timeout: timeout}
	if ep.transport == "pipe" {
		conn, err = dialWindowsPipe(ctx, ep.address, timeout)
	} else {
		conn, err = dialer.DialContext(ctx, "unix", ep.address)
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, leaseError(CodeBrokerUnavailable,
			"Cannot reach auth broker at %s: %v. Is the MiniMax Code desktop app running?", ep.address, err)
	}
	defer func() { _ = conn.Close() }()
	return exchangeFrame(ctx, conn, req, timeout)
}

// exchangeFrame performs the wire exchange over an established connection:
// one length-prefixed JSON request out, one length-prefixed JSON response in.
// It is separate from the dial so the codec can be exercised against any
// net.Conn — including net.Pipe, which has no platform dependence.
func exchangeFrame(ctx context.Context, conn net.Conn, req leaseRequest, timeout time.Duration) (*leaseResponse, error) {
	// The context governs the exchange whenever it carries a deadline: the
	// ctx.Done() branch below closes the connection, and that close is what
	// unblocks the parked write. A transport deadline set to the same instant
	// would race it — both expire together, the netpoller's coarser clock
	// usually delivers the transport timeout first, and the caller then sees
	// "i/o timeout" instead of context.DeadlineExceeded. So the transport
	// deadline is only armed when there is no context deadline to honour, as
	// the backstop for a broker that stops answering mid-exchange.
	if _, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(time.Time{})
	} else {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}
	frame, err := encodeFrame(req)
	if err != nil {
		return nil, err
	}
	// A blocked write on a synchronous transport (a named pipe, a net.Pipe)
	// is not interrupted by a cancelled context, so the write is moved off
	// the caller's goroutine and the caller waits on either outcome. Without
	// this, a client that disconnects mid-request keeps the goroutine parked
	// until the transport's own timeout expires.
	writeErr := make(chan error, 1)
	go func() { _, werr := conn.Write(frame); writeErr <- werr }()
	select {
	case err := <-writeErr:
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, leaseError(CodeBrokerUnavailable, "Cannot write to auth broker: %v", err)
		}
	case <-ctx.Done():
		// Closing the conn is what actually unblocks the parked write.
		_ = conn.Close()
		// The error the parked write finally reports is deliberately
		// discarded: the caller is being handed ctx.Err(), and the write's
		// own error is the consequence of that close.
		<-writeErr //nolint:errcheck // drained on purpose; the caller gets ctx.Err()
		return nil, ctx.Err()
	}
	header := make([]byte, 4)
	if err := readFullCtx(ctx, conn, header); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(header)
	if n == 0 || n > MaxFrameBytes {
		return nil, leaseError(CodeProtocolMismatch, "Broker sent an invalid frame length.")
	}
	payload := make([]byte, n)
	if err := readFullCtx(ctx, conn, payload); err != nil {
		return nil, err
	}
	var resp leaseResponse
	if err := json.Unmarshal(payload, &resp); err != nil {
		return nil, leaseError(CodeProtocolMismatch, "Broker sent a non-JSON frame.")
	}
	return &resp, nil
}

// readFullCtx reads exactly len(buf) bytes, abandoning the wait when the
// context is done. A synchronous transport's Read is not interrupted by a
// cancelled context, so the read runs on its own goroutine and closing the
// conn is what releases it.
func readFullCtx(ctx context.Context, conn net.Conn, buf []byte) error {
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := io.ReadFull(conn, buf)
		done <- result{n, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return leaseError(CodeBrokerUnavailable, "Broker closed the connection early: %v", r.err)
		}
		return nil
	case <-ctx.Done():
		_ = conn.Close()
		<-done
		return ctx.Err()
	}
}

func assertOK(resp *leaseResponse) (*leaseResponse, error) {
	if resp != nil && resp.OK {
		return resp, nil
	}
	if resp != nil && resp.Error != nil {
		if _, known := errorMessages[resp.Error.Code]; known {
			return nil, leaseError(resp.Error.Code, "")
		}
	}
	return nil, leaseError(CodeInternalError, "")
}

// Status asks the broker whether the desktop app is installed, running and
// signed in. It is read-only: it leases nothing.
func Status(ctx context.Context, dataDir string) (StatusResult, error) {
	ep, err := ResolveEndpoint(dataDir)
	if err != nil {
		return StatusResult{}, err
	}
	cap, err := readCapability(ep)
	if err != nil {
		return StatusResult{}, err
	}
	resp, err := sendRequest(ctx, ep, leaseRequest{
		Version:    ProtocolVersion,
		RequestID:  "9r-status-" + strconv.FormatInt(time.Now().UnixMilli(), 36),
		Capability: cap,
		Method:     "status",
	}, statusTimeout)
	if err != nil {
		return StatusResult{}, err
	}
	ok, err := assertOK(resp)
	if err != nil {
		return StatusResult{}, err
	}
	if ok.Result == nil {
		return StatusResult{}, leaseError(CodeProtocolMismatch, "Broker sent no result for status.")
	}
	return StatusResult{
		Status:      ok.Result.Status,
		Generation:  ok.Result.Generation,
		ExpiresAtMs: ok.Result.ExpiresAtMs,
	}, nil
}

// Lease borrows an access token valid for at least minValidityMs. The value is
// clamped to MaxMinValidityMs so a caller cannot pin a rotated token.
func Lease(ctx context.Context, dataDir string, minValidityMs int64) (LeaseResult, error) {
	ep, err := ResolveEndpoint(dataDir)
	if err != nil {
		return LeaseResult{}, err
	}
	cap, err := readCapability(ep)
	if err != nil {
		return LeaseResult{}, err
	}
	clamped := minValidityMs
	if clamped < 0 {
		clamped = 0
	}
	if clamped > MaxMinValidityMs {
		clamped = MaxMinValidityMs
	}
	resp, err := sendRequest(ctx, ep, leaseRequest{
		Version:     ProtocolVersion,
		RequestID:   "9r-lease-" + strconv.FormatInt(time.Now().UnixMilli(), 36),
		Capability:  cap,
		Method:      "lease",
		MinValidity: clamped,
	}, leaseTimeout)
	if err != nil {
		return LeaseResult{}, err
	}
	ok, err := assertOK(resp)
	if err != nil {
		return LeaseResult{}, err
	}
	if ok.Result == nil || ok.Result.AccessToken == "" {
		return LeaseResult{}, leaseError(CodeProtocolMismatch, "Broker sent no access token.")
	}
	return LeaseResult{
		AccessToken: ok.Result.AccessToken,
		ExpiresAtMs: ok.Result.ExpiresAtMs,
		Generation:  ok.Result.Generation,
	}, nil
}

// refreshMargin keeps a lease in cache until it is this close to expiry, so a
// request does not race the token's own death.
const refreshMargin = 60 * time.Second

// manager caches leased tokens and serializes lease requests (single-flight),
// keyed by data dir so cn/global connections sharing one desktop install share
// one lease. The desktop broker owns refresh — we only ask for a token valid
// for at least MaxMinValidityMs, keep it while it stays fresh, and drop it on
// upstream 401.
type manager struct {
	mu       sync.Mutex
	cached   *LeaseResult
	inflight chan struct{}
	// err holds the failure of the in-flight lease so every waiter sees the
	// same error instead of one succeeding and the rest hanging.
	err error
	// leaseFn overrides the broker call, so the cache and single-flight
	// invariants can be tested without a desktop install. nil means the real
	// Lease.
	leaseFn func(ctx context.Context, dataDir string, minValidityMs int64) (LeaseResult, error)
}

func (m *manager) lease(ctx context.Context, dataDir string) (LeaseResult, error) {
	if m.leaseFn != nil {
		return m.leaseFn(ctx, dataDir, MaxMinValidityMs)
	}
	return Lease(ctx, dataDir, MaxMinValidityMs)
}

func (m *manager) getToken(ctx context.Context, dataDir string) (LeaseResult, error) {
	m.mu.Lock()
	if m.cached != nil && time.Until(millisToTime(m.cached.ExpiresAtMs)) > refreshMargin {
		l := *m.cached
		m.mu.Unlock()
		return l, nil
	}
	if m.inflight != nil {
		ch := m.inflight
		m.mu.Unlock()
		select {
		case <-ch:
			m.mu.Lock()
			l, err := m.cached, m.err
			m.mu.Unlock()
			if err != nil {
				return LeaseResult{}, err
			}
			if l == nil {
				return LeaseResult{}, leaseError(CodeInternalError, "lease finished without a token")
			}
			return *l, nil
		case <-ctx.Done():
			return LeaseResult{}, ctx.Err()
		}
	}
	ch := make(chan struct{})
	m.inflight = ch
	m.mu.Unlock()

	l, err := m.lease(ctx, dataDir)

	m.mu.Lock()
	m.inflight = nil
	m.err = err
	if err == nil {
		m.cached = &l
	}
	close(ch)
	m.mu.Unlock()
	if err != nil {
		return LeaseResult{}, err
	}
	return l, nil
}

func (m *manager) invalidate() {
	m.mu.Lock()
	m.cached = nil
	m.mu.Unlock()
}

var (
	managersMu sync.Mutex
	// managers is keyed by data dir and holds one entry per desktop install
	// an operator has ever pointed a connection at. The key is never
	// request-supplied — it comes from the connection row or the env var — so
	// the map grows with operator actions, not with traffic, and each entry is
	// a single cached token. It is deliberately never pruned: deleting a
	// connection would have to reach this package, and a stale entry costs
	// one struct.
	managers = map[string]*manager{}
)

func managerFor(dataDir string) *manager {
	managersMu.Lock()
	defer managersMu.Unlock()
	m, ok := managers[dataDir]
	if !ok {
		m = &manager{}
		managers[dataDir] = m
	}
	return m
}

// GetLeaseToken returns a cached-lease token for a connection's desktop
// install. It returns an *AuthLeaseError when the broker is unreachable.
func GetLeaseToken(ctx context.Context, providerSpecificData map[string]any) (LeaseResult, error) {
	dataDir, err := ResolveDataDir(providerSpecificData)
	if err != nil {
		return LeaseResult{}, err
	}
	return managerFor(dataDir).getToken(ctx, dataDir)
}

// InvalidateLeaseToken drops the cached token for a connection's install, so
// the next request takes a fresh lease. Called on an upstream 401.
func InvalidateLeaseToken(providerSpecificData map[string]any) {
	dataDir, err := ResolveDataDir(providerSpecificData)
	if err != nil {
		return
	}
	managerFor(dataDir).invalidate()
}

func millisToTime(ms int64) time.Time {
	return time.UnixMilli(ms)
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "."
}

// leaseErrCode reports a lease error's broker code, or "" for any other error.
func leaseErrCode(err error) string {
	var le *AuthLeaseError
	if errors.As(err, &le) {
		return le.Code
	}
	return ""
}
