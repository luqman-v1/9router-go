package agy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/samber/lo"
)

// Warm-pool bounds. Every knob is an explicit cap (§4.G): a hard worker total
// and an idle TTL so an unused account's agy process does not sit in RSS
// forever. Concurrent turns on one account lease extra workers up to the
// total; idle extras expire on the TTL.
const (
	poolMaxWorkers   = 4
	poolIdleTTL      = 5 * time.Minute
	poolHandshakeTTL = 30 * time.Second
)

// poolEntry is one pre-initialized agy worker plus the session it will serve
// next. sid == "" means no prepared session (tool turns never pre-create one:
// the MCP bridge URL is per-turn). All fields are guarded by Pool.mu except
// while leased (inUse), when the lease holder owns client/dir exclusively.
type poolEntry struct {
	key         string // unique slot id
	config      string // account dir + binary + permission mode + env
	client      *Client
	dir         *AccountDir
	sid         string
	preparedFor string // canonical model the prepared session was created with
	model       string // canonical model the next refill should use
	credKey     string
	gen         uint64 // bumped on lease; a refill from an older gen is dropped
	lastUsed    time.Time
	inUse       bool
}

// Pool keeps agy ACP workers warm between turns. A cold turn pays process
// spawn + initialize + session/new — measured ~1.0–1.7 s on agy 1.5.14 before
// the first prompt byte. A warm worker with a prepared session pays only
// session/prompt; the ~10–80 ms session/new refill runs in the background
// after the turn, off the request path.
type Pool struct {
	mu      sync.Mutex
	entries map[string]*poolEntry
	nextKey int
	max     int
	ttl     time.Duration
}

// NewPool builds a warm pool. Zero options pick the §4.G-safe defaults.
func NewPool(max int, ttl time.Duration) *Pool {
	if max <= 0 {
		max = poolMaxWorkers
	}
	if ttl <= 0 {
		ttl = poolIdleTTL
	}
	return &Pool{entries: make(map[string]*poolEntry), max: max, ttl: ttl}
}

var defaultPool = NewPool(0, 0)

// DefaultPool returns the process-wide warm pool.
func DefaultPool() *Pool { return defaultPool }

// poolEnabled reads the AGY_ACP_POOL kill switch ("0"/"false" disables).
func poolEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("AGY_ACP_POOL")))
	return v != "0" && v != "false" && v != "off"
}

// poolConfig identifies a reusable worker: same account dir, binary,
// permission mode and environment. Credentials are compared separately
// (credKey) so a rotated token pair replaces the worker instead of silently
// reusing a stale one.
func poolConfig(opts TurnOptions, bin string) string {
	root, _ := lo.Coalesce(opts.AccountRoot, filepath.Join(os.TempDir(), "9router-agy-acp-default"))
	return strings.Join([]string{root, bin, strings.ToLower(opts.PermissionMode), strings.Join(opts.ExtraEnv, "\x00")}, "\x00")
}

func credKey(c Credentials) string {
	return c.AccessToken + "\x00" + c.RefreshToken
}

// lease is a checked-out worker. release must be called exactly once; it
// discards the worker when dirty.
type lease struct {
	pool  *Pool
	entry *poolEntry
	sid   string // prepared session id, "" when the turn must create its own
	dirty bool   // error suspicion: never return this worker to the pool
}

// acquire returns a warm worker for opts, spawning one when none is idle.
// The returned lease holds the entry exclusively until release.
func (p *Pool) acquire(ctx context.Context, opts TurnOptions, bin, canonical string) (*lease, error) {
	root, _ := lo.Coalesce(opts.AccountRoot, filepath.Join(os.TempDir(), "9router-agy-acp-default"))
	dir, err := NewAccountDir(root)
	if err != nil {
		return nil, err
	}
	config := poolConfig(opts, bin)
	ck := credKey(opts.Credentials)

	p.mu.Lock()
	p.sweepLocked()
	for _, e := range p.entries {
		if e.config != config || e.inUse || !e.alive() {
			continue
		}
		if e.credKey != ck {
			// Token pair rotated: the running worker cached the old one.
			p.removeLocked(e)
			continue
		}
		e.inUse = true
		e.gen++
		e.model = canonical
		sid := ""
		if len(opts.MCPTools) == 0 && e.preparedFor == canonical {
			sid = e.sid
		}
		e.sid = ""
		e.preparedFor = ""
		p.mu.Unlock()
		return &lease{pool: p, entry: e, sid: sid}, nil
	}
	if len(p.entries) >= p.max {
		p.evictOldestIdleLocked()
	}
	if len(p.entries) >= p.max {
		p.mu.Unlock()
		return nil, fmt.Errorf("agy-acp: warm pool full")
	}
	p.nextKey++
	e := &poolEntry{
		key: fmt.Sprintf("%s#%d", config, p.nextKey), config: config,
		dir: dir, credKey: ck, model: canonical, inUse: true, lastUsed: time.Now(),
	}
	p.entries[e.key] = e
	p.mu.Unlock()

	if err := p.spawn(ctx, e, opts, bin); err != nil {
		p.mu.Lock()
		delete(p.entries, e.key)
		p.mu.Unlock()
		_ = dir.Clear()
		return nil, err
	}
	return &lease{pool: p, entry: e}, nil
}

