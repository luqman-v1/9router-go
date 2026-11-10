package resilience

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// DefaultStalledToolLoopThreshold is the number of consecutive identical tool
// calls before a stalled tool loop intervention is triggered.
const DefaultStalledToolLoopThreshold = 3

// DefaultStalledToolLoopWindow is the default time window for tracking loop history.
const DefaultStalledToolLoopWindow = 15 * time.Minute

// ToolCallSignature represents the identity of a tool invocation.
type ToolCallSignature struct {
	Name         string
	PayloadHash  string
	RawArguments string
}

// StalledLoopIntervention represents the intervention message or error
// returned when a stalled tool loop is detected.
type StalledLoopIntervention struct {
	ToolName     string `json:"tool_name"`
	CallCount    int    `json:"call_count"`
	Message      string `json:"message"`
	Intervention string `json:"intervention"`
}

// Error implements error interface.
func (i *StalledLoopIntervention) Error() string {
	return i.Message
}

// FormatAssistantIntervention returns a structured intervention text for the agent.
func (i *StalledLoopIntervention) FormatAssistantIntervention() string {
	return fmt.Sprintf(
		"System Intervention [StalledToolLoopDetected]: You have called tool %q %d consecutive times with identical arguments without state progression. Stop spinning. Evaluate why prior attempts produced no new state, alter your arguments or approach, or summarize your findings to the user.",
		i.ToolName,
		i.CallCount,
	)
}

// ToolCallRecord stores a single tool call attempt in a session.
type ToolCallRecord struct {
	Signature ToolCallSignature
	Timestamp time.Time
}

// SessionToolHistory stores the consecutive tool calls for a session.
type SessionToolHistory struct {
	LastToolHash     string
	LastToolName     string
	ConsecutiveCount int
	LastUpdated      time.Time
	History          []ToolCallRecord
}

// StalledToolLoopDetector tracks repeated tool calls per session or conversation.
type StalledToolLoopDetector struct {
	mu        sync.RWMutex
	threshold int
	ttl       time.Duration
	sessions  map[string]*SessionToolHistory
}

// StalledToolLoopOptions configures the detector.
type StalledToolLoopOptions struct {
	Threshold int
	TTL       time.Duration
}

// NewStalledToolLoopDetector initializes a detector.
func NewStalledToolLoopDetector(opts StalledToolLoopOptions) *StalledToolLoopDetector {
	thresh := opts.Threshold
	if thresh <= 0 {
		thresh = DefaultStalledToolLoopThreshold
	}
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = DefaultStalledToolLoopWindow
	}
	return &StalledToolLoopDetector{
		threshold: thresh,
		ttl:       ttl,
		sessions:  make(map[string]*SessionToolHistory),
	}
}

// HashToolArguments normalizes and hashes tool arguments payload.
func HashToolArguments(args string) string {
	clean := strings.TrimSpace(args)
	h := sha256.Sum256([]byte(clean))
	return hex.EncodeToString(h[:])
}

// RecordToolCall records a tool call for a session and checks if it constitutes
// a stalled tool loop. Returns an intervention if loop threshold is reached.
func (d *StalledToolLoopDetector) RecordToolCall(sessionKey, toolName, rawArgs string) *StalledLoopIntervention {
	if sessionKey == "" || toolName == "" {
		return nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()
	d.cleanupExpiredLocked(now)

	sigHash := HashToolArguments(rawArgs)
	fullKey := toolName + ":" + sigHash

	sess, ok := d.sessions[sessionKey]
	if !ok || now.Sub(sess.LastUpdated) > d.ttl {
		sess = &SessionToolHistory{
			LastToolHash:     fullKey,
			LastToolName:     toolName,
			ConsecutiveCount: 1,
			LastUpdated:      now,
			History: []ToolCallRecord{
				{
					Signature: ToolCallSignature{
						Name:         toolName,
						PayloadHash:  sigHash,
						RawArguments: rawArgs,
					},
					Timestamp: now,
				},
			},
		}
		d.sessions[sessionKey] = sess
		return nil
	}

	sess.LastUpdated = now

	// Check if this tool call is identical to the previous one
	if sess.LastToolHash == fullKey {
		sess.ConsecutiveCount++
	} else {
		sess.LastToolHash = fullKey
		sess.LastToolName = toolName
		sess.ConsecutiveCount = 1
	}

	sess.History = append(sess.History, ToolCallRecord{
		Signature: ToolCallSignature{
			Name:         toolName,
			PayloadHash:  sigHash,
			RawArguments: rawArgs,
		},
		Timestamp: now,
	})

	if sess.ConsecutiveCount >= d.threshold {
		intervention := &StalledLoopIntervention{
			ToolName:  toolName,
			CallCount: sess.ConsecutiveCount,
			Message: fmt.Sprintf("stalled tool loop detected: tool %q called %d consecutive times with identical arguments",
				toolName, sess.ConsecutiveCount),
		}
		intervention.Intervention = intervention.FormatAssistantIntervention()
		return intervention
	}

	return nil
}

// ResetSession resets the counter and history for a given session.
// Typically invoked when user sends a new message or context changes.
func (d *StalledToolLoopDetector) ResetSession(sessionKey string) {
	if sessionKey == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.sessions, sessionKey)
}

