// Package log provides a structured logger with levels and context tracing for the 9router proxy.
//
// Usage:
//
//	log.Info("combo", "Model failed", "provider", provider, "model", model)
//	log.InfoCtx(ctx, "chat", "Processing request", "model", model)
//	log.Warn("auth", "Invalid key", "key", log.MaskSecret(key))
//	log.Error("stream", "Connection error", "err", err)
//
// Levels are controlled via LOG_LEVEL env var: debug, info (default), warn, error.
// Format is controlled via LOG_FORMAT env var: text (default), json.
package log

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

// Console log capture: an in-process ring buffer plus live subscribers, so the
// dashboard can stream server log output. Mirrors the Next translator
// console-logs contract (buffer + SSE with init/line/lines/clear events).
const (
	consoleMaxLines = 200
	consoleBufSize  = 256
)

type consoleEvent struct {
	kind string // "line" | "clear"
	line string
}

// Kind returns the event kind ("line" or "clear").
func (e consoleEvent) Kind() string { return e.kind }

// Line returns the log line for "line" events.
func (e consoleEvent) Line() string { return e.line }

var (
	consoleMu   sync.Mutex
	consoleLogs []string
	consoleSubs = map[int]chan consoleEvent{}
	consoleNext int
)

// captureConsole appends a formatted line to the ring buffer and fans it out
// to live subscribers without blocking the logger.
func captureConsole(line string) {
	consoleMu.Lock()
	consoleLogs = append(consoleLogs, line)
	if len(consoleLogs) > consoleMaxLines {
		consoleLogs = consoleLogs[len(consoleLogs)-consoleMaxLines:]
	}
	subs := make([]chan consoleEvent, 0, len(consoleSubs))
	for _, ch := range consoleSubs {
		subs = append(subs, ch)
	}
	consoleMu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- consoleEvent{kind: "line", line: line}:
		default: // drop for slow subscriber, never block logging
		}
	}
}

// ConsoleLogs returns a copy of the buffered console lines.
func ConsoleLogs() []string {
	consoleMu.Lock()
	defer consoleMu.Unlock()
	out := make([]string, len(consoleLogs))
	copy(out, consoleLogs)
	return out
}

// ClearConsoleLogs empties the buffer and notifies subscribers.
func ClearConsoleLogs() {
	consoleMu.Lock()
	consoleLogs = consoleLogs[:0]
	subs := make([]chan consoleEvent, 0, len(consoleSubs))
	for _, ch := range consoleSubs {
		subs = append(subs, ch)
	}
	consoleMu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- consoleEvent{kind: "clear"}:
		default:
		}
	}
}

// SubscribeConsole returns a channel of live log events. The caller must call
// cancel when done to stop delivery.
func SubscribeConsole() (<-chan consoleEvent, func()) {
	consoleMu.Lock()
	defer consoleMu.Unlock()
	id := consoleNext
	consoleNext++
	ch := make(chan consoleEvent, consoleBufSize)
	consoleSubs[id] = ch
	cancel := func() {
		consoleMu.Lock()
		delete(consoleSubs, id)
		consoleMu.Unlock()
	}
	return ch, cancel
}

// RequestIDContextKey is the typed context key for request IDs.
// Shared between middleware and log packages to ensure context value lookups match.
type RequestIDContextKey string

// RequestIDKey is the context key used to store request IDs.
const RequestIDKey RequestIDContextKey = "requestID"

// Level represents a log level.
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

var (
	currentLevel Level
	isJSONFormat bool
	levelMu      sync.RWMutex
	levelNames   = map[string]Level{
		"debug": LevelDebug,
		"info":  LevelInfo,
		"warn":  LevelWarn,
		"error": LevelError,
	}
	levelPrefix = map[Level]string{
		LevelDebug: "DBG",
		LevelInfo:  "INF",
		LevelWarn:  "WRN",
		LevelError: "ERR",
	}
	levelColor = map[Level]string{
		LevelDebug: "\033[36m", // cyan
		LevelInfo:  "\033[32m", // green
		LevelWarn:  "\033[33m", // yellow
		LevelError: "\033[31m", // red
	}
	colorReset   = "\033[0m"
	colorEnabled bool
)

func init() {
	currentLevel = LevelInfo
	if lvl := os.Getenv("LOG_LEVEL"); lvl != "" {
		if l, ok := levelNames[strings.ToLower(lvl)]; ok {
			currentLevel = l
		}
	}

	if fmtEnv := os.Getenv("LOG_FORMAT"); strings.ToLower(fmtEnv) == "json" {
		isJSONFormat = true
	}

	if fi, _ := os.Stdout.Stat(); fi != nil && (fi.Mode()&os.ModeCharDevice) != 0 {
		colorEnabled = os.Getenv("NO_COLOR") == "" && !isJSONFormat
	}
}

