package providers

// GetOpenDesignLane reports the declared lane of an OpenDesign model id.
// claude-haiku-5.5 and deepseek-v4-pro default to Responses; glm-5.3-flash
// defaults to Anthropic's messages; everything else defaults to chat. Every
// model is actually served on all three lanes; this is only the default.
func GetOpenDesignLane(model string) string {
	switch model {
	case "claude-haiku-5.5", "deepseek-v4-pro":
		return FormatOpenAIResponses
	case "glm-5.3-flash":
		return FormatClaude
	default:
		return FormatOpenAI
	}
}
