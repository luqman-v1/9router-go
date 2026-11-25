package headroom

import (
	"fmt"
	"strings"

	json "encoding/json/v2"

	"9router/proxy/internal/tokensaver"
)

// The built-in engine implements the /v1/compress contract in-process, so the
// gateway compresses without an external proxy. It is not a second compressor:
// it reuses the RTK pipeline this binary already runs for its own token-saver
// pass (internal/tokensaver), which is the lossless half of upstream's
// "lossless_then_lossy" mode.
//
// It deliberately refuses what the external proxy is asked to do and the
// gateway cannot verify: no CCR (the gateway has no retrieve endpoint), no
// semantic rewriting of prose, no cross-message deduplication that would drop a
// message. What it does is fold repetitive tool output — git logs, build and
// test output, directory trees, numbered reads — which is where the bytes are
// on an agent's request.
//
// Every output comes from internal/tokensaver, whose invariants are pinned by
// that package's own tests: protected tool results and error outputs are never
// touched (compress.go isProtectedTool / isErrorToolResult).

// BuiltinEngine is the in-process implementation of the /v1/compress contract.
type BuiltinEngine struct {
	// Config is the RTK configuration the pipeline runs with. The zero value is
	// usable: Compress fills every field tokensaver treats as unset.
	Config tokensaver.RTKConfig
}

// NewBuiltinEngine returns an engine running with the default RTK config.
func NewBuiltinEngine() *BuiltinEngine {
	return &BuiltinEngine{Config: tokensaver.DefaultRTKConfig()}
}

// builtinMemory is the process-level prefix-replay cache shared by every
// BuiltinEngine. It is a package singleton on purpose: the gateway builds a
// fresh client (and therefore a fresh engine) per request in its only
// production call path (internal/handlers/chat/fallback.go), so a store owned by
// an engine would be rebuilt empty every turn and never learn a prefix. Bound
// at NewMemoryStore's defaults (500 sessions, 50 entries per session, 24h TTL)
// under a mutex, so it cannot grow without bound on the small deployment target.
var builtinMemory = NewMemoryStore(0, 0, 0)

// BuiltinCompressResult is the engine's answer, in the shape the external proxy
// returns for /v1/compress.
//
// CompressionSkipped and SkipReason are omitted on purpose. The engine either
// compresses or it does not, and "did not" is Messages == nil, so the caller's
// guards never interpret a skip.
type BuiltinCompressResult struct {
	Messages          []any   `json:"messages"`
	TokensBefore      int     `json:"tokens_before"`
	TokensAfter       int     `json:"tokens_after"`
	TokensSaved       int     `json:"tokens_saved"`
	CompressionRatio  float64 `json:"compression_ratio"`
	TransformsApplied []string
	TransformsSummary string   `json:"transforms_summary"`
	CcrHashes         []string `json:"ccr_hashes"`
}

// CompressMessages compresses messages in-process.
//
// messages is the same []any the external client would post. A nil Messages in
// the result means nothing was safely compressible, which the caller treats as
// fail-open.
func (e *BuiltinEngine) CompressMessages(messages []any) (*BuiltinCompressResult, bool) {
	return e.CompressMessagesWithSession("", messages)
}

// CompressMessagesWithSession is CompressMessages plus prefix replay: when
// sessionID is non-empty, a compression decision made on an earlier turn's
// leading messages is replayed instead of re-folding that already-seen history.
//
// The replay is safe because the RTK fold is per message and the engine does no
// cross-message work (no deduplication, no semantic rewriting): a message's
// folded form depends only on that message, never on its neighbours. So the
// cached compression of a matching prefix is byte-identical to what a full
// re-fold would produce, and splicing it onto a freshly compressed tail yields
// exactly the full-compress result. The caller's validateMessagesPreserved gate
// and byte-gain check still run on the spliced output, so a divergence fails
// open rather than committing a wrong body.
//
// A empty sessionID (the native path, and any caller without a stable session
// identity) skips the cache and compresses from scratch every time.
func (e *BuiltinEngine) CompressMessagesWithSession(sessionID string, messages []any) (*BuiltinCompressResult, bool) {
	if sessionID == "" || len(messages) == 0 {
		return e.compressFromScratch(messages)
	}

	// Replay the longest cached prefix that is a true head of this request.
	if entry, ok := builtinMemory.Lookup(sessionID, messages); ok {
		if res, ok := e.replayPrefix(entry, messages); ok {
			return res, true
		}
		// An unusable entry (stale shape, count drift) falls through to a full
		// fold rather than committing a spliced body the gate would reject.
	}

	res, ok := e.compressFromScratch(messages)
	if ok && res != nil {
		builtinMemory.Store(sessionID, messages, res.Messages, res.TokensSaved)
	}
	return res, ok
}

