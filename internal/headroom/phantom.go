package headroom

import (
	"fmt"
)

// HeadroomStats carries token counts reported by the compressor.
type HeadroomStats struct {
	TokensSaved int `json:"tokens_saved"`
}

// HeadroomSizes carries payload size metrics for phantom savings detection.
type HeadroomSizes struct {
	BodyBytes    int `json:"bodyBytes"`
	MessageBytes int `json:"messageBytes"`
}

// HeadroomDiagnostics carries before and after sizes for phantom savings evaluation.
type HeadroomDiagnostics struct {
	Before HeadroomSizes `json:"before"`
	After  HeadroomSizes `json:"after"`
}

const (
	// DefaultMinShrinkRatio is the minimum ratio (5%) payloads must shrink before
	// being flagged as phantom savings.
	DefaultMinShrinkRatio = 0.05

	// HeadroomPhantomWarningText is the upstream warning message emitted when phantom savings is detected.
	// Updated from "reported token delta, but outbound JSON shrank <5%" in decolua/9router#4855.
	HeadroomPhantomWarningText = "reported token delta, but messages and outbound JSON shrank <5%"
)

// IsHeadroomPhantomSavings reports whether Headroom reported positive token savings
// but BOTH messageBytes and bodyBytes failed to shrink by at least minShrinkRatio (default 0.05).
// If either shrinks meaningfully, it is not phantom savings.
// This prevents false compression warnings and rejections when tools dominate the body,
// while preserving detection for top-level system prompt compression (where bodyBytes shrinks).
// Port of open-sse/rtk/headroom.js isHeadroomPhantomSavings (upstream commit db81a8c9).
func IsHeadroomPhantomSavings(stats *HeadroomStats, diagnostics *HeadroomDiagnostics, minShrinkRatio ...float64) bool {
	if stats == nil || stats.TokensSaved <= 0 || diagnostics == nil {
		return false
	}
	ratio := DefaultMinShrinkRatio
	if len(minShrinkRatio) > 0 && minShrinkRatio[0] > 0 {
		ratio = minShrinkRatio[0]
	}

	checkKey := func(before, after int) bool {
		return before > 0 && after > 0 && float64(after) >= float64(before)*(1.0-ratio)
	}

	// Upstream contract: ["bodyBytes", "messageBytes"].every(...)
	return checkKey(diagnostics.Before.BodyBytes, diagnostics.After.BodyBytes) &&
		checkKey(diagnostics.Before.MessageBytes, diagnostics.After.MessageBytes)
}

// FormatHeadroomSizeLog formats size diagnostics for log output.
func FormatHeadroomSizeLog(d *HeadroomDiagnostics) string {
	if d == nil {
		return ""
	}
	return fmt.Sprintf("body=%dB→%dB messages=%dB→%dB",
		d.Before.BodyBytes, d.After.BodyBytes,
		d.Before.MessageBytes, d.After.MessageBytes,
	)
}

// FormatHeadroomPhantomWarning formats the log warning line when phantom savings is detected.
func FormatHeadroomPhantomWarning(d *HeadroomDiagnostics) string {
	sizeLog := FormatHeadroomSizeLog(d)
	if sizeLog != "" {
		return fmt.Sprintf("%s | %s", HeadroomPhantomWarningText, sizeLog)
	}
	return HeadroomPhantomWarningText
}
