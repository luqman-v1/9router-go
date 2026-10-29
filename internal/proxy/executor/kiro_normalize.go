package executor

import (
	"regexp"
	"strings"
)

// anthropicSnapshotSuffix matches Anthropic date-suffixed snapshot ids
// (claude-sonnet-4-5-20250929); Kiro only accepts the dotted alias.
var anthropicSnapshotSuffix = regexp.MustCompile(`-\d{8}$`)

// normalizeKiroUpstreamModelId folds an Anthropic model id onto the Kiro
// upstream alias. Kiro's GenerateAssistantResponse answers anything but the
// dotted alias with 400 INVALID_MODEL_ID, and clients speaking the Anthropic
// Messages API (Claude Code, cc-switch) send the dash-dated spelling, so the
// raw id has to be folded before the request leaves this process. Matching is
// keyword-based rather than an exact-id table so snapshot suffixes and minor
// revisions do not fall through; unknown ids pass through untouched so
// operators can still reach models Kiro adds ahead of this list.
func normalizeKiroUpstreamModelId(model string) string {
	if model == "" {
		return model
	}
	id := strings.ToLower(anthropicSnapshotSuffix.ReplaceAllString(model, ""))

	if strings.Contains(id, "sonnet") {
		switch {
		case strings.Contains(id, "5.5") || strings.Contains(id, "5-5"):
			return "claude-sonnet-5.5"
		case strings.Contains(id, "4.6") || strings.Contains(id, "4-6"):
			return "claude-sonnet-4.6"
		case strings.Contains(id, "sonnet-5") || strings.Contains(id, "sonnet.5"):
			return "claude-sonnet-5"
		case strings.Contains(id, "4.5") || strings.Contains(id, "4-5"):
			return "claude-sonnet-4.5"
		case strings.Contains(id, "sonnet-4") || strings.Contains(id, "sonnet.4"):
			return "claude-sonnet-4"
		}
		return "claude-sonnet-4.5"
	}
	if strings.Contains(id, "fable") {
		return "claude-fable-5"
	}
	if strings.Contains(id, "opus") {
		switch {
		case strings.Contains(id, "5.5") || strings.Contains(id, "5-5"):
			return "claude-opus-5.5"
		case strings.Contains(id, "opus-5") || strings.Contains(id, "opus.5") || strings.Contains(id, "opus 5"):
			return "claude-opus-5"
		case strings.Contains(id, "4.8") || strings.Contains(id, "4-8"):
			return "claude-opus-4.8"
		case strings.Contains(id, "4.7") || strings.Contains(id, "4-7"):
			return "claude-opus-4.7"
		case strings.Contains(id, "4.5") || strings.Contains(id, "4-5"):
			return "claude-opus-4.5"
		}
		return "claude-opus-4.6"
	}
	if strings.Contains(id, "haiku") {
		if strings.Contains(id, "5.5") || strings.Contains(id, "5-5") {
			return "claude-haiku-5.5"
		}
		return "claude-haiku-4.5"
	}
	if id == "auto" {
		return "auto"
	}
	if strings.Contains(id, "deepseek") {
		return "deepseek-3.2"
	}
	if strings.Contains(id, "glm") {
		return "glm-5"
	}
	if strings.Contains(id, "minimax") {
		if strings.Contains(id, "2.5") || strings.Contains(id, "2-5") {
			return "minimax-m2.5"
		}
		return "minimax-m2.1"
	}
	if strings.Contains(id, "qwen") {
		return "qwen3-coder-next"
	}
	if strings.Contains(id, "kimi") {
		return "kimi-k3"
	}
	if strings.Contains(id, "gpt") {
		switch {
		case strings.Contains(id, "terra"):
			return "gpt-5.6-terra"
		case strings.Contains(id, "luna"):
			return "gpt-5.6-luna"
		case strings.Contains(id, "sol") || strings.Contains(id, "5.6") || strings.Contains(id, "5-6"):
			return "gpt-5.6-sol"
		}
		return model
	}
	return model
}
