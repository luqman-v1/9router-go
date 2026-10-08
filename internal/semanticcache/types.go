package semanticcache

import (
	"time"
)

// Entry represents a cached chat completion response.
type Entry struct {
	Key          string
	Model        string
	ResponseBody []byte
	ContentType  string
	StoredAt     time.Time
	HitCount     int64
	TokensSaved  int64
}

// EntryMeta represents metadata of a cached entry for the dashboard and API.
type EntryMeta struct {
	ID          string `json:"id"`
	Signature   string `json:"signature"`
	Model       string `json:"model"`
	HitCount    int64  `json:"hit_count"`
	TokensSaved int64  `json:"tokens_saved"`
	CreatedAt   string `json:"created_at"`
	ExpiresAt   string `json:"expires_at"`
}

// Stats represents overall semantic cache metrics.
type Stats struct {
	MemoryEntries int    `json:"memoryEntries"`
	DBEntries     int    `json:"dbEntries"`
	Hits          int64  `json:"hits"`
	Misses        int64  `json:"misses"`
	HitRate       string `json:"hitRate"`
	TokensSaved   int64  `json:"tokensSaved"`
}

// Config configures semantic/prompt cache behavior.
type Config struct {
	Enabled             bool
	SimilarityThreshold float64
	TTL                 time.Duration
	MaxEntries          int
}
