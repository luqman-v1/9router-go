package customjson

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"encoding/json/jsontext"
	json "encoding/json/v2"
)

var (
	tokenPattern     = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)
	exactToken       = regexp.MustCompile(`^\{\{\s*([^{}]+?)\s*\}\}$`)
	arrayIndex       = regexp.MustCompile(`\[(\d+)\]`)
	fieldPathPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)*$`)
)

// pathParts splits a field path ("data.answers[0].text", "$" for the whole
// value) into its keys, refusing anything that is not plain dot/index syntax.
func pathParts(path string) ([]string, error) {
	path = strings.TrimSpace(path)
	if path == "$" {
		return nil, nil
	}
	normalized := arrayIndex.ReplaceAllString(strings.TrimPrefix(path, "$."), ".$1")
	if !fieldPathPattern.MatchString(normalized) {
		return nil, fmt.Errorf("invalid field path: %s", path)
	}
	parts := strings.Split(normalized, ".")
	if slices.ContainsFunc(parts, func(p string) bool { return slices.Contains(blockedKeys, p) }) {
		return nil, errors.New("unsafe field path")
	}
	return parts, nil
}

// readPath walks decoded JSON. found is false when a key or index is absent;
// a present JSON null is found with a nil value.
func readPath(value any, parts []string) (v any, found bool) {
	current := value
	for _, key := range parts {
		switch node := current.(type) {
		case map[string]any:
			next, ok := node[key]
			if !ok {
				return nil, false
			}
			current = next
		case []any:
			i, err := strconv.Atoi(key)
			if err != nil || i < 0 || i >= len(node) {
				return nil, false
			}
			current = node[i]
		default:
			return nil, false
		}
	}
	return current, true
}

// visit rewrites every string leaf of a template with fn, rejecting templates
// nested deeper than maxTemplateDepth or using a blocked key.
func visit(value any, depth int, fn func(string) (any, error)) (any, error) {
	if depth > maxTemplateDepth {
		return nil, errors.New("request template is nested too deeply")
	}
	switch v := value.(type) {
	case string:
		return fn(v)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			rendered, err := visit(item, depth+1, fn)
			if err != nil {
				return nil, err
			}
			out[i] = rendered
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			if slices.Contains(blockedKeys, key) {
				return nil, errors.New("unsafe template key")
			}
			rendered, err := visit(item, depth+1, fn)
			if err != nil {
				return nil, err
			}
			out[key] = rendered
		}
		return out, nil
	}
	return value, nil
}

// replaceTokens substitutes every {{path}} in s with fn(path), stopping at
// the first error.
func replaceTokens(s string, fn func(path string) (string, error)) (string, error) {
	var firstErr error
	out := tokenPattern.ReplaceAllStringFunc(s, func(match string) string {
		if firstErr != nil {
			return ""
		}
		repl, err := fn(tokenPattern.FindStringSubmatch(match)[1])
		if err != nil {
			firstErr = err
		}
		return repl
	})
	return out, firstErr
}

// BuildRequest renders the node's request template from a translated OpenAI
// chat request. A string that is exactly one placeholder takes the value's own
// JSON type; an embedded placeholder is interpolated as text.
func BuildRequest(cfg Config, model string, body []byte) ([]byte, error) {
	request := map[string]any{}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &request); err != nil {
			return nil, fmt.Errorf("customjson.BuildRequest: %w", err)
		}
	}
	messages, _ := request["messages"].([]any)
	if messages == nil {
		messages = []any{}
	}
	vars := map[string]any{
		"prompt":   latestUserText(messages),
		"messages": messages,
		"model":    model,
		"stream":   false,
		"request":  request,
	}
	resolve := func(path string) (any, error) {
		path = strings.TrimSpace(path)
		parts, err := pathParts(path)
		if err != nil {
			return nil, err
		}
		v, found := readPath(vars, parts)
		if !found {
			return nil, fmt.Errorf("missing template value: %s", path)
		}
		return v, nil
	}
	rendered, err := visit(cfg.RequestTemplate, 0, func(s string) (any, error) {
		if m := exactToken.FindStringSubmatch(s); m != nil {
			return resolve(m[1])
		}
		return replaceTokens(s, func(path string) (string, error) {
			v, err := resolve(path)
			if err != nil {
				return "", err
			}
			return textOf(v), nil
		})
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(rendered, json.Deterministic(true))
}

// latestUserText is the text of the most recent user message: a string
// content verbatim, or the text parts of a multi-part content joined by
// newlines.
func latestUserText(messages []any) string {
	for _, raw := range slices.Backward(messages) {
		msg, _ := raw.(map[string]any)
		if msg["role"] != "user" {
			continue
		}
		switch content := msg["content"].(type) {
		case string:
			return content
		case []any:
			var texts []string
			for _, p := range content {
				part, _ := p.(map[string]any)
				if part["type"] == "text" {
					text, _ := part["text"].(string)
					texts = append(texts, text)
				}
			}
			return strings.Join(texts, "\n")
		}
		return ""
	}
	return ""
}

// textOf renders a value for string interpolation: strings as-is, every
// other JSON value as its encoding.
func textOf(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	out, err := json.Marshal(v, json.Deterministic(true))
	if err != nil {
		return ""
	}
	return string(out)
}

// Headers builds the outbound headers: the node's custom headers, JSON
// content negotiation, and the connection's secret in the configured place.
func Headers(cfg Config, apiKey string) (map[string]string, error) {
	if cfg.AuthType != AuthNone && apiKey == "" {
		return nil, errors.New("API key is required for this custom JSON endpoint")
	}
	headers := maps.Clone(cfg.Headers)
	if headers == nil {
		headers = map[string]string{}
	}
	headers["Content-Type"] = "application/json"
	headers["Accept"] = "application/json"
	switch cfg.AuthType {
	case AuthBearer:
		headers["Authorization"] = "Bearer " + apiKey
	case AuthAPIKey:
		maps.DeleteFunc(headers, func(name, _ string) bool { return strings.EqualFold(name, cfg.AuthHeader) })
		headers[cfg.AuthHeader] = apiKey
	}
	return headers, nil
}

// ExtractText lifts the reply out of the endpoint's JSON response. Strings are
// returned verbatim; any other value is returned as its JSON encoding.
func ExtractText(cfg Config, data []byte) (string, error) {
	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return "", errors.New("invalid JSON response")
	}
	parts, err := pathParts(cfg.ResponsePath)
	if err != nil {
		return "", err
	}
	v, found := readPath(decoded, parts)
	if !found || v == nil {
		return "", fmt.Errorf("response field not found: %s", cfg.ResponsePath)
	}
	return textOf(v), nil
}

// IsConnection reports whether a connection's providerSpecificData selects a
// custom JSON endpoint.
func IsConnection(psd map[string]any) bool {
	apiType, _ := psd["apiType"].(string)
	return apiType == APIType
}

// FromConnection reads and re-validates the configuration a connection
// carries, returning it with the endpoint it applies to.
func FromConnection(psd map[string]any) (Config, string, error) {
	endpoint, _ := psd["baseUrl"].(string)
	raw, err := json.Marshal(psd["customJson"])
	if err != nil {
		return Config{}, "", fmt.Errorf("customjson.FromConnection: %w", err)
	}
	cfg, err := Normalize(jsontext.Value(raw), endpoint)
	return cfg, strings.TrimSpace(endpoint), err
}
