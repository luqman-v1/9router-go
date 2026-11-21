package chat

import (
	"encoding/json/jsontext"
	"testing"

	json "encoding/json/v2"
)

func TestMergeExtraBody(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		extra string
		want  string
	}{
		{
			name:  "operator overrides client on a shared key",
			body:  `{"model":"m","temperature":0.2}`,
			extra: `{"temperature":0.9,"top_k":3}`,
			want:  `{"model":"m","temperature":0.9,"top_k":3}`,
		},
		{
			name:  "provider is not merged: legacy key dropped, client value kept",
			body:  `{"model":"m","provider":{"sort":"price"}}`,
			extra: `{"provider":{"order":["b"]},"plugins":[{"id":"web"}],"top_k":1}`,
			want:  `{"model":"m","provider":{"sort":"price"},"top_k":1}`,
		},
		{
			name:  "reasoning is replaced whole",
			body:  `{"model":"m","reasoning":{"effort":"high","exclude":true}}`,
			extra: `{"reasoning":{"max_tokens":100}}`,
			want:  `{"model":"m","reasoning":{"max_tokens":100}}`,
		},
		{
			name:  "invalid value dropped, valid kept",
			body:  `{"model":"m"}`,
			extra: `{"temperature":"hot","top_k":2}`,
			want:  `{"model":"m","top_k":2}`,
		},
		{
			name:  "max_tokens with effort drops reasoning",
			body:  `{"model":"m"}`,
			extra: `{"reasoning":{"max_tokens":9},"reasoning_effort":"low","top_k":2}`,
			want:  `{"model":"m","top_k":2}`,
		},
		{
			name:  "arrays are replaced",
			body:  `{"model":"m","stop":["a","b"]}`,
			extra: `{"stop":["c"]}`,
			want:  `{"model":"m","stop":["c"]}`,
		},
		{
			name:  "keys outside the allowlist are refused at merge time",
			body:  `{"model":"m","stream":true}`,
			extra: `{"model":"evil","stream":false,"tools":[],"Temperature":1,"top_k":5}`,
			want:  `{"model":"m","stream":true,"top_k":5}`,
		},
		{
			name:  "large integers keep their bytes",
			body:  `{"model":"m","seed":12345678901234567}`,
			extra: `{"top_p":1}`,
			want:  `{"model":"m","seed":12345678901234567,"top_p":1}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var extra map[string]jsontext.Value
			if err := json.Unmarshal([]byte(tt.extra), &extra); err != nil {
				t.Fatalf("decode extra: %v", err)
			}
			got, err := mergeExtraBody([]byte(tt.body), extra)
			if err != nil {
				t.Fatalf("mergeExtraBody: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("mergeExtraBody = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestApplyCustomExtraBodyGate(t *testing.T) {
	h := newExtraBodyTestHandler(t, `{"providerAlias":"openrouter","id":"vendor/m:free","extraBody":{"top_k":7}}`)
	body := []byte(`{"model":"vendor/m:free"}`)
	tests := []struct {
		name, provider, model, want string
	}{
		{"openrouter canonical id", "openrouter", "vendor/m:free", `{"model":"vendor/m:free","top_k":7}`},
		{"openrouter alias", "or", "vendor/m:free", `{"model":"vendor/m:free","top_k":7}`},
		{"sibling provider untouched", "openai", "vendor/m:free", string(body)},
		{"no entry untouched", "openrouter", "vendor/other", string(body)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(h.applyCustomExtraBody(body, tt.provider, tt.model)); got != tt.want {
				t.Errorf("applyCustomExtraBody = %s, want %s", got, tt.want)
			}
		})
	}
}

// A client body the merge cannot decode (duplicate member, invalid UTF-8) is
// forwarded unchanged rather than rewritten from a partial parse.
func TestApplyCustomExtraBodyInvalidClientBody(t *testing.T) {
	h := newExtraBodyTestHandler(t, `{"providerAlias":"openrouter","id":"vendor/m:free","extraBody":{"top_k":7}}`)
	for name, body := range map[string]string{
		"duplicate member": `{"model":"vendor/m:free","model":"x"}`,
		"invalid utf-8":    `{"model":"vendor/m:free","x":"` + string([]byte{0xff}) + `"}`,
		"not an object":    `[1]`,
	} {
		t.Run(name, func(t *testing.T) {
			if got := string(h.applyCustomExtraBody([]byte(body), "openrouter", "vendor/m:free")); got != body {
				t.Errorf("applyCustomExtraBody = %q, want the body unchanged", got)
			}
		})
	}
}

func newExtraBodyTestHandler(t *testing.T, row string) *ChatHandler {
	t.Helper()
	repo := newTestRepo(t)
	if err := repo.SetKV("customModels", "openrouter|vendor/m:free|llm", row); err != nil {
		t.Fatalf("seed custom model: %v", err)
	}
	return &ChatHandler{Repo: repo}
}
