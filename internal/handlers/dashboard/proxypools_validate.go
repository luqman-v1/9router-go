package dashboard

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/proxy"
)

// proxyPoolType enumerates the valid proxy pool types accepted by /api/proxy-pools.
type proxyPoolType string

const (
	proxyPoolTypeHTTP       proxyPoolType = "http"
	proxyPoolTypeSOCKS5     proxyPoolType = "socks5"
	proxyPoolTypeVercel     proxyPoolType = "vercel"
	proxyPoolTypeCloudflare proxyPoolType = "cloudflare"
	proxyPoolTypeDeno       proxyPoolType = "deno"
	proxyPoolTypeNetlify    proxyPoolType = "netlify"

	validProxyPoolTypesList = "http, socks5, vercel, cloudflare, deno, netlify"
)

func parseProxyPoolType(raw string) (proxyPoolType, bool) {
	t := proxyPoolType(strings.ToLower(strings.TrimSpace(raw)))
	switch t {
	case proxyPoolTypeHTTP, proxyPoolTypeSOCKS5, proxyPoolTypeVercel,
		proxyPoolTypeCloudflare, proxyPoolTypeDeno, proxyPoolTypeNetlify:
		return t, true
	default:
		return "", false
	}
}

func (t proxyPoolType) isRelay() bool {
	switch t {
	case proxyPoolTypeVercel, proxyPoolTypeCloudflare, proxyPoolTypeDeno, proxyPoolTypeNetlify:
		return true
	default:
		return false
	}
}

type createProxyPoolRequest struct {
	Name        string   `json:"name"`
	ProxyURL    string   `json:"proxyUrl"`
	URLs        []string `json:"urls"`
	Type        string   `json:"type"`
	NoProxy     string   `json:"noProxy"`
	StrictProxy *bool    `json:"strictProxy"`
	IsActive    *bool    `json:"isActive"`
}

type updateProxyPoolRequest struct {
	Name        *string   `json:"name"`
	ProxyURL    *string   `json:"proxyUrl"`
	URLs        *[]string `json:"urls"`
	Type        *string   `json:"type"`
	NoProxy     *string   `json:"noProxy"`
	StrictProxy *bool     `json:"strictProxy"`
	IsActive    *bool     `json:"isActive"`
}

type fieldValidationError struct {
	Field  string
	Reason string
}

func (e *fieldValidationError) Error() string {
	if e == nil {
		return ""
	}
	if e.Field == "" {
		return e.Reason
	}
	return e.Field + ": " + e.Reason
}

func writeProxyPoolValidationError(w http.ResponseWriter, field, reason string) {
	msg := reason
	if field != "" {
		msg = field + ": " + reason
	}
	errObj := map[string]any{
		"message": msg,
		"type":    "invalid_request_error",
		"code":    "bad_request",
		"reason":  reason,
	}
	if field != "" {
		errObj["field"] = field
	}
	handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{
		"error": errObj,
	})
}

func decodeProxyPoolJSON(body []byte, dst any) *fieldValidationError {
	if len(bytes.TrimSpace(body)) == 0 {
		return &fieldValidationError{Reason: "request body is empty; expected a JSON object"}
	}
	dec := jsontext.NewDecoder(bytes.NewReader(body))
	kind := dec.PeekKind()
	if kind == jsontext.KindInvalid {
		if _, err := dec.ReadToken(); err != nil {
			return classifyJSONDecodeError(err)
		}
		return &fieldValidationError{Reason: "invalid JSON body"}
	}
	if kind != '{' {
		got := jsonKindName(kind)
		if got == "" {
			got = "non-object value"
		}
		return &fieldValidationError{Reason: fmt.Sprintf("request body must be a JSON object, got %s", got)}
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return classifyJSONDecodeError(err)
	}
	return nil
}

