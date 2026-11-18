// Package agy implements the Agent Client Protocol (ACP) transport for the
// official Antigravity CLI (`agy acp`). It is used by the agy-acp provider,
// which stays strictly independent from the HTTP-based antigravity provider.
//
// The protocol is newline-delimited JSON-RPC 2.0 over the child process's
// stdin/stdout. See internal/proxy/executor/agy_acp.go for the request adapter.
package agy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
)

// rpcMessage is the envelope exchanged with the ACP subprocess.
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("acp error %d: %s", e.Code, e.Message)
}

// rpcRequest is a pending response correlation entry.
type rpcRequest struct {
	resp chan rpcMessage
}

// notification carries an unsolicited method+params from the agent.
type notification struct {
	Method string
	Params json.RawMessage
}

// Client owns one ACP subprocess and multiplexes JSON-RPC requests and
// notifications. It is safe for one concurrent prompt at a time (ACP's own
// constraint); multiple Clients are spawned for concurrency.
type Client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser

	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[string]*rpcRequest
	nextID  atomic.Int64

	notifyMu sync.Mutex
	notifyCh chan notification
	done     chan struct{}
	errMu    sync.Mutex
	exitErr  error
	// stderrTail holds a bounded tail for diagnostics. Guarded by errMu.
	stderrTail []byte
}

// Start spawns the given command and begins reading its stdout. The caller
// must Close the client when done.
func Start(ctx context.Context, cmd *exec.Cmd) (*Client, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("acp stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("acp stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("acp stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, fmt.Errorf("acp start: %w", err)
	}
	c := &Client{
		cmd:      cmd,
		stdin:    stdin,
		stdout:   stdout,
		stderr:   stderr,
		pending:  make(map[string]*rpcRequest),
		notifyCh: make(chan notification, 256),
		done:     make(chan struct{}),
	}
	go c.readLoop()
	go c.waitLoop()
	go func() {
		// Drain stderr so the child never blocks on a full pipe. Only a
		// bounded tail is kept, for error reporting; no token is logged.
		buf := make([]byte, 32*1024)
		for {
			n, err := stderr.Read(buf)
			if n > 0 {
				c.appendStderr(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		<-ctx.Done()
		c.Close()
	}()
	return c, nil
}

func (c *Client) appendStderr(p []byte) {
	c.errMu.Lock()
	defer c.errMu.Unlock()
	if len(c.stderrTail) < 8*1024 {
		c.stderrTail = append(c.stderrTail, p...)
		if len(c.stderrTail) > 8*1024 {
			c.stderrTail = c.stderrTail[:8*1024]
		}
	}
}

func (c *Client) waitLoop() {
	err := c.cmd.Wait()
	c.errMu.Lock()
	c.exitErr = err
	c.errMu.Unlock()
	c.closeDone()
}

func (c *Client) closeDone() {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.done:
	default:
		close(c.done)
	}
}

func (c *Client) readLoop() {
	defer c.closeDone()
	// ACP responses carry model rosters and tool schemas; use a generous but
	// bounded line length rather than bufio.Scanner's 64 KiB default.
	r := bufio.NewReaderSize(c.stdout, 64*1024)
	buf := make([]byte, 0, 64*1024)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			buf = append(buf, line...)
			if len(buf) > maxLineBytes {
				buf = buf[len(buf)-maxLineBytes:]
			}
			if bytes.IndexByte(buf, '\n') >= 0 {
				c.dispatch(bytes.TrimSpace(buf))
				buf = buf[:0]
			}
		}
		if err != nil {
			if err != io.EOF {
				c.errMu.Lock()
				c.exitErr = err
				c.errMu.Unlock()
			}
			return
		}
	}
}

const maxLineBytes = 16 << 20

func (c *Client) dispatch(line []byte) {
	var msg rpcMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		return // banner / non-JSON output — ignore, like the upstream executor
	}
	if msg.Method != "" && len(msg.ID) == 0 {
		c.notifyMu.Lock()
		select {
		case c.notifyCh <- notification{Method: msg.Method, Params: msg.Params}:
		default: // bounded queue: drop rather than block the agent
		}
		c.notifyMu.Unlock()
		return
	}
	if len(msg.ID) == 0 {
		return
	}
	key := string(msg.ID)
	c.mu.Lock()
	req := c.pending[key]
	delete(c.pending, key)
	c.mu.Unlock()
	if req != nil {
		select {
		case req.resp <- msg:
		default:
		}
	}
}

// Call sends a JSON-RPC request and waits for the correlated response. It
// fails promptly when the subprocess exits.
func (c *Client) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	idBytes, _ := json.Marshal(id)
	reqPayload, err := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  any             `json:"params"`
	}{"2.0", idBytes, method, params})
	if err != nil {
		return nil, fmt.Errorf("acp marshal %s: %w", method, err)
	}
	ch := make(chan rpcMessage, 1)
	c.mu.Lock()
	c.pending[string(idBytes)] = &rpcRequest{resp: ch}
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, string(idBytes))
		c.mu.Unlock()
	}()

	c.writeMu.Lock()
	_, err = fmt.Fprintf(c.stdin, "%s\n", reqPayload)
	c.writeMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("acp write %s: %w", method, err)
	}

	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	case <-c.done:
		return nil, c.processError()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Notify sends a JSON-RPC response to a server-initiated request (e.g. a
// permission prompt), without expecting a reply.
func (c *Client) Notify(id json.RawMessage, result any) error {
	payload, err := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  any             `json:"result"`
	}{"2.0", id, result})
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = fmt.Fprintf(c.stdin, "%s\n", payload)
	return err
}

// Notifications exposes the bounded notification queue.
func (c *Client) Notifications() <-chan notification { return c.notifyCh }

func (c *Client) processError() error {
	c.errMu.Lock()
	defer c.errMu.Unlock()
	if c.exitErr != nil && len(c.stderrTail) > 0 {
		return fmt.Errorf("acp subprocess exited: %w (stderr: %s)", c.exitErr, string(c.stderrTail))
	}
	if c.exitErr != nil {
		return fmt.Errorf("acp subprocess exited: %w", c.exitErr)
	}
	return fmt.Errorf("acp subprocess exited")
}

// Close terminates the subprocess and releases its pipes. Idempotent.
func (c *Client) Close() {
	c.closeDone()
	_ = c.stdin.Close()
	_ = c.stdout.Close()
	_ = c.stderr.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
}
