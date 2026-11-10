package audit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// entryStore is the slice of the audit store the recorder writes through.
type entryStore interface {
	BatchInsert(ctx context.Context, entries []Entry) error
}

// Exporter receives rows after they are committed to the database. The OTLP
// follow-up lands here: a nil exporter means admin events stay in the log
// only.
type Exporter interface {
	Enqueue(entries []Entry)
	Stop(deadline time.Duration)
}

// Config tunes a Recorder.
type Config struct {
	// BufferSize bounds queued events; a full queue drops new events.
	BufferSize int
	// FlushInterval and FlushSize bound how long and how many events wait
	// before a database write.
	FlushInterval time.Duration
	FlushSize     int
	// Key signs the hash chain (see DeriveKey). Empty uses plain SHA-256.
	Key []byte
	// InstanceID names this writer's hash chain. Empty generates a random id.
	InstanceID string
	// DefaultTenantID fills events recorded without a tenant.
	DefaultTenantID string
	Logger          *slog.Logger
}

// Recorder queues audit events and writes them in batches from one goroutine.
// Record never blocks, so auditing cannot slow down or fail an admin request;
// the cost is that events are dropped (and counted) when the queue is full.
// All methods are safe on a nil *Recorder, which records nothing.
type Recorder struct {
	store         entryStore
	exporter      Exporter
	log           *slog.Logger
	key           []byte
	chainID       string
	tenantID      string
	buf           chan Entry
	flushInterval time.Duration
	flushSize     int
	// Chain state, owned by the run goroutine.
	seq      int64
	prevHash string
	written  atomic.Int64
	failed   atomic.Int64
	dropped  atomic.Int64
	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}
	flushReq chan chan struct{}
}

// New starts a recorder writing to s and, when exp is non-nil, exporting
// committed rows through it. Call Stop on shutdown to drain the queue.
func New(s entryStore, exp Exporter, cfg Config) *Recorder {
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = 4096
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = 500 * time.Millisecond
	}
	if cfg.FlushSize <= 0 {
		cfg.FlushSize = 200
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.InstanceID == "" {
		cfg.InstanceID = NewInstanceID()
	}
	r := &Recorder{
		store:         s,
		exporter:      exp,
		log:           cfg.Logger,
		key:           cfg.Key,
		chainID:       cfg.InstanceID,
		tenantID:      cfg.DefaultTenantID,
		buf:           make(chan Entry, cfg.BufferSize),
		flushInterval: cfg.FlushInterval,
		flushSize:     cfg.FlushSize,
		stopCh:        make(chan struct{}),
		doneCh:        make(chan struct{}),
		flushReq:      make(chan chan struct{}),
	}
	go r.run()
	return r
}

// NewInstanceID returns a random writer id ("ins_" + 16 hex chars).
func NewInstanceID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "ins_" + hex.EncodeToString(b[:])
}

// Record queues ev. It never blocks.
func (r *Recorder) Record(ev Event) {
	if r == nil {
		return
	}
	e := toEntry(ev, r.tenantID)
	select {
	case r.buf <- e:
	default:
		r.dropped.Add(1)
	}
}

// Flush writes everything queued so far and waits for it (or ctx).
func (r *Recorder) Flush(ctx context.Context) {
	if r == nil {
		return
	}
	done := make(chan struct{})
	select {
	case r.flushReq <- done:
	case <-r.doneCh:
		return
	case <-ctx.Done():
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// Stop drains the queue (bounded by deadline), then stops the exporter.
func (r *Recorder) Stop(deadline time.Duration) {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() { close(r.stopCh) })
	select {
	case <-r.doneCh:
	case <-time.After(deadline):
		r.log.Warn("audit recorder drain timed out", "pending", len(r.buf))
	}
	if r.exporter != nil {
		r.exporter.Stop(deadline)
	}
}

// ExporterStats reports the OTLP exporter counters, or ok=false when no
// exporter is attached (export off). It lets /api/audit/status tell "export
// off" from "export on but the collector is refusing".
func (r *Recorder) ExporterStats() (ExporterStats, bool) {
	if r == nil || r.exporter == nil {
		return ExporterStats{}, false
	}
	st, ok := r.exporter.(interface{ Stats() ExporterStats })
	if !ok {
		return ExporterStats{}, false
	}
	return st.Stats(), true
}

// ChainID is this writer's hash chain id.
func (r *Recorder) ChainID() string {
	if r == nil {
		return ""
	}
	return r.chainID
}

// Key is the chain signing key, for Verify.
func (r *Recorder) Key() []byte {
	if r == nil {
		return nil
	}
	return r.key
}

// Stats reports recorder counters.
type Stats struct {
	Enabled bool   `json:"enabled"`
	ChainID string `json:"chain_id,omitempty"`
	Queued  int    `json:"queued"`
	Written int64  `json:"written"`
	Failed  int64  `json:"failed"`
	Dropped int64  `json:"dropped"`
}

// Stats returns a snapshot of the recorder's counters.
func (r *Recorder) Stats() Stats {
	if r == nil {
		return Stats{}
	}
	return Stats{
		Enabled: true,
		ChainID: r.chainID,
		Queued:  len(r.buf),
		Written: r.written.Load(),
		Failed:  r.failed.Load(),
		Dropped: r.dropped.Load(),
	}
}

func (r *Recorder) run() {
	defer close(r.doneCh)
	ticker := time.NewTicker(r.flushInterval)
	defer ticker.Stop()
	batch := make([]Entry, 0, r.flushSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		r.write(batch)
		batch = make([]Entry, 0, r.flushSize)
	}
	drain := func() {
		for {
			select {
			case e := <-r.buf:
				batch = append(batch, e)
				if len(batch) >= r.flushSize {
					flush()
				}
			default:
				return
			}
		}
	}
	for {
		select {
		case <-r.stopCh:
			drain()
			flush()
			return
		case <-ticker.C:
			flush()
		case done := <-r.flushReq:
			drain()
			flush()
			close(done)
		case e := <-r.buf:
			batch = append(batch, e)
			if len(batch) >= r.flushSize {
				flush()
			}
		}
	}
}

// write links batch into the chain and inserts it. The chain only advances
// when the insert commits, so a failed batch leaves no gap behind it: the
// next successful batch continues from the last committed row, and Verify
// reads exactly what is in the database.
func (r *Recorder) write(batch []Entry) {
	seq, prev := r.seq, r.prevHash
	for i := range batch {
		seq++
		batch[i].ChainID = r.chainID
		batch[i].ChainSeq = seq
		batch[i].PrevHash = prev
		batch[i].Hash = entryHash(r.key, batch[i])
		prev = batch[i].Hash
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.store.BatchInsert(ctx, batch); err != nil {
		r.failed.Add(int64(len(batch)))
		r.log.Warn("audit batch insert failed", "err", err, "count", len(batch))
		return
	}
	r.seq, r.prevHash = seq, prev
	r.written.Add(int64(len(batch)))
	if r.exporter != nil {
		r.exporter.Enqueue(batch)
	}
}
