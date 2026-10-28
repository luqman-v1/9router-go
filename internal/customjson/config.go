// Package customjson ports upstream's Custom JSON API provider
// (decolua/9router#4929): a compatible node whose endpoint speaks its own
// JSON schema instead of OpenAI or Anthropic. The node stores a request
// template and a response field path; the router renders the template from
// the translated OpenAI request, sends one JSON request, and lifts the reply
// back out of the response so the existing protocol translators can serve it.
package customjson

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"encoding/json/jsontext"
	json "encoding/json/v2"
)

// APIType is the providerNodes.data apiType value that selects this provider.
const APIType = "custom-json"

// AuthType is how the connection's secret reaches the endpoint.
type AuthType string

const (
	AuthBearer AuthType = "bearer"
	AuthAPIKey AuthType = "api-key"
	AuthNone   AuthType = "none"
)

const (
	defaultMethod       = "POST"
	defaultAuthHeader   = "X-API-Key"
	defaultResponsePath = "choices[0].message.content"
	maxTemplateBytes    = 64 * 1024
	maxTemplateDepth    = 32
	maxHeaders          = 20
	maxHeaderValueBytes = 8192
)

var (
	methods      = []string{"POST", "PUT", "PATCH"}
	authTypes    = []AuthType{AuthBearer, AuthAPIKey, AuthNone}
	variables    = []string{"prompt", "messages", "model", "stream", "request"}
	blockedKeys  = []string{"__proto__", "prototype", "constructor"}
	blockedNames = []string{"host", "content-length", "content-type", "connection", "transfer-encoding", "authorization", "cookie"}

	headerNamePattern = regexp.MustCompile(`^[A-Za-z0-9-]+$`)
)

// Config is the normalized, persisted configuration of a custom JSON node.
// Only these documented fields are stored; anything else a client sends is
// dropped by Normalize.
type Config struct {
	Method          string            `json:"method"`
	AuthType        AuthType          `json:"authType"`
	AuthHeader      string            `json:"authHeader"`
	Headers         map[string]string `json:"headers"`
	RequestTemplate map[string]any    `json:"requestTemplate"`
	ResponsePath    string            `json:"responsePath"`
}

// DefaultRequestTemplate is the template a new node starts with: the selected
// model and the OpenAI-format chat history.
func DefaultRequestTemplate() map[string]any {
	return map[string]any{"model": "{{model}}", "messages": "{{messages}}"}
}

func defaults() Config {
	return Config{
		Method:          defaultMethod,
		AuthType:        AuthBearer,
		AuthHeader:      defaultAuthHeader,
		Headers:         map[string]string{},
		RequestTemplate: DefaultRequestTemplate(),
		ResponsePath:    defaultResponsePath,
	}
}

// rawConfig decodes each field loosely so a wrong JSON type is reported as a
// validation message rather than as an opaque decode failure.
type rawConfig struct {
	Method          jsontext.Value `json:"method"`
	AuthType        jsontext.Value `json:"authType"`
	AuthHeader      jsontext.Value `json:"authHeader"`
	Headers         jsontext.Value `json:"headers"`
	RequestTemplate jsontext.Value `json:"requestTemplate"`
	ResponsePath    jsontext.Value `json:"responsePath"`
}

// Normalize validates a client-supplied configuration against the node's
// endpoint and returns the fields that may be persisted.
func Normalize(raw []byte, endpoint string) (Config, error) {
	var in rawConfig
	if len(raw) == 0 || jsontext.Value(raw).Kind() != '{' || json.Unmarshal(raw, &in) != nil {
		return Config{}, errors.New("custom JSON configuration is required")
	}
	if err := validateEndpoint(endpoint); err != nil {
		return Config{}, err
	}
	cfg := defaults()
	steps := []func() error{
		func() error { return overlayString(in.Method, &cfg.Method) },
		func() error { return overlayString(in.AuthType, (*string)(&cfg.AuthType)) },
		func() error { return overlayString(in.AuthHeader, &cfg.AuthHeader) },
		func() error { return overlayTemplate(in.RequestTemplate, &cfg) },
		func() error { return overlayHeaders(in.Headers, &cfg) },
		func() error { return overlayResponsePath(in.ResponsePath, &cfg) },
		cfg.validateEnums,
		cfg.validateAuthHeader,
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return Config{}, err
		}
	}
	return cfg, nil
}

// Marshal encodes the config the way it is stored in the node data blob and
// copied into connection providerSpecificData.
func (c Config) Marshal() jsontext.Value {
	out, _ := json.Marshal(c, json.Deterministic(true))
	return out
}

