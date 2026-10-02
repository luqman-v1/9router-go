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
	APIKey         string
	Body           []byte
	IsStream       bool
	ClaudeClient   bool              // /v1/messages client: body is Claude Messages, response must be translated back
	TranslateResp  bool
	ConnectionID   string            // for OAuth refresh by fallback
	SessionID      string            // client session / conversation id
	ConnData       map[string]any    // connection providerSpecificData (e.g. fingerprintId for client cloaking)
	Leases         LeaseStore        // cross-process lease backend (nil = memory only)
	ProjectID      string            // for gemini-native (antigravity)
	ModelName      string            // extracted model name
	Endpoint       string            // custom URL override (azure)
	ToolNameMap    map[string]string // claude OAuth tool-cloak map (suffixed -> original)
	UpstreamClaude bool              // upstream responds in Claude format while client sent OpenAI format
	ResponseBuf    io.Writer         // writer to capture response text for token estimation & logging
	StartTime      time.Time         // request start time for TTFT tracking
	TTFT           *int64            // pointer to TTFT metric (ms to first chunk)
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

// IsGeminiNative checks if a config uses gemini-native format.
func IsGeminiNative(cfg *providers.ProviderConfig) bool { return cfg.Format == "gemini-native" }
