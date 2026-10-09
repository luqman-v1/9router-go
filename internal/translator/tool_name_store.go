package translator

import (
	"sync"
	"time"
)

// toolNameStore remembers which tool a tool_call id belongs to. The streaming
// translator mints the id for Gemini, so the name is otherwise only known to
// the code that emitted it; a client that echoes back role:"tool" messages
// without repeating the assistant tool_calls leaves the reverse direction with
// nothing but the id to go on.
//
// This exists because the id is opaque. Gemini 3.x sends its own
// functionCall.id (a token like "call_abc123"), so a name parsed back out of
// the id is not a name. Storing the pairing sidesteps that entirely and mirrors
// globalThoughtSigStore, which already keys the thought signature the same way.
type toolNameStore struct {
	mu      sync.RWMutex
	entries map[string]toolNameEntry
	order   []string // FIFO / LRU approximation
}

type toolNameEntry struct {
	name      string
	expiresAt time.Time
}

var globalToolNameStore = &toolNameStore{
	entries: make(map[string]toolNameEntry),
	order:   make([]string, 0, maxMemorySignatures),
}

// pruneLocked drops expired entries and rebuilds order so it never holds a key
// that entries no longer has. The rebuild is gated on `pruned`, not only on
// capacity: an id that expires and is later reused is appended again, so without
// it order accumulates duplicates forever whenever traffic stays under the cap
// and the capacity branch never runs. Invariant: len(order) <= len(entries).
func (s *toolNameStore) pruneLocked(now time.Time) {
	pruned := false
	for k, v := range s.entries {
		if now.After(v.expiresAt) {
			delete(s.entries, k)
			pruned = true
		}
	}
	if !pruned && len(s.entries) <= maxMemorySignatures {
		return
	}
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

// toolCallStoreKeys returns every key a tool_call id resolves under: the raw
// id, the id stripped of its "__ts__" transport suffix, and both of those
// scoped to the stream session. Mirrors StoreGeminiThoughtSignature so the two
// stores agree on where an entry lives.
func toolCallStoreKeys(toolCallID, sessionID string) []string {
	cleanID := geminiCleanToolCallID(toolCallID)

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
	return keys
}

// StoreGeminiToolCallName records which tool a tool_call id belongs to, so a
// later tool result can be answered without parsing the id.
func StoreGeminiToolCallName(toolCallID, name, sessionID string) {
	if toolCallID == "" || name == "" {
		return
	}

	now := time.Now()
	exp := now.Add(memoryTTL)

	globalToolNameStore.mu.Lock()
	defer globalToolNameStore.mu.Unlock()

	globalToolNameStore.pruneLocked(now)

	for _, k := range toolCallStoreKeys(toolCallID, sessionID) {
		if _, exists := globalToolNameStore.entries[k]; !exists {
			globalToolNameStore.order = append(globalToolNameStore.order, k)
		}
		globalToolNameStore.entries[k] = toolNameEntry{name: name, expiresAt: exp}
	}
}

// GetGeminiToolCallName returns the tool a tool_call id was minted for, or ""
// when the id is unknown to this gateway.
func GetGeminiToolCallName(toolCallID, sessionID string) string {
	if toolCallID == "" {
		return ""
	}

	now := time.Now()

	globalToolNameStore.mu.RLock()
	defer globalToolNameStore.mu.RUnlock()

	for _, k := range toolCallStoreKeys(toolCallID, sessionID) {
		if entry, ok := globalToolNameStore.entries[k]; ok && now.Before(entry.expiresAt) {
			return entry.name
		}
	}
	return ""
}

// ClearGeminiToolCallNames resets the store (useful for tests).
func ClearGeminiToolCallNames() {
	globalToolNameStore.mu.Lock()
	defer globalToolNameStore.mu.Unlock()
	clear(globalToolNameStore.entries)
	globalToolNameStore.order = globalToolNameStore.order[:0]
}
