package audit

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// Entry is one stored audit row: the record the hash chain signs and the
// columns audit_log persists. It is defined here rather than in the database
// layer so the chain algorithm, the recorder and the store all sign and read
// the same shape without the audit package depending on db (db depends on
// audit, never the reverse).
//
// CreatedAt carries microsecond precision because entryHash hashes
// UnixMicro: the stored value and the hash must agree to the microsecond or
// a later Verify would report every row as modified.
type Entry struct {
	ID           string // log.record.uid
	TenantID     string
	Actor        string // user.id: dashboard session, api key id, or "system"
	ActorType    string // dashboard | api_key | system
	Action       string // event.name suffix, e.g. keys.create, auth.login
	Target       string // id of the resource acted on
	ResourceType string // keys, accounts, chains, ...
	Outcome      string // success | failure | denied
	Severity     string // INFO | WARN | ERROR
	ClientIP     string
	UserAgent    string
	HTTPMethod   string
	HTTPRoute    string
	StatusCode   int
	RequestID    string
	TraceID      string // W3C trace id (hex) from the caller's traceparent
	SpanID       string
	Detail       string // JSON object of extra attributes (never secrets)
	ChainID      string // writer instance whose hash chain holds the row
	ChainSeq     int64
	PrevHash     string
	Hash         string
	CreatedAt    time.Time
}

// newEntryID returns a random row id ("au_" + 32 hex chars).
func newEntryID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "au_" + hex.EncodeToString(b[:])
}

// truncate bounds one field at its storage cap.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