// spawn starts the worker and performs initialize under a bounded context so
// a hung binary cannot pin the slot forever. The worker process itself runs
// under a background context: its lifetime belongs to the pool, not the turn.
func (p *Pool) spawn(ctx context.Context, e *poolEntry, opts TurnOptions, bin string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if opts.Credentials.AccessToken != "" && opts.Credentials.RefreshToken != "" {
		if err := e.dir.WriteCredentials(opts.Credentials); err != nil {
			return err
		}
	}
	// The worker belongs to the pool, not the turn: exec.CommandContext kills
	// the process when its ctx ends, so Start and buildCommand must stay on a
	// background ctx. The handshake must still respect the caller's deadline.
	// WithoutCancel drops the cancel signal but keeps values and any deadline;
	// WithTimeout then caps it at the handshake TTL.
	hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), poolHandshakeTTL)
	defer cancel()
	client, err := Start(context.Background(), buildCommand(context.Background(), bin, e.dir, opts))
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		client.Close()
		return err
	}
	if _, err := client.Call(hctx, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientInfo":         map[string]any{"name": "9router", "version": "1.0"},
		"clientCapabilities": map[string]any{},
	}); err != nil {
		client.Close()
		return classifyError(err)
	}
	e.client = client
	return nil
}

// newSession creates a session on the leased worker. mcpServers must be an
// array (agy rejects an object with -32602).
func (l *lease) newSession(ctx context.Context, canonical string, mcpServers []any) (string, error) {
	res, err := l.entry.client.Call(ctx, "session/new", map[string]any{
		"mcpServers": mcpServers,
		"model":      canonical,
	})
	if err != nil {
		// A caller cancellation interrupts our call but leaves the worker
		// healthy; only agent-side faults poison it.
		l.dirty = ctx.Err() == nil
		return "", classifyError(err)
	}
	var session struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(res, &session); err != nil || session.SessionID == "" {
		return "", fmt.Errorf("agy-acp: session/new returned no sessionId")
	}
	return session.SessionID, nil
}

// release returns a healthy worker to the pool and refills its prepared
// session in the background. A dirty or dead lease is discarded instead.
func (l *lease) release() {
	p, e := l.pool, l.entry
	if l.dirty || !e.alive() {
		p.mu.Lock()
		p.removeLocked(e)
		p.mu.Unlock()
		return
	}
	p.mu.Lock()
	e.inUse = false
	e.lastUsed = time.Now()
	gen := e.gen
	canRefill := e.sid == ""
	p.mu.Unlock()
	if canRefill {
		go p.refill(e, gen)
	}
}

// removeLocked kills a worker and clears its credential files. Caller holds
// p.mu; Close/Clear block briefly on process teardown, which is acceptable at
// this call rate (once per discarded turn at most).
func (p *Pool) removeLocked(e *poolEntry) {
	if p.entries[e.key] == e {
		delete(p.entries, e.key)
	}
	if e.client != nil {
		e.client.Close()
	}
	_ = e.dir.Clear()
}

// refill pre-creates the next text-only session so the following turn skips
// session/new entirely. gen guards against a stale refill landing on a slot
// that has since been re-leased. Failures leave sid empty; the next turn then
// creates its own session inline.
func (p *Pool) refill(e *poolEntry, gen uint64) {
	p.mu.Lock()
	if e.inUse || e.gen != gen || !e.alive() || e.sid != "" {
		p.mu.Unlock()
		return
	}
	client, model := e.client, e.model
	p.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), poolHandshakeTTL)
	defer cancel()
	res, err := client.Call(ctx, "session/new", map[string]any{
		"mcpServers": []any{},
		"model":      model,
	})
	if err != nil {
		return
	}
	var session struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(res, &session) != nil || session.SessionID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !e.inUse && e.gen == gen && e.alive() && e.sid == "" {
		e.sid = session.SessionID
		e.preparedFor = model
	}
}

func (e *poolEntry) alive() bool {
	if e.client == nil {
		return false
	}
	select {
	case <-e.client.done:
		return false
	default:
		return true
	}
}

// sweepLocked drops dead and idle-expired entries. Caller holds p.mu.
func (p *Pool) sweepLocked() {
	now := time.Now()
	for _, e := range p.entries {
		if e.inUse {
			continue
		}
		if !e.alive() || now.Sub(e.lastUsed) > p.ttl {
			p.removeLocked(e)
		}
	}
}

// evictOldestIdleLocked makes room for a new account when the pool is full.
func (p *Pool) evictOldestIdleLocked() {
	var oldest *poolEntry
	for _, e := range p.entries {
		if e.inUse {
			continue
		}
		if oldest == nil || e.lastUsed.Before(oldest.lastUsed) {
			oldest = e
		}
	}
	if oldest != nil {
		p.removeLocked(oldest)
	}
}

// Close empties the pool. Used by tests and shutdown paths.
func (p *Pool) Close() {
	p.mu.Lock()
	entries := p.entries
	p.entries = make(map[string]*poolEntry)
	p.mu.Unlock()
	for _, e := range entries {
		if e.client != nil {
			e.client.Close()
		}
		_ = e.dir.Clear()
	}
}

// isolatedPoolDeferred returns a new isolated pool. The test must defer
// p.Close() before its TempDir cleanup; defaultPool is restored on Cleanup.
func isolatedPoolDeferred(t interface {
	Cleanup(func())
	Helper()
}) *Pool {
	t.Helper()
	prev := defaultPool
	p := NewPool(0, 0)
	defaultPool = p
	t.Cleanup(func() { defaultPool = prev })
	return p
}
