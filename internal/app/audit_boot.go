package app

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"time"

	"9router/proxy/internal/audit"
	"9router/proxy/internal/db"
	"9router/proxy/internal/log"
	"9router/proxy/internal/updater"
	"9router/proxy/internal/vault"
)

// auditStop stops the retention sweeper and the recorder (which drains its
// queue). Bounded: a stuck batch insert must not hold the whole shutdown.
type auditStop func()

// startAuditTrail starts the tamper-evident audit trail for this process:
// the recorder's writer goroutine, and — when a retention window is
// configured — the hourly sweeper. It returns a stop function the caller
// invokes on shutdown, after the HTTP server has stopped taking requests, so
// the requests that were just served are still in the queue to be written.
//
// The chain-signing key must be stable across restarts or Verify would see a
// different key than the one that signed the rows still in the database:
// every row would read as "modified". Two sources, in order:
//  1. ROUTER_MASTER_KEY, when set and valid: the key is derived from the
//     credential master key, so the log is only re-computable by someone who
//     can already read the credentials.
//  2. Otherwise a random 32-byte key persisted in the settings table, created
//     on first boot and reused after — a fresh install has no master key, but
//     it still gets a stable chain.
func startAuditTrail(repo *db.Repo) auditStop {
	key, source, err := resolveAuditChainKey(repo)
	if err != nil {
		// The audit trail is a compliance feature, not a load-bearing one: a
		// broken key must not take the gateway down. Auditing stays off and
		// the operator sees it in the log and in /api/audit/status.
		log.Error("audit", "chain key unavailable; audit trail disabled", "error", err)
		return func() {}
	}
	// OTLP export, on only when an endpoint is configured: the committed rows
	// are shipped to a collector in the standard OTel logs shape so a SIEM
	// importer (Splunk, Elastic, Loki) can ingest them without this repo's
	// schema. Export off is the default: a fresh install ships nothing.
	var exporter audit.Exporter
	version, instanceID := updater.CurrentVersion, auditInstanceID()
	if cfg, ok, err := audit.ResolveOTLPConfig(version, instanceID, os.Getenv); err != nil {
		log.Warn("audit", "OTLP export disabled by bad config", "error", err)
	} else if ok {
		exporter = audit.NewOTLPExporter(cfg)
		log.Info("audit", "OTLP export enabled", "endpoint", cfg.URL)
	}
	rec := audit.New(repo.Audit(), exporter, audit.Config{
		Key:             key,
		DefaultTenantID: db.AuditTenant,
		Logger:          slog.Default(),
	})
	// Attach before the first request can be recorded: the middleware reads
	// this per request, so a slow operator click that lands before the attach
	// is "auditing off" for that one request, not a lost row.
	repo.SetAuditRecorder(rec)

	var sweeper *audit.Sweeper
	if days := repo.GetAuditRetentionDays(); days > 0 {
		sweeper = audit.NewSweeper(repo.Audit(), slog.Default(), time.Duration(days)*24*time.Hour)
		sweeper.Start()
	}
	log.Info("audit", "audit trail started", "key_source", source, "chain_id", rec.ChainID(), "retention_days", repo.GetAuditRetentionDays())
	return func() {
		sweeper.Stop(3 * time.Second)
		rec.Stop(5 * time.Second)
		repo.SetAuditRecorder(nil)
	}
}

// auditInstanceID is the OTel service.instance.id resource attribute: the host
// name, which is the conventional value and is what the dashboard already
// reports. A blank hostname (sandboxed test) degrades to a stable local id.
func auditInstanceID() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "9router-go"
}

// resolveAuditChainKey returns the chain-signing key and a short name of its
// source ("master_key" or "settings"), or an error when neither source is
// usable.
func resolveAuditChainKey(repo *db.Repo) (key []byte, source string, err error) {
	if k, err := masterKeyFromEnv(vault.MasterKeyEnv); err == nil {
		return audit.DeriveKey(k), "master_key", nil
	}
	// No usable master key: use the persisted per-install key.
	if v, ok := repo.AuditChainKey(); ok && len(v) == vault.KeySize {
		return audit.DeriveKey(v), "settings", nil
	}
	fresh := make([]byte, vault.KeySize)
	if _, err := rand.Read(fresh); err != nil {
		return nil, "", fmt.Errorf("audit: generate chain key: %w", err)
	}
	if err := repo.SetAuditChainKey(fresh); err != nil {
		// Not persisted, the key would not survive a restart and the chain
		// would verify against the wrong key after one: fail closed (auditing
		// off) rather than start a chain that lies.
		return nil, "", fmt.Errorf("audit: persist chain key: %w", err)
	}
	return audit.DeriveKey(fresh), "settings", nil
}

// masterKeyFromEnv decodes ROUTER_MASTER_KEY exactly the vault does. It is
// local rather than an export because the vault keeps its decode private and
// the audit trail only needs the raw key, not the sealing machinery.
func masterKeyFromEnv(name string) ([]byte, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return nil, fmt.Errorf("audit: %s not set", name)
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("audit: %s is not base64: %w", name, err)
	}
	if len(key) != vault.KeySize {
		return nil, fmt.Errorf("audit: %s must decode to %d bytes, got %d", name, vault.KeySize, len(key))
	}
	return key, nil
}
