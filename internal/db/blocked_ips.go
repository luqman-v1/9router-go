package db

import (
	json "encoding/json/v2"
	"fmt"
	"net/netip"
	"strings"
	"time"
)

// ScopeBlockedIPs is the kv scope holding the blocked-IP blacklist.
const ScopeBlockedIPs = "blocked_ips"

// BlockedIPEntry is one row of the blocked-IP blacklist.
type BlockedIPEntry struct {
	IP        string `json:"ip"`
	Country   string `json:"country"`
	BlockedAt string `json:"blockedAt"`
	Reason    string `json:"reason"`
	BlockedBy string `json:"blockedBy"`
}

// normalizeBlockedIP parses and canonicalizes an IP string.
func normalizeBlockedIP(raw string) (string, error) {
	addr, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("invalid ip %q: %w", raw, err)
	}
	return addr.Unmap().String(), nil
}

// ListBlockedIPs returns every entry in the blocked-IP blacklist.
func (r *Repo) ListBlockedIPs() ([]BlockedIPEntry, error) {
	kv, err := r.GetKVScope(ScopeBlockedIPs)
	if err != nil {
		return nil, err
	}
	out := make([]BlockedIPEntry, 0, len(kv))
	for _, v := range kv {
		var e BlockedIPEntry
		if err := json.Unmarshal([]byte(v), &e); err != nil {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// BlockIP adds an IP to the blacklist (upsert).
func (r *Repo) BlockIP(ip, country, reason, by string) (BlockedIPEntry, error) {
	norm, err := normalizeBlockedIP(ip)
	if err != nil {
		return BlockedIPEntry{}, err
	}
	entry := BlockedIPEntry{
		IP:        norm,
		Country:   country,
		BlockedAt: time.Now().UTC().Format(time.RFC3339),
		Reason:    reason,
		BlockedBy: by,
	}
	b, err := json.Marshal(entry)
	if err != nil {
		return BlockedIPEntry{}, fmt.Errorf("marshal blocked ip: %w", err)
	}
	if err := r.SetKV(ScopeBlockedIPs, norm, string(b)); err != nil {
		return BlockedIPEntry{}, err
	}
	return entry, nil
}

// UnblockIP removes an IP from the blacklist.
func (r *Repo) UnblockIP(ip string) error {
	norm, err := normalizeBlockedIP(ip)
	if err != nil {
		return err
	}
	return r.DeleteKV(ScopeBlockedIPs, norm)
}

// IsIPBlocked reports whether an IP is on the blacklist.
func (r *Repo) IsIPBlocked(ip string) (bool, error) {
	norm, err := normalizeBlockedIP(ip)
	if err != nil {
		return false, err
	}
	v, err := r.GetKV(ScopeBlockedIPs, norm)
	if err != nil {
		return false, err
	}
	return v != "", nil
}

