package customjson

import (
	"reflect"
	"strings"
	"testing"

	"encoding/json/jsontext"
	json "encoding/json/v2"
)

const testEndpoint = "https://api.example.com/api/run?version=2"

func rawConfigOf(t *testing.T, overrides map[string]any) jsontext.Value {
	t.Helper()
	cfg := map[string]any{
		"method":     "POST",
		"authType":   "bearer",
		"authHeader": "X-API-Key",
		"headers":    map[string]any{"X-App": "9router"},
		"requestTemplate": map[string]any{
			"project_id": "{{prompt}}", "history": "{{messages}}", "model": "{{model}}", "stream": "{{stream}}",
		},
		"responsePath": "data.answers[0].text",
	}
	for k, v := range overrides {
		cfg[k] = v
	}
	out, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	return out
}

func mustNormalize(t *testing.T, raw jsontext.Value) Config {
	t.Helper()
	cfg, err := Normalize(raw, testEndpoint)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	return cfg
}

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return m
}

func TestBuildRequestPreservesTypesAndInterpolatesText(t *testing.T) {
	content := "Example \"input\"\n基础"
	body := `{"messages":[{"role":"user","content":"Example \"input\"\n基础"}],"project_id":"demo-project"}`
	cfg := mustNormalize(t, rawConfigOf(t, map[string]any{"requestTemplate": map[string]any{
		"project_id": "{{prompt}}", "history": "{{messages}}", "model": "{{model}}", "stream": "{{stream}}",
		"name": "{{request.project_id}}", "greeting": "Input: {{ prompt }}", "enabled": true, "limit": 3,
	}}))
	out, err := BuildRequest(cfg, "assistant", []byte(body))
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	got := decode(t, out)
	want := map[string]any{
		"project_id": content,
		"history":    []any{map[string]any{"role": "user", "content": content}},
		"model":      "assistant", "stream": false, "name": "demo-project",
		"greeting": "Input: " + content, "enabled": true, "limit": float64(3),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BuildRequest =\n%v\nwant\n%v", got, want)
	}
}

func TestBuildRequestUsesLatestUserTextParts(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"old"},{"role":"user","content":[{"type":"text","text":"hello"},{"type":"image_url","image_url":{}},{"type":"text","text":"world"}]},{"role":"assistant","content":"reply"}]}`
	cfg := mustNormalize(t, rawConfigOf(t, nil))
	out, err := BuildRequest(cfg, "assistant", []byte(body))
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if got := decode(t, out)["project_id"]; got != "hello\nworld" {
		t.Errorf("prompt = %q, want %q", got, "hello\nworld")
	}
}

func TestBuildRequestDefaultTemplate(t *testing.T) {
	cfg := mustNormalize(t, jsontext.Value(`{}`))
	out, err := BuildRequest(cfg, "chat-model", []byte(`{"messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	want := map[string]any{"model": "chat-model", "messages": []any{map[string]any{"role": "user", "content": "hello"}}}
	if got := decode(t, out); !reflect.DeepEqual(got, want) {
		t.Errorf("default template = %v, want %v", got, want)
	}
	text, err := ExtractText(cfg, []byte(`{"choices":[{"message":{"content":"hello back"}}]}`))
	if err != nil || text != "hello back" {
		t.Errorf("default response path = %q, %v", text, err)
	}
}

func TestBuildRequestMissingVariableFails(t *testing.T) {
	cfg := mustNormalize(t, rawConfigOf(t, map[string]any{"requestTemplate": map[string]any{"project_id": "{{request.missing}}"}}))
	_, err := BuildRequest(cfg, "x", []byte(`{"messages":[]}`))
	if err == nil || !strings.Contains(err.Error(), "Missing template value") {
		t.Fatalf("err = %v, want Missing template value", err)
	}
}

func TestNormalizeRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name  string
		field string
		value any
	}{
		{"template array", "requestTemplate", []any{}},
		{"unknown variable", "requestTemplate", map[string]any{"x": "{{secret}}"}},
		{"unsafe request path", "requestTemplate", map[string]any{"x": "{{request.__proto__}}"}},
		{"unsafe template key", "requestTemplate", map[string]any{"__proto__": map[string]any{"polluted": true}}},
		{"unclosed placeholder", "requestTemplate", map[string]any{"x": "{{prompt}"}},
		{"wildcard response path", "responsePath", "data.*"},
		{"unsafe response path", "responsePath", "constructor"},
		{"empty response path", "responsePath", "  "},
		{"authorization header", "headers", map[string]any{"Authorization": "secret"}},
		{"header injection", "headers", map[string]any{"X-App": "x\r\ny"}},
		{"method", "method", "TRACE"},
		{"auth type", "authType", "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Normalize(rawConfigOf(t, map[string]any{tt.field: tt.value}), testEndpoint); err == nil {
				t.Errorf("Normalize accepted %s = %v", tt.field, tt.value)
			}
		})
	}
	t.Run("deeply nested template", func(t *testing.T) {
		var nested any = "{{prompt}}"
		for range maxTemplateDepth + 1 {
			nested = map[string]any{"x": nested}
		}
		if _, err := Normalize(rawConfigOf(t, map[string]any{"requestTemplate": nested}), testEndpoint); err == nil {
			t.Error("Normalize accepted a template nested too deeply")
		}
	})
	t.Run("missing configuration", func(t *testing.T) {
		if _, err := Normalize(jsontext.Value(`null`), testEndpoint); err == nil {
			t.Error("Normalize accepted null")
		}
	})
}

func TestNormalizeRejectsInvalidEndpoint(t *testing.T) {
	for _, endpoint := range []string{"", "file:///etc/passwd", "https://user:secret@example.com", "javascript:alert(1)", "https://example.com/#fragment"} {
		t.Run(endpoint, func(t *testing.T) {
			if _, err := Normalize(rawConfigOf(t, nil), endpoint); err == nil {
				t.Errorf("Normalize accepted endpoint %q", endpoint)
			}
		})
	}
}

func TestNormalizeDropsUndocumentedFields(t *testing.T) {
	cfg := mustNormalize(t, rawConfigOf(t, map[string]any{"script": "alert(1)"}))
	if bytesContain(cfg.Marshal(), "script") {
		t.Errorf("stored config kept an undocumented field: %s", cfg.Marshal())
	}
}

func bytesContain(b jsontext.Value, s string) bool { return strings.Contains(string(b), s) }

func TestExtractText(t *testing.T) {
	cfg := mustNormalize(t, rawConfigOf(t, nil))
	if got, err := ExtractText(cfg, []byte(`{"data":{"answers":[{"text":"OK"}]}}`)); err != nil || got != "OK" {
		t.Errorf("ExtractText = %q, %v", got, err)
	}
	whole := Config{ResponsePath: "$"}
	for _, raw := range []string{`false`, `0`, `{"project":"demo-project"}`} {
		if got, err := ExtractText(whole, []byte(raw)); err != nil || got != raw {
			t.Errorf("ExtractText($, %s) = %q, %v", raw, got, err)
		}
	}
	for _, raw := range []string{`{}`, `{"data":{"answers":[{"text":null}]}}`, `<html>`} {
		if _, err := ExtractText(cfg, []byte(raw)); err == nil {
			t.Errorf("ExtractText(%s) succeeded, want error", raw)
		}
	}
}

func TestHeaders(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		apiKey  string
		want    map[string]string
		wantErr bool
	}{
		{
			name:   "bearer",
			cfg:    Config{AuthType: AuthBearer, Headers: map[string]string{"X-App": "9router"}},
			apiKey: "secret",
			want:   map[string]string{"X-App": "9router", "Content-Type": "application/json", "Accept": "application/json", "Authorization": "Bearer secret"},
		},
		{
			name:   "api key replaces a same-named custom header",
			cfg:    Config{AuthType: AuthAPIKey, AuthHeader: "X-Service-Key", Headers: map[string]string{"x-service-key": "stale"}},
			apiKey: "secret",
			want:   map[string]string{"Content-Type": "application/json", "Accept": "application/json", "X-Service-Key": "secret"},
		},
		{
			name: "none sends no credential",
			cfg:  Config{AuthType: AuthNone},
			want: map[string]string{"Content-Type": "application/json", "Accept": "application/json"},
		},
		{name: "key required", cfg: Config{AuthType: AuthBearer}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Headers(tt.cfg, tt.apiKey)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Headers error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Headers = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFromConnection(t *testing.T) {
	psd := map[string]any{
		"apiType":    APIType,
		"baseUrl":    testEndpoint,
		"customJson": map[string]any{"authType": "none", "responsePath": "answer"},
	}
	if !IsConnection(psd) {
		t.Fatal("IsConnection = false")
	}
	cfg, endpoint, err := FromConnection(psd)
	if err != nil || endpoint != testEndpoint || cfg.AuthType != AuthNone || cfg.ResponsePath != "answer" {
		t.Errorf("FromConnection = %+v, %q, %v", cfg, endpoint, err)
	}
	if IsConnection(map[string]any{"apiType": "chat"}) {
		t.Error("IsConnection(chat) = true")
	}
}
