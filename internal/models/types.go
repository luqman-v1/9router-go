package models

// Meta represents a key-value meta record.
type Meta struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Setting represents a single settings record.
type Setting struct {
	ID   int    `json:"id"`
	Data string `json:"data"` // JSON string representation of the settings data
}

// ProviderConnection represents an upstream provider connection.
type ProviderConnection struct {
	ID        string  `json:"id"`
	Provider  string  `json:"provider"`
	AuthType  string  `json:"authType"`
	Name      *string `json:"name,omitempty"`
	Email     *string `json:"email,omitempty"`
	Priority  *int    `json:"priority,omitempty"`
	IsActive  int     `json:"isActive"` // 0 or 1
	Data      string  `json:"data"`     // JSON string representing additional provider config
	CreatedAt string  `json:"createdAt"`
	UpdatedAt string  `json:"updatedAt"`
}

// ProviderNode represents a deployment node / executor config.
type ProviderNode struct {
	ID        string  `json:"id"`
	Type      *string `json:"type,omitempty"`
	Name      *string `json:"name,omitempty"`
	Data      string  `json:"data"` // JSON string representing node details
	CreatedAt string  `json:"createdAt"`
	UpdatedAt string  `json:"updatedAt"`
}

// ProxyPool represents a pool of proxies.
type ProxyPool struct {
	ID         string  `json:"id"`
	IsActive   int     `json:"isActive"` // 0 or 1
	TestStatus *string `json:"testStatus,omitempty"`
	Data       string  `json:"data"` // JSON string representing proxy credentials and checks
	CreatedAt  string  `json:"createdAt"`
	UpdatedAt  string  `json:"updatedAt"`
}

// APIKey represents a client-facing authorization key.
type APIKey struct {
	ID        string  `json:"id"`
	Key       string  `json:"key"`
	Name      *string `json:"name,omitempty"`
	MachineID *string `json:"machineId,omitempty"`
	IsActive  int     `json:"isActive"` // 0 or 1
	CreatedAt string  `json:"createdAt"`
}

// Combo represents a multi-model routing combinated alias.
type Combo struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Kind      *string `json:"kind,omitempty"`
	Models    string  `json:"models"`    // JSON string representing model selection details
	Strategy  string  `json:"strategy"`  // routing strategy: "fallback", "round-robin", "capacity", "fusion"
	CreatedAt string  `json:"createdAt"`
	UpdatedAt string  `json:"updatedAt"`
}

// KV represents a scoped key-value store entry.
type KV struct {
	Scope string `json:"scope"`
	Key   string `json:"key"`
	Value string `json:"value"`
}

// UsageHistory represents logged usage metrics for a request.
type UsageHistory struct {
	ID               int64   `json:"id"`
	Timestamp        string  `json:"timestamp"`
	Provider         *string `json:"provider,omitempty"`
	Model            *string `json:"model,omitempty"`
	ConnectionID     *string `json:"connectionId,omitempty"`
	APIKey           *string `json:"apiKey,omitempty"`
	Endpoint         *string `json:"endpoint,omitempty"`
	PromptTokens     int     `json:"promptTokens"`
	CompletionTokens int     `json:"completionTokens"`
	Cost             float64 `json:"cost"`
	Status           *string `json:"status,omitempty"`
	Tokens           *string `json:"tokens,omitempty"` // JSON metadata on pricing / raw details
	Meta             *string `json:"meta,omitempty"`   // Extra metadata JSON
}

// UsageDaily represents aggregated usage per day.
type UsageDaily struct {
	DateKey string `json:"dateKey"`
	Data    string `json:"data"` // JSON string representing daily stats details
}

// RequestDetail represents the cached payload/response logs of requests.
type RequestDetail struct {
	ID           string  `json:"id"`
	Timestamp    string  `json:"timestamp"`
	Provider     *string `json:"provider,omitempty"`
	Model        *string `json:"model,omitempty"`
	ConnectionID *string `json:"connectionId,omitempty"`
	Status       *string `json:"status,omitempty"`
	Data         string  `json:"data"` // JSON representation of request/response payload
}
