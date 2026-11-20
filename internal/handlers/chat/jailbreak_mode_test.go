package chat

import (
	"context"
	"encoding/json"
	"testing"

	"9router/proxy/internal/db"
)

// Injection position and mode. The one rule these tests defend is that empty
// fields mean "exactly what this build did before the knobs existed": a
// preset saved by an older release, or imported from a file, must land
// byte-identical to legacy prepend and must not migrate on read. Position
// "append" moves the prompt after the original content; mode "chat" sends it
// as a scripted trailing user turn instead of a system prompt.

func TestInjectJailbreakPromptDefaultIsByteIdenticalToLegacyPrepend(t *testing.T) {
	repo := newTestRepo(t)
	if err := repo.SaveJailbreakDoc(&db.JailbreakDoc{Prompt: "CUSTOM RULES"}); err != nil {
		t.Fatalf("save doc: %v", err)
	}
	const armed = "wal/glm-5.3-flash"
	if err := repo.SetJailbreakModel(armed, true); err != nil {
		t.Fatalf("enable model: %v", err)
	}
	h := &ChatHandler{Repo: repo}

	// Both wire formats, with Position and Mode left at their zero values.
	tests := []struct {
		name string
		body map[string]any
		want string
	}{
		{
			name: "openai messages",
			body: map[string]any{
				"model": "wal/glm-5.3-flash",
				"messages": []any{
					map[string]any{"role": "system", "content": "be helpful"},
					map[string]any{"role": "user", "content": "hi"},
				},
			},
			want: `{"messages":[{"content":"CUSTOM RULES","role":"system"},` +
				`{"content":"be helpful","role":"system"},{"content":"hi","role":"user"}],` +
				`"model":"wal/glm-5.3-flash"}`,
		},
		{
			name: "claude string system",
			body: map[string]any{
				"model":    "anthropic/claude-3-7-sonnet",
				"system":   "base system",
				"messages": []any{map[string]any{"role": "user", "content": "hi"}},
			},
			want: `{"messages":[{"content":"hi","role":"user"}],` +
				`"model":"anthropic/claude-3-7-sonnet","system":"CUSTOM RULES\n\nbase system"}`,
		},
		{
			name: "claude block system",
			body: map[string]any{
				"system": []any{
					map[string]any{"type": "text", "text": "base system", "cache_control": map[string]any{"type": "ephemeral"}},
				},
				"messages": []any{map[string]any{"role": "user", "content": "hi"}},
			},
			want: `{"messages":[{"content":"hi","role":"user"}],"system":` +
				`[{"text":"CUSTOM RULES","type":"text"},` +
				`{"cache_control":{"type":"ephemeral"},"text":"base system","type":"text"}]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			armed := "wal/glm-5.3-flash"
			if tc.body["system"] != nil && tc.body["model"] == nil {
				armed = "anthropic/claude-3-7-sonnet"
				if err := repo.SetJailbreakModel(armed, true); err != nil {
					t.Fatalf("enable claude model: %v", err)
				}
			}
			h.applyJailbreak(context.Background(), tc.body, armed)
			got, _ := json.Marshal(tc.body)
			if string(got) != tc.want {
				t.Errorf("injected body:\n got %s\nwant %s", got, tc.want)
			}
		})
	}

	// Position "system" is the explicit spelling of the default and must land
	// the same bytes.
	spelled := map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	}
	injectJailbreakPrompt(spelled, &db.JailbreakDoc{Prompt: "CUSTOM RULES", Position: "system"})
	got, _ := json.Marshal(spelled)
	want := `{"messages":[{"content":"CUSTOM RULES","role":"system"},{"content":"hi","role":"user"}]}`
	if string(got) != want {
		t.Errorf("position system:\n got %s\nwant %s", got, want)
	}
}

func TestInjectJailbreakPromptAppendLandsAfterOriginal(t *testing.T) {
	tests := []struct {
		name string
		body map[string]any
		want string
	}{
		{
			name: "openai messages gets a trailing system message",
			body: map[string]any{
				"messages": []any{
					map[string]any{"role": "system", "content": "be helpful"},
					map[string]any{"role": "user", "content": "hi"},
				},
			},
			want: `{"messages":[{"content":"be helpful","role":"system"},` +
				`{"content":"hi","role":"user"},{"content":"CUSTOM RULES","role":"system"}]}`,
		},
		{
			name: "claude string system grows after the original text",
			body: map[string]any{
				"system":   "base system",
				"messages": []any{map[string]any{"role": "user", "content": "hi"}},
			},
			want: `{"messages":[{"content":"hi","role":"user"}],` +
				`"system":"base system\n\nCUSTOM RULES"}`,
		},
		{
			name: "claude block system appends a block and keeps the original",
			body: map[string]any{
				"system": []any{
					map[string]any{"type": "text", "text": "base system", "cache_control": map[string]any{"type": "ephemeral"}},
				},
				"messages": []any{map[string]any{"role": "user", "content": "hi"}},
			},
			want: `{"messages":[{"content":"hi","role":"user"}],"system":` +
				`[{"cache_control":{"type":"ephemeral"},"text":"base system","type":"text"},` +
				`{"text":"CUSTOM RULES","type":"text"}]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			injectJailbreakPrompt(tc.body, &db.JailbreakDoc{Prompt: "CUSTOM RULES", Position: "append"})
			got, _ := json.Marshal(tc.body)
			if string(got) != tc.want {
				t.Errorf("injected body:\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestInjectJailbreakPromptChatAppendsUserTurn(t *testing.T) {
	tests := []struct {
		name string
		body map[string]any
		want string
	}{
		{
			name: "openai messages",
			body: map[string]any{
				"messages": []any{
					map[string]any{"role": "system", "content": "be helpful"},
					map[string]any{"role": "user", "content": "hi"},
				},
			},
			want: `{"messages":[{"content":"be helpful","role":"system"},` +
				`{"content":"hi","role":"user"},{"content":"CUSTOM RULES","role":"user"}]}`,
		},
		{
			// The operator's top-level system prompt must survive a chat-mode
			// preset untouched: the Anthropic API rejects a role:"system"
			// message, so nothing about "system" is written in this mode.
			name: "claude top-level system is left alone",
			body: map[string]any{
				"system":   "base system",
				"messages": []any{map[string]any{"role": "user", "content": "hi"}},
			},
			want: `{"messages":[{"content":"hi","role":"user"},` +
				`{"content":"CUSTOM RULES","role":"user"}],"system":"base system"}`,
		},
		{
			name: "claude block system is left alone",
			body: map[string]any{
				"system": []any{map[string]any{"type": "text", "text": "base system"}},
				"messages": []any{
					map[string]any{"role": "user", "content": "hi"},
					map[string]any{"role": "assistant", "content": "hello"},
				},
			},
			want: `{"messages":[{"content":"hi","role":"user"},{"content":"hello","role":"assistant"},` +
				`{"content":"CUSTOM RULES","role":"user"}],` +
				`"system":[{"text":"base system","type":"text"}]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			injectJailbreakPrompt(tc.body, &db.JailbreakDoc{Prompt: "CUSTOM RULES", Mode: "chat"})
			got, _ := json.Marshal(tc.body)
			if string(got) != tc.want {
				t.Errorf("injected body:\n got %s\nwant %s", got, tc.want)
			}
		})
	}

	// No role:"system" message anywhere, even when the body carries none of
	// its own — that is the shape the Anthropic API rejects outright.
	bare := map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}}
	injectJailbreakPrompt(bare, &db.JailbreakDoc{Prompt: "CUSTOM RULES", Mode: "chat"})
	for _, m := range bare["messages"].([]any) {
		if m.(map[string]any)["role"] == "system" {
			t.Errorf("chat mode injected a system message: %v", m)
		}
	}
	if _, ok := bare["system"]; ok {
		t.Error("chat mode created a top-level system field")
	}
}

func TestInjectJailbreakPromptUnarmedModelUntouched(t *testing.T) {
	repo := newTestRepo(t)
	if err := repo.SaveJailbreakPreset(&db.JailbreakDoc{
		ID: "chat-mode", Name: "Chat", Prompt: "CUSTOM RULES", Mode: "chat",
	}); err != nil {
		t.Fatalf("save preset: %v", err)
	}
	const unarmed = "wal/glm-5.3-flash-turbo"
	if err := repo.SetJailbreakModelPreset(unarmed, "chat-mode", false); err != nil {
		t.Fatalf("unbind model: %v", err)
	}
	h := &ChatHandler{Repo: repo}
	body := map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}}
	before, _ := json.Marshal(body)
	h.applyJailbreak(context.Background(), body, unarmed)
	after, _ := json.Marshal(body)
	if string(before) != string(after) {
		t.Errorf("unarmed model was injected: %s -> %s", before, after)
	}
}

func TestInjectJailbreakPromptChatWithAppendStillChatOnly(t *testing.T) {
	// The dashboard validator rejects this pair, so it cannot arrive through
	// the API — but injection must not blow up or double-inject if a stored
	// row from a hand-edited database carries it. Mode wins: the prompt is a
	// user turn, and "append" is simply not a position a user turn has.
	body := map[string]any{
		"system":   "base system",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	}
	injectJailbreakPrompt(body, &db.JailbreakDoc{Prompt: "CUSTOM RULES", Mode: "chat", Position: "append"})
	got, _ := json.Marshal(body)
	want := `{"messages":[{"content":"hi","role":"user"},{"content":"CUSTOM RULES","role":"user"}],` +
		`"system":"base system"}`
	if string(got) != want {
		t.Errorf("chat+append body:\n got %s\nwant %s", got, want)
	}
}
