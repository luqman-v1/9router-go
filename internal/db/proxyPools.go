package db

import (
	"database/sql"
	"crypto/rand"
	json "encoding/json/v2"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"9router/proxy/internal/handlerutil"
)

// ProxyPool represents a pool of proxy URLs for routing requests.
type ProxyPool struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	IsActive    bool     `json:"isActive"`
	URLs        []string `json:"urls"`
	Strategy    string   `json:"strategy"` // "round-robin" or "random"
	Type        string   `json:"type"`     // "http", "vercel", "cloudflare", "deno", "netlify"
	NoProxy     string   `json:"noProxy"`
	StrictProxy bool     `json:"strictProxy"`
	index       uint64   // atomic counter for round-robin
}

// IsEdgeRelay reports whether a pool is an edge relay (vercel/cloudflare/deno/
// netlify) rather than a dialable HTTP proxy. A relay is an ordinary HTTPS
// endpoint that forwards to whichever upstream the x-relay-target / x-relay-path
// headers name, so it must never be dialed with http.ProxyURL — that produces
// "malformed HTTP status code".
//
// One predicate for all four platforms: they share a contract, so a fifth edge
// runtime belongs here rather than in every caller's own list.
func (p *ProxyPool) IsEdgeRelay() bool {
	if p == nil {
		return false
	}
	switch p.Type {
	case "vercel", "cloudflare", "deno", "netlify":
		return true
	default:
		return false
	}
}

var proxyPoolCache sync.Map // map[string]*ProxyPool

// invalidateActivePoolIDs drops this Repo's rotation candidate cache. It
// stores a typed nil pointer rather than a nil slice: a nil slice asserts to
// []string successfully, which would leave the cache permanently empty.
func (r *Repo) invalidateActivePoolIDs() {
	r.activePoolIDs.Store((*[]string)(nil))
}

// GetProxyPool reads a proxy pool from the proxyPools table.
func (r *Repo) GetProxyPool(poolID string) (*ProxyPool, error) {
	var data string
	var isActive int
	err := r.db.QueryRow(
		`SELECT data, isActive FROM proxyPools WHERE id = ?`, poolID,
	).Scan(&data, &isActive)
	if err != nil {
		return nil, fmt.Errorf("proxy pool %s: %w", poolID, err)
	}

	var raw map[string]any
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return nil, fmt.Errorf("parse pool data: %w", err)
	}

	pool := &ProxyPool{
		ID:       poolID,
		IsActive: isActive == 1,
		Name:     handlerutil.GetString(raw, "name"),
		Strategy: handlerutil.GetString(raw, "strategy"),
	}

	if urls, ok := raw["urls"].([]any); ok {
		for _, u := range urls {
			if s, ok := u.(string); ok && s != "" {
				pool.URLs = append(pool.URLs, s)
			}
		}
	}
	if len(pool.URLs) == 0 {
		if singleURL := handlerutil.GetString(raw, "proxyUrl"); singleURL != "" {
			pool.URLs = append(pool.URLs, singleURL)
		}
	}

	pool.Type = handlerutil.GetString(raw, "type")
	if pool.Type == "" {
		pool.Type = "http"
	}
	pool.NoProxy = handlerutil.GetString(raw, "noProxy")
	if sp, ok := raw["strictProxy"].(bool); ok {
		pool.StrictProxy = sp
	}

	if pool.Strategy == "" {
		pool.Strategy = "round-robin"
	}

	if cached, ok := proxyPoolCache.Load(poolID); ok {
		if pool, ok := cached.(*ProxyPool); ok {
			return pool, nil
		}
	}
	// Store the freshly-read pool. If another goroutine won the race, return
	// its value instead of overwriting — never mutate a value in the cache,
	// since concurrent readers use it lock-free via NextURL.
	actual, _ := proxyPoolCache.LoadOrStore(poolID, pool)
	if pool, ok := actual.(*ProxyPool); ok {
		return pool, nil
	}
	return pool, nil
}

// NextURL returns the next proxy URL using round-robin selection.
func (p *ProxyPool) NextURL() string {
	if len(p.URLs) == 0 {
		return ""
	}
	idx := atomic.AddUint64(&p.index, 1)
	return p.URLs[idx%uint64(len(p.URLs))]
}

