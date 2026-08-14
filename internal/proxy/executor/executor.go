package executor

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"9router/proxy/internal/providers"
)

// Request holds all inputs for an executor.
type Request struct {
	Ctx            context.Context
	Client         *http.Client
	Config         *providers.ProviderConfig
	APIKey         string
	Body           []byte
	IsStream       bool
	TranslateResp  bool
	ConnectionID   string    // for OAuth refresh by fallback
	SessionID      string    // client session / conversation id
	ProjectID      string    // for gemini-native (antigravity)
	ModelName      string    // extracted model name
	Endpoint       string    // custom URL override (azure)
	ResponseBuf    io.Writer // writer to capture response text for token estimation & logging
	StartTime      time.Time // request start time for TTFT tracking
	TTFT           *int64    // pointer to TTFT metric (ms to first chunk)
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