func validateEndpoint(endpoint string) error {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		if err == nil && u.Host != "" {
			return errors.New("use an HTTP(S) endpoint without URL credentials or a fragment")
		}
		return errors.New("a complete HTTP(S) endpoint URL is required")
	}
	if u.User != nil || strings.Contains(endpoint, "#") {
		return errors.New("use an HTTP(S) endpoint without URL credentials or a fragment")
	}
	return nil
}

// overlayString replaces dst when the field is present. A present field of
// the wrong type is stored as an impossible value so the enum checks reject it.
func overlayString(v jsontext.Value, dst *string) error {
	if len(v) == 0 {
		return nil
	}
	var s string
	if json.Unmarshal(v, &s) != nil {
		*dst = "\x00invalid"
		return nil
	}
	*dst = s
	return nil
}

func overlayTemplate(v jsontext.Value, cfg *Config) error {
	if len(v) == 0 {
		return validateTemplate(cfg.RequestTemplate)
	}
	if v.Kind() != '{' {
		return errors.New("request template must be a JSON object")
	}
	if len(v) > maxTemplateBytes {
		return errors.New("request template exceeds 64 KB")
	}
	var tpl map[string]any
	if err := json.Unmarshal(v, &tpl); err != nil {
		return errors.New("request template must be a JSON object")
	}
	cfg.RequestTemplate = tpl
	return validateTemplate(tpl)
}

func validateTemplate(tpl map[string]any) error {
	_, err := visit(tpl, 0, func(s string) (any, error) {
		remainder, err := replaceTokens(s, func(path string) (string, error) {
			parts, err := pathParts(path)
			if err != nil {
				return "", err
			}
			if len(parts) == 0 || !slices.Contains(variables, parts[0]) {
				return "", fmt.Errorf("unknown template variable: %s", path)
			}
			return "", nil
		})
		if err != nil {
			return nil, err
		}
		if strings.Contains(remainder, "{{") || strings.Contains(remainder, "}}") {
			return nil, errors.New("invalid template placeholder")
		}
		return s, nil
	})
	return err
}

func overlayHeaders(v jsontext.Value, cfg *Config) error {
	if len(v) == 0 {
		return nil
	}
	var headers map[string]any
	if v.Kind() != '{' || json.Unmarshal(v, &headers) != nil || len(headers) > maxHeaders {
		return errors.New("custom headers must be a JSON object with at most 20 entries")
	}
	out := make(map[string]string, len(headers))
	for name, raw := range headers {
		if !headerNamePattern.MatchString(name) || slices.Contains(blockedNames, strings.ToLower(name)) || slices.Contains(blockedKeys, name) {
			return fmt.Errorf("header cannot be configured: %s", name)
		}
		value, ok := raw.(string)
		if !ok || strings.ContainsAny(value, "\r\n") || len(value) > maxHeaderValueBytes {
			return fmt.Errorf("invalid header value: %s", name)
		}
		out[name] = value
	}
	cfg.Headers = out
	return nil
}

func overlayResponsePath(v jsontext.Value, cfg *Config) error {
	const msg = "response field path is required (use $ for the entire JSON response)"
	if len(v) > 0 {
		var s string
		if json.Unmarshal(v, &s) != nil {
			return errors.New(msg)
		}
		cfg.ResponsePath = s
	}
	cfg.ResponsePath = strings.TrimSpace(cfg.ResponsePath)
	if cfg.ResponsePath == "" {
		return errors.New(msg)
	}
	_, err := pathParts(cfg.ResponsePath)
	return err
}

func (c *Config) validateEnums() error {
	if !slices.Contains(methods, c.Method) {
		return errors.New("invalid custom JSON HTTP method")
	}
	if !slices.Contains(authTypes, c.AuthType) {
		return errors.New("invalid custom JSON authentication type")
	}
	return nil
}

func (c *Config) validateAuthHeader() error {
	if c.AuthType != AuthAPIKey {
		// The header name is only sent for api-key auth; keep a usable value
		// stored so switching the auth type back later starts from it.
		if !headerNamePattern.MatchString(c.AuthHeader) {
			c.AuthHeader = defaultAuthHeader
		}
		return nil
	}
	if !headerNamePattern.MatchString(c.AuthHeader) || slices.Contains(blockedNames, strings.ToLower(c.AuthHeader)) {
		return errors.New("invalid API key header name")
	}
	return nil
}