// ProxyPoolData is the JSON payload stored in the proxyPools.data column for
// deploy-type pools. Field order matches what the Next.js dashboard writes so
// the shared DB stays byte-compatible.
type ProxyPoolData struct {
	Name         string  `json:"name"`
	ProxyURL     string  `json:"proxyUrl"`
	NoProxy      string  `json:"noProxy"`
	Type         string  `json:"type"`
	StrictProxy  bool    `json:"strictProxy"`
	LastTestedAt *string `json:"lastTestedAt"`
	LastError    *string `json:"lastError"`
}

// InsertProxyPool inserts a new proxy pool row and returns the pool object in
// the same shape as the dashboard's createProxyPool result.
func (r *Repo) InsertProxyPool(d ProxyPoolData) (map[string]any, error) {
	id := randomID()
	now := time.Now().UTC().Format(time.RFC3339)
	dataBytes, err := json.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("marshal pool data: %w", err)
	}
	_, err = r.db.Exec(
		`INSERT INTO proxyPools (id, isActive, testStatus, data, createdAt, updatedAt) VALUES (?, ?, ?, ?, ?, ?)`,
		id, 1, "unknown", string(dataBytes), now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert proxy pool: %w", err)
	}
	r.invalidateActivePoolIDs()
	return map[string]any{
		"id":           id,
		"name":         d.Name,
		"proxyUrl":     d.ProxyURL,
		"noProxy":      d.NoProxy,
		"type":         d.Type,
		"strictProxy":  d.StrictProxy,
		"isActive":     true,
		"testStatus":   "unknown",
		"lastTestedAt": nil,
		"lastError":    nil,
		"createdAt":    now,
		"updatedAt":    now,
	}, nil
}

// randomID returns a random hex id (uuid-shaped, like the dashboard's uuidv4).
func randomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("pool-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", b)
}

