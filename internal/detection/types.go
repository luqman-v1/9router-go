package detection

import (
	"9router/proxy/internal/translator"
)

type FindingSeverity string

const (
	SeverityWarning  FindingSeverity = "warning"
	SeverityCritical FindingSeverity = "critical"
)

type LoopFinding struct {
	Rule     string          `json:"rule"`
	Severity FindingSeverity `json:"severity"`
	Message  string          `json:"message"`
	Tool     string          `json:"tool,omitempty"`
	Count    int             `json:"count,omitempty"`
}

type ToolCallItem struct {
	ID        string
	Name      string
	Arguments string
	Error     string
}

// Aliases for convenience when parsing messages
type OpenAIMessage = translator.OpenAIMessage
type OpenAIToolCall = translator.OpenAIToolCall
type OpenAIFunctionCall = translator.OpenAIFunctionCall
