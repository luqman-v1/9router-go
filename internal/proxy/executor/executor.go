package executor

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

// LeaseStore is the cross-process session coordination backend,
// implemented by *db.Repo over upstream_leases. A nil store means
// single-process mode: memory cache only, exactly like before.
type LeaseStore interface {
	ReadLease(scope, key string) (*db.Lease, error)
	AcquireLease(scope, key, value string, ttl time.Duration) (bool, error)
	ReleaseLease(scope, key, value string) error
}

// Request holds all inputs for an executor.
type Request struct {
	Ctx            context.Context
	Client         *http.Client
	Config         *providers.ProviderConfig
	Provider       string // canonical provider id; provider-scoped wire rules key off it
	APIKey         string
	Body           []byte
	IsStream       bool
	ClaudeClient   bool              // /v1/messages client: body is Claude Messages, response must be translated back
	TranslateResp  bool
	ConnectionID   string            // for OAuth refresh by fallback
	SessionID      string            // client session / conversation id
	ConnData       map[string]any    // connection providerSpecificData (e.g. fingerprintId for client cloaking)
	Leases         LeaseStore        // cross-process lease backend (nil = memory only)
	ModelName      string            // extracted model name
	Endpoint       string            // custom URL override (azure)
	ToolNameMap    map[string]string // claude OAuth tool-cloak map (suffixed -> original)
	UpstreamClaude bool              // upstream responds in Claude format while client sent OpenAI format
	// LiftReasoningAliases marks a lane whose upstream streams thinking under
	// the vendor aliases (reasoning / reasoning_details[]) instead of
	// reasoning_content (OpenCode Free's MiMo route); the passthrough adds
	// reasoning_content so OpenAI-shaped clients see the thinking.
	LiftReasoningAliases bool
	ResponseBuf          io.Writer // writer to capture response text for token estimation & logging
	StartTime            time.Time // request start time for TTFT tracking
	TTFT                 *int64    // pointer to TTFT metric (ms to first chunk)
}

// markTTFT stamps the time-to-first-token on the first emitted chunk, once.
// The usage recorder reads metrics.TTFT into latency.ttft for the dashboard
// detail row; a stream that never calls this records TTFT as unseen (0),
// which renders as "— (non-stream)" and is excluded from the TTFT averages.
// Every streaming executor must call it before flushing the first
// client-visible frame — the semantic first token, not the first byte or an
// empty role-only delta. startTime comes from req.StartTime so the value
// matches what the chat handler measures.
func markTTFT(req *Request, startTime time.Time) {
	if req == nil || req.TTFT == nil || *req.TTFT != 0 {
		return
	}
	if startTime.IsZero() {
		startTime = req.StartTime
	}
	if startTime.IsZero() {
		startTime = time.Now()
	}
	*req.TTFT = time.Since(startTime).Milliseconds()
}

// Executor forwards a request upstream and writes the response.
type Executor func(w http.ResponseWriter, req *Request) error

type executorFactory func() Executor

var (
	registryMu sync.RWMutex
	registry   = map[string]executorFactory{}
)

// Register adds an executor factory for the given provider name.
func Register(provider string, fn executorFactory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[provider] = fn
}

// Get returns the executor for the given provider, or nil if not found.
func Get(provider string) Executor {
	registryMu.RLock()
	defer registryMu.RUnlock()
	fn, ok := registry[provider]
	if !ok {
		return nil
	}
	return fn()
}

// Default returns the default OpenAI-compat executor.
func Default() Executor { return ForwardOpenAI }

// Release clears internal byte buffers and maps so memory can be collected
// immediately following upstream dispatch.
func (r *Request) Release() {
	if r == nil {
		return
	}
	r.Body = nil
	r.ConnData = nil
	r.ToolNameMap = nil
}
