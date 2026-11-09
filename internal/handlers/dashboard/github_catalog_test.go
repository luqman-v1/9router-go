package dashboard

import (
	"testing"
)

func TestIsRoutableCopilotChatModel(t *testing.T) {
	tests := []struct {
		name string
		item copilotCatalogItem
		want bool
	}{
		{
			name: "enabled chat model",
			item: copilotCatalogItem{ID: "gpt-5", Policy: struct {
				State string `json:"state"`
			}{State: "enabled"}, Capabilities: struct {
				Type     string `json:"type"`
				Family   string `json:"family"`
				Limits   any    `json:"limits"`
				Supports struct {
					Reasoning any `json:"reasoning"`
				} `json:"supports"`
			}{Type: "chat"}},
			want: true,
		},
		{
			name: "disabled policy state is dropped",
			item: copilotCatalogItem{ID: "gpt-5", Policy: struct {
				State string `json:"state"`
			}{State: "disabled"}, Capabilities: struct {
				Type     string `json:"type"`
				Family   string `json:"family"`
				Limits   any    `json:"limits"`
				Supports struct {
					Reasoning any `json:"reasoning"`
				} `json:"supports"`
			}{Type: "chat"}},
			want: false,
		},
		{
			name: "empty policy state is treated as enabled",
			item: copilotCatalogItem{ID: "gpt-5", Capabilities: struct {
				Type     string `json:"type"`
				Family   string `json:"family"`
				Limits   any    `json:"limits"`
				Supports struct {
					Reasoning any `json:"reasoning"`
				} `json:"supports"`
			}{Type: "chat"}},
			want: true,
		},
		{
			name: "embeddings kind is dropped",
			item: copilotCatalogItem{ID: "text-embed-3", Capabilities: struct {
				Type     string `json:"type"`
				Family   string `json:"family"`
				Limits   any    `json:"limits"`
				Supports struct {
					Reasoning any `json:"reasoning"`
				} `json:"supports"`
			}{Type: "embeddings"}},
			want: false,
		},
		{
			name: "missing id and model is dropped",
			item: copilotCatalogItem{Capabilities: struct {
				Type     string `json:"type"`
				Family   string `json:"family"`
				Limits   any    `json:"limits"`
				Supports struct {
					Reasoning any `json:"reasoning"`
				} `json:"supports"`
			}{Type: "chat"}},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRoutableCopilotChatModel(tt.item); got != tt.want {
				t.Errorf("isRoutableCopilotChatModel() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseCopilotCatalog(t *testing.T) {
	// A live-shaped answer: capability-driven filter keeps gpt-5 (chat, enabled)
	// and the no-capability-block chat endpoint entry, drops the disabled entry
	// and the embeddings entry.
	raw := []byte(`{
		"data": [
			{"id":"gpt-5","name":"GPT 5","capabilities":{"type":"chat"},"policy":{"state":"enabled"}},
			{"id":"gpt-4","name":"GPT 4","capabilities":{"type":"chat"},"policy":{"state":"disabled"}},
			{"id":"text-embed-3","capabilities":{"type":"embeddings"},"policy":{"state":"enabled"}},
			{"id":"o3-mini","supported_endpoints":[{"endpoint":"/chat/completions"}]},
			{"id":"empty-entry"}
		]
	}`)
	got := parseCopilotCatalog(raw)
	if len(got) != 2 {
		t.Fatalf("parseCopilotCatalog() returned %d models, want 2: %+v", len(got), got)
	}
	if got[0].ID != "gpt-5" || got[1].ID != "o3-mini" {
		t.Errorf("parseCopilotCatalog() ids = [%s %s], want [gpt-5 o3-mini]", got[0].ID, got[1].ID)
	}
	// Sorted by id.
	if got[0].ID > got[1].ID {
		t.Error("parseCopilotCatalog() is not sorted by id")
	}
	// A display name is carried through.
	if got[0].Name != "GPT 5" {
		t.Errorf("parseCopilotCatalog() name = %q, want GPT 5", got[0].Name)
	}
}

func TestParseCopilotCatalog_BareArray(t *testing.T) {
	raw := []byte(`[{"id":"gpt-5","capabilities":{"type":"chat"},"policy":{"state":"enabled"}}]`)
	got := parseCopilotCatalog(raw)
	if len(got) != 1 || got[0].ID != "gpt-5" {
		t.Fatalf("parseCopilotCatalog(bare array) = %+v, want [gpt-5]", got)
	}
}

func TestParseCopilotCatalog_Garbage(t *testing.T) {
	for _, raw := range [][]byte{nil, {}, []byte("not json"), []byte("[]")} {
		if got := parseCopilotCatalog(raw); len(got) != 0 {
			t.Errorf("parseCopilotCatalog(%q) = %+v, want empty", raw, got)
		}
	}
}

func TestCopilotTokenFromData(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{
			name: "derived copilot token wins",
			data: `{"accessToken":"gh-token","providerSpecificData":{"copilotToken":"copilot-bearer"}}`,
			want: "copilot-bearer",
		},
		{
			name: "falls back to github access token",
			data: `{"accessToken":"gh-token"}`,
			want: "gh-token",
		},
		{
			name: "falls back to api key",
			data: `{"apiKey":"sk-x"}`,
			want: "sk-x",
		},
		{name: "empty data", data: "", want: ""},
		{name: "invalid json", data: "{not json", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := copilotTokenFromData(tt.data); got != tt.want {
				t.Errorf("copilotTokenFromData() = %q, want %q", got, tt.want)
			}
		})
	}
}