// SetLevel changes the log level at runtime.
func SetLevel(l Level) {
	levelMu.Lock()
	defer levelMu.Unlock()
	currentLevel = l
}

// SetJSONFormat toggles JSON vs Text log format.
func SetJSONFormat(enable bool) {
	levelMu.Lock()
	defer levelMu.Unlock()
	isJSONFormat = enable
}

func shouldLog(l Level) bool {
	levelMu.RLock()
	defer levelMu.RUnlock()
	return l >= currentLevel
}

// MaskSecret masks sensitive credentials (e.g. "sk-proj-12345678" -> "sk-p...5678").
func MaskSecret(secret string) string {
	if len(secret) <= 8 {
		return "***"
	}
	return secret[:4] + "..." + secret[len(secret)-4:]
}

// Debug logs a debug-level message.
func Debug(tag, msg string, kv ...any) {
	if !shouldLog(LevelDebug) {
		return
	}
	output(LevelDebug, tag, msg, kv...)
}

// Info logs an info-level message.
func Info(tag, msg string, kv ...any) {
	if !shouldLog(LevelInfo) {
		return
	}
	output(LevelInfo, tag, msg, kv...)
}

// Warn logs a warning-level message.
func Warn(tag, msg string, kv ...any) {
	if !shouldLog(LevelWarn) {
		return
	}
	output(LevelWarn, tag, msg, kv...)
}

// Error logs an error-level message.
func Error(tag, msg string, kv ...any) {
	if !shouldLog(LevelError) {
		return
	}
	output(LevelError, tag, msg, kv...)
}

// DebugCtx logs a debug-level message with request ID from context if present.
func DebugCtx(ctx context.Context, tag, msg string, kv ...any) {
	if !shouldLog(LevelDebug) {
		return
	}
	outputCtx(ctx, LevelDebug, tag, msg, kv...)
}

// InfoCtx logs an info-level message with request ID from context if present.
func InfoCtx(ctx context.Context, tag, msg string, kv ...any) {
	if !shouldLog(LevelInfo) {
		return
	}
	outputCtx(ctx, LevelInfo, tag, msg, kv...)
}

// WarnCtx logs a warning-level message with request ID from context if present.
func WarnCtx(ctx context.Context, tag, msg string, kv ...any) {
	if !shouldLog(LevelWarn) {
		return
	}
	outputCtx(ctx, LevelWarn, tag, msg, kv...)
}

// ErrorCtx logs an error-level message with request ID from context if present.
func ErrorCtx(ctx context.Context, tag, msg string, kv ...any) {
	if !shouldLog(LevelError) {
		return
	}
	outputCtx(ctx, LevelError, tag, msg, kv...)
}

func outputCtx(ctx context.Context, l Level, tag, msg string, kv ...any) {
	if ctx != nil {
		if reqID, ok := ctx.Value(RequestIDKey).(string); ok && reqID != "" {
			kv = append([]any{"req_id", reqID}, kv...)
		}
	}
	output(l, tag, msg, kv...)
}

func output(l Level, tag, msg string, kv ...any) {
	levelMu.RLock()
	jsonMode := isJSONFormat
	levelMu.RUnlock()

	if jsonMode {
		entry := map[string]any{
			"time":  time.Now().UTC().Format(time.RFC3339Nano),
			"level": levelPrefix[l],
			"tag":   tag,
			"msg":   msg,
		}
		if len(kv) > 0 {
			if len(kv)%2 != 0 {
				kv = append(kv, "")
			}
			for i := 0; i < len(kv); i += 2 {
				key := fmt.Sprint(kv[i])
				entry[key] = kv[i+1]
			}
		}
		b, err := json.Marshal(entry)
		if err == nil {
			line := string(b)
			captureConsole(line)
			log.Println(line)
			return
		}
	}

	prefix := levelPrefix[l]
	var b strings.Builder
	b.Grow(len(prefix) + len(tag) + len(msg) + 80)
	if colorEnabled {
		b.WriteString(levelColor[l])
	}
	b.WriteString(prefix)
	if colorEnabled {
		b.WriteString(colorReset)
	}
	b.WriteString(" [")
	b.WriteString(tag)
	b.WriteString("] ")
	b.WriteString(msg)

	if len(kv) > 0 {
		if len(kv)%2 != 0 {
			kv = append(kv, "")
		}
		for i := 0; i < len(kv); i += 2 {
			b.WriteString(" ")
			b.WriteString(fmt.Sprint(kv[i]))
			b.WriteString("=")
			b.WriteString(fmt.Sprint(kv[i+1]))
		}
	}

	line := b.String()
	captureConsole(line)
	log.Println(line)
}
