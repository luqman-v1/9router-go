package db

import (
	"encoding/json"
	"fmt"
	"sync/atomic"
)

// ProxyPool represents a pool of proxy URLs for routing requests.
type ProxyPool struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	IsActive bool     `json:"isActive"`
	URLs     []string `json:"urls"`
	Strategy string   `json:"strategy"` // "round-robin" or "random"
	index    uint64   // atomic counter for round-robin
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
		Name:     getString(raw, "name"),
		Strategy: getString(raw, "strategy"),
	}

	if urls, ok := raw["urls"].([]any); ok {
		for _, u := range urls {
			if s, ok := u.(string); ok {
				pool.URLs = append(pool.URLs, s)
			}
		}
	}

	if pool.Strategy == "" {
		pool.Strategy = "round-robin"
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

func getString(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
