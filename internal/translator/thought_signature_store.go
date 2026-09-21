package translator

import (
	"strings"
	"sync"
	"time"
)

const (
	maxMemorySignatures = 2000
	memoryTTL           = time.Hour
)

type signatureEntry struct {
	signature string
	family    string
	expiresAt time.Time
}

// SignatureFamily returns the normalized model family ("claude", "gemini", or original)
// so signatures are not replayed to incompatible model families (upstream parity with bc3be0cb).
func SignatureFamily(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return ""
	}
	if strings.Contains(m, "claude") {
		return "claude"
	}
	if strings.Contains(m, "gemini") {
		return "gemini"
	}
	return m
}

func isFamilyCompatible(entryFamily, targetFamily string) bool {
	return entryFamily == "" || targetFamily == "" || entryFamily == targetFamily
}

type thoughtSignatureStore struct {
	mu      sync.RWMutex
	entries map[string]signatureEntry
	order   []string // FIFO / LRU approximation
}

var globalThoughtSigStore = &thoughtSignatureStore{
	entries: make(map[string]signatureEntry),
	order:   make([]string, 0, maxMemorySignatures),
}

func (s *thoughtSignatureStore) pruneExpiredLocked(now time.Time) {
	for k, v := range s.entries {
		if now.After(v.expiresAt) {
			delete(s.entries, k)
		}
	}

	// Bound max capacity
	if len(s.entries) > maxMemorySignatures {
		newOrder := make([]string, 0, len(s.entries))
		for _, k := range s.order {
			if _, ok := s.entries[k]; ok {
				newOrder = append(newOrder, k)
			}
		}
		for len(newOrder) > maxMemorySignatures {
			oldest := newOrder[0]
			newOrder = newOrder[1:]
			delete(s.entries, oldest)
		}
		s.order = newOrder
	}
}

// StoreGeminiThoughtSignature stores a thought signature for a tool_call_id with optional session namespace and model family.
func StoreGeminiThoughtSignature(toolCallID, signature, sessionID string, model ...string) {
	if toolCallID == "" || signature == "" {
		return
	}

	var family string
	if len(model) > 0 && model[0] != "" {
		family = SignatureFamily(model[0])
	}
	now := time.Now()
	exp := now.Add(memoryTTL)

	// Clean toolCallID if it contains __ts__ suffix
	cleanID := toolCallID
	if idx := strings.LastIndex(toolCallID, "__ts__"); idx != -1 {
		cleanID = toolCallID[:idx]
	}

	globalThoughtSigStore.mu.Lock()
	defer globalThoughtSigStore.mu.Unlock()

	globalThoughtSigStore.pruneExpiredLocked(now)

	keys := []string{toolCallID}
	if cleanID != toolCallID {
		keys = append(keys, cleanID)
	}

	if sessionID != "" {
		keys = append(keys, sessionID+":"+toolCallID)
		if cleanID != toolCallID {
			keys = append(keys, sessionID+":"+cleanID)
		}
	}

	for _, k := range keys {
		if _, exists := globalThoughtSigStore.entries[k]; !exists {
			globalThoughtSigStore.order = append(globalThoughtSigStore.order, k)
		}
		globalThoughtSigStore.entries[k] = signatureEntry{
			signature: signature,
			family:    family,
			expiresAt: exp,
		}
	}
}

// GetGeminiThoughtSignature retrieves a thought signature by tool_call_id (checking session namespace first and matching model family).
func GetGeminiThoughtSignature(toolCallID, sessionID string, model ...string) string {
	if toolCallID == "" {
		return ""
	}

	var targetFamily string
	if len(model) > 0 && model[0] != "" {
		targetFamily = SignatureFamily(model[0])
	}
	cleanID := toolCallID
	if idx := strings.LastIndex(toolCallID, "__ts__"); idx != -1 {
		cleanID = toolCallID[:idx]
	}

	now := time.Now()

	globalThoughtSigStore.mu.RLock()
	defer globalThoughtSigStore.mu.RUnlock()

	// 1. Session-scoped check
	if sessionID != "" {
		if entry, ok := globalThoughtSigStore.entries[sessionID+":"+toolCallID]; ok && now.Before(entry.expiresAt) && isFamilyCompatible(entry.family, targetFamily) {
			return entry.signature
		}
		if cleanID != toolCallID {
			if entry, ok := globalThoughtSigStore.entries[sessionID+":"+cleanID]; ok && now.Before(entry.expiresAt) && isFamilyCompatible(entry.family, targetFamily) {
				return entry.signature
			}
		}
	}

	// 2. Global toolCallID check
	if entry, ok := globalThoughtSigStore.entries[toolCallID]; ok && now.Before(entry.expiresAt) && isFamilyCompatible(entry.family, targetFamily) {
		return entry.signature
	}
	if cleanID != toolCallID {
		if entry, ok := globalThoughtSigStore.entries[cleanID]; ok && now.Before(entry.expiresAt) && isFamilyCompatible(entry.family, targetFamily) {
			return entry.signature
		}
	}
	return ""
}

// ClearGeminiThoughtSignatures resets the store (useful for tests).
func ClearGeminiThoughtSignatures() {
	globalThoughtSigStore.mu.Lock()
	defer globalThoughtSigStore.mu.Unlock()
	clear(globalThoughtSigStore.entries)
	globalThoughtSigStore.order = globalThoughtSigStore.order[:0]
}