func classifyJSONDecodeError(err error) *fieldValidationError {
	var semErr *json.SemanticError
	if errors.As(err, &semErr) {
		field := formatJSONPointer(semErr.JSONPointer)
		expected := expectedJSONTypeName(semErr.GoType)
		got := jsonKindName(semErr.JSONKind)
		var reason string
		switch {
		case expected != "" && got != "":
			reason = fmt.Sprintf("expected %s, got %s", expected, got)
		case semErr.Err != nil:
			reason = semErr.Err.Error()
		default:
			reason = "invalid value"
		}
		return &fieldValidationError{Field: field, Reason: reason}
	}

	var synErr *jsontext.SyntacticError
	if errors.As(err, &synErr) {
		field := formatJSONPointer(synErr.JSONPointer)
		if errors.Is(synErr.Err, jsontext.ErrDuplicateName) {
			return &fieldValidationError{
				Field:  field,
				Reason: fmt.Sprintf("duplicate JSON object key at byte %d", synErr.ByteOffset),
			}
		}
		reason := fmt.Sprintf("malformed JSON at byte %d", synErr.ByteOffset)
		if synErr.Err != nil {
			reason = fmt.Sprintf("%s: %v", reason, synErr.Err)
		}
		return &fieldValidationError{Field: field, Reason: reason}
	}

	return &fieldValidationError{Reason: "invalid JSON body"}
}

