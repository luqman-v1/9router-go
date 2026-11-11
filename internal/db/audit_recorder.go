package db

import (
	"encoding/base64"

	"9router/proxy/internal/audit"
	"9router/proxy/internal/vault"
)

// The audit recorder lives on the Repo, the same way the vault does: it is
// process state bound to one database, and the router is built before the
// app's boot hook starts the recorder's writer, so a handler must read it per
// request rather than snapshot it at construction (a snapshot taken in
// ProvideRouter would be nil in production — the exact trap ModelHealth
// documents in the dashboard handler).
//
// A nil recorder is legal: unit tests that build a router by hand have none,
// and the middleware and endpoints treat it as "auditing off" rather than
// failing the request.

// SetAuditRecorder attaches the running audit recorder. A nil recorder
// detaches it.
func (r *Repo) SetAuditRecorder(rec *audit.Recorder) {
	if rec == nil {
		r.auditRecorder.Store((*audit.Recorder)(nil))
		return
	}
	// Store a non-nil *audit.Recorder pointer. Storing the typed nil directly
	// is indistinguishable from a missing value on Load, so the nil case goes
	// through the explicit branch above.
	r.auditRecorder.Store(rec)
}

// AuditRecorder returns the attached audit recorder, or nil when auditing is
// off (no recorder was started).
func (r *Repo) AuditRecorder() *audit.Recorder {
	v, _ := r.auditRecorder.Load().(*audit.Recorder)
	return v
}

// GetAuditRetentionDays reports how many days of audit rows to keep, read from
// the operator's settings. Zero (the default, and the value when the field is
// absent) means "keep forever": the retention sweeper is then not started, so
// a fresh install does not start deleting rows the operator never asked to
// prune.
func (r *Repo) GetAuditRetentionDays() int {
	raw, err := r.GetSettingsRaw()
	if err != nil || raw == nil {
		return 0
	}
	switch v := raw["audit_retention_days"].(type) {
	case int:
		if v < 0 {
			return 0
		}
		return v
	case int64:
		if v < 0 {
			return 0
		}
		return int(v)
	case float64:
		if v < 0 {
			return 0
		}
		return int(v)
	}
	return 0
}

// AuditChainKey returns the persisted chain-signing key (raw 32 bytes) and
// whether it is present. It is stored base64-encoded in the settings row.
func (r *Repo) AuditChainKey() ([]byte, bool) {
	raw, err := r.GetSettingsRaw()
	if err != nil || raw == nil {
		return nil, false
	}
	s, ok := raw["audit_chain_key"].(string)
	if !ok || s == "" {
		return nil, false
	}
	key, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(key) != vault.KeySize {
		return nil, false
	}
	return key, true
}

// SetAuditChainKey persists the chain-signing key so the hash chain stays
// signed by the same key across restarts.
func (r *Repo) SetAuditChainKey(key []byte) error {
	return r.UpdateSettingsRaw(map[string]any{
		"audit_chain_key": base64.StdEncoding.EncodeToString(key),
	})
}