// replayPrefix splices a cached prefix compression onto a freshly compressed
// tail. The cached entry already holds the folded prefix messages, so only the
// unseen tail runs through the RTK pipeline.
func (e *BuiltinEngine) replayPrefix(entry *MemoryEntry, messages []any) (*BuiltinCompressResult, bool) {
	if entry == nil || entry.Length <= 0 || entry.Length > len(messages) || len(entry.CompressedMessages) == 0 {
		return nil, false
	}
	tail := messages[entry.Length:]
	tailCompressed := tail
	if len(tail) > 0 {
		if tailRes, ok := e.compressFromScratch(tail); ok && tailRes != nil {
			tailCompressed = tailRes.Messages
		}
	}
	combined := make([]any, 0, len(entry.CompressedMessages)+len(tailCompressed))
	combined = append(combined, entry.CompressedMessages...)
	combined = append(combined, tailCompressed...)
	// Message count must survive the splice; if it did not, defer to a full
	// fold rather than hand the caller a body its gate would reject.
	if len(combined) != len(messages) {
		return nil, false
	}
	return e.resultFromCompressed(messages, combined)
}

// compressFromScratch runs the RTK pipeline over the whole message list with no
// cache consultation. It is CompressMessages' original body.
func (e *BuiltinEngine) compressFromScratch(messages []any) (*BuiltinCompressResult, bool) {
	if len(messages) == 0 {
		return nil, false
	}
	cfg := e.resolveConfig()

	// tokensaver.CompressMessagesWithConfig is the engine: it walks the same
	// three tool-result shapes the gateway supports (OpenAI tool message,
	// Responses function_call_output, Claude tool_result), applies the
	// protected-tool and error-output skips, and re-marshals deterministically.
	// It reports false when nothing was eligible, which is the engine's
	// fail-open.
	bodyBytes, err := json.Marshal(map[string]any{"messages": messages}, json.Deterministic(true))
	if err != nil {
		return nil, false
	}
	compressedBytes, ok := tokensaver.CompressMessagesWithConfig(bodyBytes, cfg)
	if !ok {
		return nil, false
	}
	var out struct {
		Messages []any `json:"messages"`
	}
	if err := json.Unmarshal(compressedBytes, &out); err != nil || len(out.Messages) == 0 {
		return nil, false
	}

	// Token accounting uses the estimator the rest of the gateway uses for
	// prompt tokens, (len+3)/4 (internal/handlers/chat/chat.go,
	// internal/handlers/chat/capacity_adapter_history.go). A dedicated
	// tokenizer would be a dependency this binary does not otherwise need, and
	// a different estimator here would make the reported saving disagree with
	// the accounting the operator reads on the usage page.
	before := estimateBodyTokens(bodyBytes)
	after := estimateBodyTokens(compressedBytes)
	if saved := before - after; saved <= 0 {
		// Nothing folded, or it folded back to the same size. Report no
		// compression rather than a saving of zero.
		return nil, false
	} else {
		return &BuiltinCompressResult{
			Messages:          out.Messages,
			TokensBefore:      before,
			TokensAfter:       after,
			TokensSaved:       saved,
			CompressionRatio:  float64(after) / float64(before),
			TransformsApplied: []string{"rtk-tool-output"},
			TransformsSummary: summarizeTransforms(messages, out.Messages),
			// The engine never emits CCR, so the contract's empty array is
			// always satisfied without the caller special-casing it.
			CcrHashes: nil,
		}, true
	}
}

// resultFromCompressed builds the result for an already-folded message list,
// applying the same token accounting compressFromScratch uses. It reports false
// when the fold produced no saving, so a replay that bought nothing is treated
// as "no compression" like any other.
func (e *BuiltinEngine) resultFromCompressed(messages, compressed []any) (*BuiltinCompressResult, bool) {
	before := messageListTokens(messages)
	after := messageListTokens(compressed)
	saved := before - after
	if before <= 0 || after <= 0 || saved <= 0 {
		return nil, false
	}
	return &BuiltinCompressResult{
		Messages:          compressed,
		TokensBefore:      before,
		TokensAfter:       after,
		TokensSaved:       saved,
		CompressionRatio:  float64(after) / float64(before),
		TransformsApplied: []string{"rtk-tool-output"},
		TransformsSummary: summarizeTransforms(messages, compressed),
		CcrHashes:         nil,
	}, true
}

