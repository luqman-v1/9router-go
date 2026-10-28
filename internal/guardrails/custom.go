package guardrails

// CustomPattern defines a user-specified regex pattern to match and sanitize.
type CustomPattern struct {
	Name        string `json:"name"`
	Regex       string `json:"regex"`
	Replacement string `json:"replacement,omitempty"`
}
