package executor

import (
	"net/http"

	"9router/proxy/internal/providers"
)

// Request holds all inputs for an executor.
type Request struct {
	Client         *http.Client
	Config         *providers.ProviderConfig
	APIKey         string
	Body           []byte
	IsStream       bool
	TranslateResp  bool
	ConnectionID   string // for OAuth refresh by fallback
	ProjectID      string // for gemini-native (antigravity)
	ModelName      string // extracted model name
	Endpoint       string // custom URL override (azure)
}

// Executor forwards a request upstream and writes the response.
type Executor func(w http.ResponseWriter, req *Request) error

type executorFactory func() Executor

var registry = map[string]executorFactory{}

// Register adds an executor factory for the given provider name.
func Register(provider string, fn executorFactory) {
	registry[provider] = fn
}

// Get returns the executor for the given provider, or nil if not found.
func Get(provider string) Executor {
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
