// Package log provides a structured logger with levels for the 9router proxy.
//
// Usage:
//
//	log.Info("combo", "Model failed", "provider", provider, "model", model)
//	log.Warn("auth", "Invalid key", "key", maskedKey)
//	log.Error("stream", "Connection error", "err", err)
//
// Levels are controlled via LOG_LEVEL env var: debug, info (default), warn, error.
package log

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
)

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
	// ANSI color codes per level.
	levelColor = map[Level]string{
		LevelDebug: "\033[36m", // cyan
		LevelInfo:  "\033[32m", // green
		LevelWarn:  "\033[33m", // yellow
		LevelError: "\033[31m", // red
	}
	colorReset = "\033[0m"
	// cached — set once at init.
	colorEnabled bool
)

func init() {
	currentLevel = LevelInfo
	if lvl := os.Getenv("LOG_LEVEL"); lvl != "" {
		if l, ok := levelNames[strings.ToLower(lvl)]; ok {
			currentLevel = l
		}
	}

	// Enable color when stdout is a terminal and NO_COLOR is not set.
	if fi, _ := os.Stdout.Stat(); fi != nil && (fi.Mode()&os.ModeCharDevice) != 0 {
		colorEnabled = os.Getenv("NO_COLOR") == ""
	}
}

// SetLevel changes the log level at runtime.
func SetLevel(l Level) {
	levelMu.Lock()
	defer levelMu.Unlock()
	currentLevel = l
}

// shouldLog returns true if the given level should be logged.
func shouldLog(l Level) bool {
	levelMu.RLock()
	defer levelMu.RUnlock()
	return l >= currentLevel
}

// Debug logs a debug-level message. Hidden unless LOG_LEVEL=debug.
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

// output formats and writes a log message.
// Format: "[LEVEL] [tag] msg key=val key=val"
func output(l Level, tag, msg string, kv ...any) {
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
			// Odd number — last is a single value
			kv = append(kv, "")
		}
		for i := 0; i < len(kv); i += 2 {
			b.WriteString(" ")
			b.WriteString(fmt.Sprint(kv[i]))
			b.WriteString("=")
			b.WriteString(fmt.Sprint(kv[i+1]))
		}
	}

	log.Println(b.String())
}