// ListProxyPools returns all proxy pools with their data unpacked.
//
// testStatus is nullable in the shared schema and this database is written by
// both dashboards, so a pool row can legitimately carry NULL there. Scanning it
// as a plain string would fail the whole read and blank the tab over one pool
// that has simply never been probed; the missing value reads as "" here. Any
// other scan failure still aborts: that is a broken read, not an absent value.
func (r *Repo) ListProxyPools() ([]map[string]any, error) {
	rows, err := r.db.Query(`SELECT id, isActive, testStatus, data, createdAt, updatedAt FROM proxyPools ORDER BY createdAt DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []map[string]any
	for rows.Next() {
		var id, dataStr, createdAt, updatedAt string
		var isActiveInt int
		var testStatus sql.NullString
		if err := rows.Scan(&id, &isActiveInt, &testStatus, &dataStr, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan proxyPools: %w", err)
		}
		var poolData map[string]any
		if err := json.Unmarshal([]byte(dataStr), &poolData); err != nil {
			poolData = make(map[string]any)
		}
		poolData["id"] = id
		poolData["isActive"] = (isActiveInt == 1)
		poolData["testStatus"] = testStatus.String
		poolData["createdAt"] = createdAt
		poolData["updatedAt"] = updatedAt
		list = append(list, poolData)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate proxyPools: %w", err)
	}
	if list == nil {
		list = []map[string]any{}
	}
	return list, nil
}

// ActivePoolIDs returns the ids of every active pool that carries a URL, in
// creation order. Proxy rotation picks from this list, so it is cached: the
// list is read on the forwarding hot path and changes only when a pool is
// written. Every mutation below drops the cache, so a stale entry cannot
// outlive a change to the pool table.
//
// The cache is per-Repo rather than package state because the answer belongs
// to one database: two Repos over different handles would otherwise read each
// other's pools.
func (r *Repo) ActivePoolIDs() []string {
	// A cached-but-nil pointer means invalidated: dereferencing it would panic,
	// so both the empty list and the miss fall through to a fresh read.
	if cached, ok := r.activePoolIDs.Load().(*[]string); ok && cached != nil {
		return *cached
	}
	ids := r.queryActivePoolIDs()
	r.activePoolIDs.Store(&ids)
	return ids
}

// queryActivePoolIDs reads the candidate list straight from the table. The
// filter runs in SQL and the payload decodes into a typed struct, so a database
// holding many pools does not pay for rows rotation can never pick.
func (r *Repo) queryActivePoolIDs() []string {
	rows, err := r.db.Query(`SELECT id, data FROM proxyPools WHERE isActive = 1 ORDER BY createdAt`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id, data string
		if err := rows.Scan(&id, &data); err != nil {
			continue
		}
		var payload struct {
			ProxyURL string   `json:"proxyUrl"`
			URLs     []string `json:"urls"`
		}
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			continue
		}
		if poolPayloadHasURL(payload.ProxyURL, payload.URLs) {
			ids = append(ids, id)
		}
	}
	return ids
}

// poolPayloadHasURL reports whether a pool carries at least one URL. A pool
// without one cannot serve a request, so rotation must not select it — that
// would hand a share of the traffic to a pool the resolver then refuses.
func poolPayloadHasURL(proxyURL string, urls []string) bool {
	if strings.TrimSpace(proxyURL) != "" {
		return true
	}
	for _, u := range urls {
		if strings.TrimSpace(u) != "" {
			return true
		}
	}
	return false
}

// IsProxyPoolRotation reports whether a saved value means "rotate across the
// active pools". Only the two values the provider card offers are accepted: it
// writes exactly `round-robin` and `random`. `sticky` is deliberately not
// accepted — it is a connection-rotation value, and taking it here used to
// serve round-robin, promising an affinity this rotation does not keep.
func IsProxyPoolRotation(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "round-robin", "roundrobin", "random":
		return true
	}
	return false
}

// DeleteProxyPool removes a proxy pool by id.
func (r *Repo) DeleteProxyPool(id string) error {
	_, err := r.db.Exec(`DELETE FROM proxyPools WHERE id = ?`, id)
	proxyPoolCache.Delete(id)
	r.invalidateActivePoolIDs()
	return err
}

// UpdateProxyPool updates a proxy pool's data, isActive, and testStatus.
func (r *Repo) UpdateProxyPool(id string, updates map[string]any) error {
	var dataStr string
	var isActive int
	var testStatus string
	err := r.db.QueryRow(`SELECT data, isActive, testStatus FROM proxyPools WHERE id = ?`, id).Scan(&dataStr, &isActive, &testStatus)
	if err != nil {
		return err
	}
	var existing map[string]any
	_ = json.Unmarshal([]byte(dataStr), &existing)
	if existing == nil {
		existing = make(map[string]any)
	}
	for k, v := range updates {
		if k == "isActive" {
			if b, ok := v.(bool); ok {
				if b {
					isActive = 1
				} else {
					isActive = 0
				}
			}
		} else if k == "testStatus" {
			if s, ok := v.(string); ok {
				testStatus = s
			}
		} else if k != "id" && k != "createdAt" && k != "updatedAt" {
			existing[k] = v
		}
	}
	updatedBytes, _ := json.Marshal(existing)
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = r.db.Exec(`UPDATE proxyPools SET data = ?, isActive = ?, testStatus = ?, updatedAt = ? WHERE id = ?`,
		string(updatedBytes), isActive, testStatus, now, id)
	proxyPoolCache.Delete(id)
	r.invalidateActivePoolIDs()
	return err
}

// SetProxyPoolStatus updates only testStatus and lastTestedAt on a pool.
func (r *Repo) SetProxyPoolStatus(id string, status string, latencyMs int64) error {
	var dataStr string
	var isActive int
	var currentStatus string
	err := r.db.QueryRow(`SELECT data, isActive, testStatus FROM proxyPools WHERE id = ?`, id).Scan(&dataStr, &isActive, &currentStatus)
	if err != nil {
		return err
	}
	var existing map[string]any
	_ = json.Unmarshal([]byte(dataStr), &existing)
	if existing == nil {
		existing = make(map[string]any)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	existing["lastTestedAt"] = now
	existing["latency"] = latencyMs
	updatedBytes, _ := json.Marshal(existing)
	_, err = r.db.Exec(`UPDATE proxyPools SET data = ?, testStatus = ?, updatedAt = ? WHERE id = ?`,
		string(updatedBytes), status, now, id)
	proxyPoolCache.Delete(id)
	return err
}