// messageListTokens estimates the prompt tokens of a message list using the
// gateway-wide (len+3)/4 estimator over the same {"messages": ...} envelope
// compressFromScratch marshals, so replay accounting matches a full fold.
func messageListTokens(messages []any) int {
	b, err := json.Marshal(map[string]any{"messages": messages}, json.Deterministic(true))
	if err != nil {
		return 0
	}
	return estimateBodyTokens(b)
}

// CompressText compresses one text leaf and reports whether it shrank. It is
// the engine's single-leaf entry point, used by the native-tool-output path
// where the leaves live outside the messages array.
func (e *BuiltinEngine) CompressText(text string) (string, bool) {
	if strings.TrimSpace(text) == "" {
		return text, false
	}
	out := tokensaver.CompressTextDetailedWithCommand(text, "", e.resolveConfig())
	if out.Text == text || len(out.Text) >= len(text) {
		return text, false
	}
	return out.Text, true
}

// resolveConfig fills the fields tokensaver reads as "unset", so a partially
// populated Config behaves like the default rather than like a stricter one.
func (e *BuiltinEngine) resolveConfig() tokensaver.RTKConfig {
	cfg := e.Config
	if cfg.MaxLines <= 0 {
		cfg.MaxLines = tokensaver.SmartTruncMin
	}
	if cfg.MaxChars <= 0 {
		cfg.MaxChars = tokensaver.RawCap
	}
	if cfg.Categories == nil {
		cfg.Categories = tokensaver.DefaultRTKConfig().Categories
	}
	return cfg
}

// summarizeTransforms counts the text leaves that actually shrank, so
// transforms_summary carries real information instead of a constant string.
// The two walks are order-stable, so a leaf count mismatch is reported as a
// generic summary rather than silently misaligned counts.
func summarizeTransforms(srcMsgs, outMsgs []any) string {
	var src, out []int
	collectTextLeaves(srcMsgs, &src)
	collectTextLeaves(outMsgs, &out)
	if len(src) == 0 || len(src) != len(out) {
		return "tool output folded by the in-process RTK pipeline"
	}
	shrunk := 0
	for i := range src {
		if out[i] < src[i] {
			shrunk++
		}
	}
	if shrunk == 0 {
		return "tool output folded by the in-process RTK pipeline"
	}
	return fmt.Sprintf("%d tool-output block(s) folded by the in-process RTK pipeline", shrunk)
}

// collectTextLeaves records the length of every text leaf in the message list,
// in a stable order, so the same walk over the source and the compressed
// messages lines up index for index.
func collectTextLeaves(messages []any, out *[]int) {
	for _, raw := range messages {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		appendLeaf(out, m["content"])
		appendLeaf(out, m["output"])
		if arr, ok := m["content"].([]any); ok {
			for _, part := range arr {
				pm, ok := part.(map[string]any)
				if !ok {
					continue
				}
				switch pm["type"] {
				case "text", "input_text", "tool_result":
					appendLeaf(out, pm["text"])
					appendLeaf(out, pm["content"])
				}
			}
		}
	}
}

// appendLeaf appends the byte length of a text value. Non-string values are
// skipped: the leaves the engine rewrites are plain text, and a number or an
// object would desynchronize the two walks.
func appendLeaf(out *[]int, v any) {
	if s, ok := v.(string); ok {
		*out = append(*out, len(s))
	}
}

// estimateBodyTokens is the gateway's prompt-token estimator applied to a body.
func estimateBodyTokens(b []byte) int {
	return (len(b) + 3) / 4
}

// FormatBuiltinResult renders the engine's answer for a log line.
func FormatBuiltinResult(r *BuiltinCompressResult) string {
	if r == nil {
		return "builtin: no compression"
	}
	return fmt.Sprintf("builtin: %d→%d tokens (%d saved, ratio %.3f)",
		r.TokensBefore, r.TokensAfter, r.TokensSaved, r.CompressionRatio)
}