func formatJSONPointer(ptr jsontext.Pointer) string {
	var b strings.Builder
	first := true
	for tok := range ptr.Tokens() {
		if first {
			b.WriteString(tok)
			first = false
			continue
		}
		if isDigits(tok) {
			b.WriteByte('[')
			b.WriteString(tok)
			b.WriteByte(']')
			continue
		}
		b.WriteByte('.')
		b.WriteString(tok)
	}
	return b.String()
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func jsonKindName(k jsontext.Kind) string {
	switch k {
	case 'n':
		return "null"
	case 'f', 't':
		return "boolean"
	case '"':
		return "string"
	case '0':
		return "number"
	case '{':
		return "object"
	case '[':
		return "array"
	default:
		return ""
	}
}

func expectedJSONTypeName(t reflect.Type) string {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil {
		return ""
	}
	switch t.Kind() {
	case reflect.Bool:
		return "boolean"
	case reflect.String:
		return "string"
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.String {
			return "array of strings"
		}
		return "array"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Struct, reflect.Map:
		return "object"
	default:
		return ""
	}
}

func resolveSinglePoolURL(proxyURL string, urls []string) (string, *fieldValidationError) {
	proxyURL = strings.TrimSpace(proxyURL)
	for i, raw := range urls {
		if strings.TrimSpace(raw) == "" {
			return "", &fieldValidationError{
				Field:  fmt.Sprintf("urls[%d]", i),
				Reason: "must be a non-empty proxy URL",
			}
		}
	}
	if len(urls) > 1 {
		return "", &fieldValidationError{
			Field:  "urls",
			Reason: "only a single proxy URL is supported per pool; use proxyUrl",
		}
	}
	if len(urls) == 1 {
		u := strings.TrimSpace(urls[0])
		if proxyURL == "" {
			proxyURL = u
		} else if u != proxyURL {
			return "", &fieldValidationError{
				Field:  "urls",
				Reason: "conflicts with proxyUrl; provide either proxyUrl or matching urls",
			}
		}
	}
	return proxyURL, nil
}

func validateProxyPoolURL(rawURL string, poolType proxyPoolType, field string) *fieldValidationError {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Hostname() == "" {
		return &fieldValidationError{
			Field:  field,
			Reason: "must be a valid URL with scheme and host (e.g. http://host:port or socks5://host:port)",
		}
	}
	scheme := strings.ToLower(u.Scheme)
	switch {
	case poolType.isRelay():
		if scheme != "http" && scheme != "https" {
			return &fieldValidationError{
				Field:  field,
				Reason: fmt.Sprintf("relay type %q requires an http or https URL, got scheme %q", poolType, u.Scheme),
			}
		}
	case poolType == proxyPoolTypeSOCKS5:
		if scheme != "socks5" && scheme != "socks5h" {
			return &fieldValidationError{
				Field:  field,
				Reason: fmt.Sprintf("type %q requires a socks5 or socks5h URL, got scheme %q", poolType, u.Scheme),
			}
		}
	default:
		if !proxy.DialableProxyScheme(scheme) {
			return &fieldValidationError{
				Field:  field,
				Reason: fmt.Sprintf("unsupported proxy scheme %q; expected http, https, socks5, or socks5h", u.Scheme),
			}
		}
	}
	return nil
}

func validateCreateProxyPool(req createProxyPoolRequest) (db.ProxyPoolData, *fieldValidationError) {
	proxyURL, verr := resolveSinglePoolURL(req.ProxyURL, req.URLs)
	if verr != nil {
		return db.ProxyPoolData{}, verr
	}
	if proxyURL == "" {
		return db.ProxyPoolData{}, &fieldValidationError{Field: "proxyUrl", Reason: "is required"}
	}

	var resolvedType proxyPoolType
	if rawType := strings.TrimSpace(req.Type); rawType != "" {
		parsed, ok := parseProxyPoolType(rawType)
		if !ok {
			return db.ProxyPoolData{}, &fieldValidationError{
				Field:  "type",
				Reason: "must be one of: " + validProxyPoolTypesList,
			}
		}
		resolvedType = parsed
	}

	urlField := "proxyUrl"
	if strings.TrimSpace(req.ProxyURL) == "" && len(req.URLs) == 1 {
		urlField = "urls[0]"
	}
	if verr := validateProxyPoolURL(proxyURL, resolvedType, urlField); verr != nil {
		return db.ProxyPoolData{}, verr
	}

	if resolvedType == "" {
		u, _ := url.Parse(proxyURL)
		scheme := strings.ToLower(u.Scheme)
		if scheme == "socks5" || scheme == "socks5h" {
			resolvedType = proxyPoolTypeSOCKS5
		} else {
			resolvedType = proxyPoolTypeHTTP
		}
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Proxy Pool"
	}
	strictProxy := false
	if req.StrictProxy != nil {
		strictProxy = *req.StrictProxy
	}

	return db.ProxyPoolData{
		Name:        name,
		ProxyURL:    proxyURL,
		NoProxy:     strings.TrimSpace(req.NoProxy),
		Type:        string(resolvedType),
		StrictProxy: strictProxy,
		IsActive:    req.IsActive,
	}, nil
}

func buildUpdateProxyPoolMap(req updateProxyPoolRequest, existing *db.ProxyPool) (map[string]any, *fieldValidationError) {
	updates := make(map[string]any)
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			return nil, &fieldValidationError{Field: "name", Reason: "must not be empty"}
		}
		updates["name"] = name
	}

	var effectiveType proxyPoolType
	if existing != nil {
		if parsed, ok := parseProxyPoolType(existing.Type); ok {
			effectiveType = parsed
		}
	}
	if req.Type != nil {
		parsed, ok := parseProxyPoolType(*req.Type)
		if !ok {
			return nil, &fieldValidationError{
				Field:  "type",
				Reason: "must be one of: " + validProxyPoolTypesList,
			}
		}
		effectiveType = parsed
		updates["type"] = string(parsed)
	}

	if req.ProxyURL != nil || req.URLs != nil {
		var pURL string
		var urls []string
		if req.ProxyURL != nil {
			pURL = strings.TrimSpace(*req.ProxyURL)
			if pURL == "" {
				return nil, &fieldValidationError{Field: "proxyUrl", Reason: "must not be empty"}
			}
		}
		if req.URLs != nil {
			if len(*req.URLs) == 0 {
				return nil, &fieldValidationError{Field: "urls", Reason: "must contain a proxy URL"}
			}
			urls = *req.URLs
		}
		resolvedURL, verr := resolveSinglePoolURL(pURL, urls)
		if verr != nil {
			return nil, verr
		}
		urlField := "urls[0]"
		if req.ProxyURL != nil {
			urlField = "proxyUrl"
		}
		if verr := validateProxyPoolURL(resolvedURL, effectiveType, urlField); verr != nil {
			return nil, verr
		}
		updates["proxyUrl"] = resolvedURL
	} else if req.Type != nil && existing != nil && existing.NextURL() != "" {
		if verr := validateProxyPoolURL(existing.NextURL(), effectiveType, "type"); verr != nil {
			return nil, verr
		}
	}

	if req.NoProxy != nil {
		updates["noProxy"] = strings.TrimSpace(*req.NoProxy)
	}
	if req.IsActive != nil {
		updates["isActive"] = *req.IsActive
	}
	if req.StrictProxy != nil {
		updates["strictProxy"] = *req.StrictProxy
	}
	return updates, nil
}