// GetConsecutiveCount returns current count for a session.
func (d *StalledToolLoopDetector) GetConsecutiveCount(sessionKey string) int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	sess, ok := d.sessions[sessionKey]
	if !ok {
		return 0
	}
	return sess.ConsecutiveCount
}

// cleanupExpiredLocked removes sessions inactive past TTL.
func (d *StalledToolLoopDetector) cleanupExpiredLocked(now time.Time) {
	for k, sess := range d.sessions {
		if now.Sub(sess.LastUpdated) > d.ttl {
			delete(d.sessions, k)
		}
	}
}

// InspectHistoryTurns scans conversation messages to detect if the tail of the
// conversation contains N consecutive identical tool calls or results without progress.
// This works statelessly on a single request payload history.
func InspectHistoryTurns(messages []map[string]any, threshold int) *StalledLoopIntervention {
	if threshold <= 0 {
		threshold = DefaultStalledToolLoopThreshold
	}
	if len(messages) < threshold {
		return nil
	}

	var extractedCalls []ToolCallSignature

	for _, msg := range messages {
		role, _ := msg["role"].(string)
		if role == "user" {
			// In Claude format, tool_result blocks are sent inside a user message.
			// Only an actual human text turn resets the loop counter.
			isOnlyToolResult := false
			if blocks, ok := msg["content"].([]any); ok && len(blocks) > 0 {
				hasHumanText := false
				for _, b := range blocks {
					if bm, ok := b.(map[string]any); ok {
						typ, _ := bm["type"].(string)
						txt, _ := bm["text"].(string)
						if typ == "text" && strings.TrimSpace(txt) != "" {
							hasHumanText = true
							break
						}
					}
				}
				if !hasHumanText {
					isOnlyToolResult = true
				}
			}
			if !isOnlyToolResult {
				// A real human user turn resets consecutive tool loop state
				extractedCalls = nil
			}
			continue
		}

		if role == "assistant" {
			// Check tool_calls (OpenAI format)
			if tcs, ok := msg["tool_calls"].([]any); ok && len(tcs) > 0 {
				for _, tcRaw := range tcs {
					if tcMap, ok := tcRaw.(map[string]any); ok {
						fn, _ := tcMap["function"].(map[string]any)
						name, _ := fn["name"].(string)
						if name == "" {
							name, _ = tcMap["name"].(string)
						}
						if name == "" {
							continue
						}
						args, _ := fn["arguments"].(string)
						if args == "" {
							if a, ok := tcMap["arguments"].(string); ok {
								args = a
							}
						}
						extractedCalls = append(extractedCalls, ToolCallSignature{
							Name:         name,
							PayloadHash:  HashToolArguments(args),
							RawArguments: args,
						})
					}
				}
			}

			// Check content blocks for tool_use (Claude format)
			if blocks, ok := msg["content"].([]any); ok {
				for _, b := range blocks {
					if bm, ok := b.(map[string]any); ok {
						if typ, _ := bm["type"].(string); typ == "tool_use" {
							name, _ := bm["name"].(string)
							if name == "" {
								continue
							}
							var argsStr string
							if inp, ok := bm["input"]; ok {
								switch v := inp.(type) {
								case string:
									argsStr = v
								default:
									if b, err := json.Marshal(v); err == nil {
										argsStr = string(b)
									} else {
										argsStr = fmt.Sprintf("%v", v)
									}
								}
							}
							extractedCalls = append(extractedCalls, ToolCallSignature{
								Name:         name,
								PayloadHash:  HashToolArguments(argsStr),
								RawArguments: argsStr,
							})
						}
					}
				}
			}
		}
	}

	if len(extractedCalls) < threshold {
		return nil
	}

	// Examine tail of extractedCalls for identical consecutive calls
	n := len(extractedCalls)
	last := extractedCalls[n-1]
	consecutive := 1

	for i := n - 2; i >= 0; i-- {
		cur := extractedCalls[i]
		if cur.Name == last.Name && cur.PayloadHash == last.PayloadHash {
			consecutive++
			if consecutive >= threshold {
				intervention := &StalledLoopIntervention{
					ToolName:  last.Name,
					CallCount: consecutive,
					Message: fmt.Sprintf("stalled tool loop detected: tool %q called %d consecutive times with identical arguments",
						last.Name, consecutive),
				}
				intervention.Intervention = intervention.FormatAssistantIntervention()
				return intervention
			}
		} else {
			break
		}
	}

	return nil
}
