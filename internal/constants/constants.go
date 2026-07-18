// Package constants provides application-wide constants to avoid magic strings and magic numbers.
package constants

// HTTP Content Types
const (
	ContentTypeJSON        = "application/json"
	ContentTypeEventStream = "text/event-stream"
)

// HTTP Headers
const (
	HeaderAuthorization   = "Authorization"
	HeaderContentType     = "Content-Type"
	HeaderCacheControl    = "Cache-Control"
	HeaderConnection      = "Connection"
	HeaderAccept          = "Accept"
	HeaderXAPIKey         = "X-API-Key"
	HeaderUserAgent       = "User-Agent"
)

// Auth token prefix for header value (includes trailing space)
const (
	AuthPrefixBearer = "Bearer "
)

// Auth scheme names (used in ProviderConfig.AuthScheme)
const (
	AuthSchemeBearer = "bearer"
	AuthSchemeRaw    = "raw"
)

// Cache / Connection Directives
const (
	CacheNoCache  = "no-cache"
	ConnKeepAlive = "keep-alive"
)

// Default File Permissions
const (
	FilePermDir  = 0755
	FilePermFile = 0644
	FilePermKey  = 0600
)

// SSE Format
const (
	SSEFormat = "data: %s\n\n"
)

// Lock Durations (seconds)
const (
	DefaultLockDuration429 = 60
	DefaultLockDuration401 = 120
)

// Truncation Limits
const (
	MaxResponseContentLen = 10000
	MaxMessageContentLen  = 500
	MaxLoggedMessages     = 20
)

// Buffer Sizes
const (
	StreamReadBuffer = 4096 // 4 KB read buffer for streaming
)
