package translator

import (
	json "encoding/json/v2"
	"testing"
)

func filterBody(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return FilterToOpenAIFormat(m)
}

func TestFilterToOpenAIFormat(t *testing.T) {
	t.Run("messages missing returns identical", func(t *testing.T) {
		in := map[string]any{"model": "m"}
		if got := FilterToOpenAIFormat(in); len(got) != 1 {
			t.Fatalf("changed: %+v", got)
		}
	})
	t.Run("developer becomes system", func(t *testing.T) {
		out := filterBody(t, map[string]any{"messages": []any{
			map[string]any{"role": "developer", "content": "be nice"},
		}})
		msgs := out["messages"].([]any)
		if msgs[0].(map[string]any)["role"] != "system" {
			t.Fatalf("role = %+v", msgs[0])
		}
	})
	t.Run("thinking blocks dropped, signature stripped", func(t *testing.T) {
		out := filterBody(t, map[string]any{"messages": []any{
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "thinking", "thinking": "hmm"},
				map[string]any{"type": "redacted_thinking", "data": "x"},
				map[string]any{"type": "text", "text": "hi", "signature": "sig", "cache_control": map[string]any{}},
				map[string]any{"type": "tool_use", "id": "1"},
			}},
		}})
		parts := out["messages"].([]any)[0].(map[string]any)["content"].([]any)
		if len(parts) != 1 || parts[0].(map[string]any)["type"] != "text" {
			t.Fatalf("parts = %+v", parts)
		}
		if _, has := parts[0].(map[string]any)["signature"]; has {
			t.Fatalf("signature kept: %+v", parts[0])
		}
	})
	t.Run("all dropped message removed by empty filter", func(t *testing.T) {
		out := filterBody(t, map[string]any{"messages": []any{
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "thinking", "thinking": "hmm"},
			}},
			map[string]any{"role": "user", "content": "hi"},
		}})
		msgs := out["messages"].([]any)
		if len(msgs) != 1 || msgs[0].(map[string]any)["role"] != "user" {
			t.Fatalf("messages = %+v", msgs)
		}
	})
	t.Run("empty tools deleted", func(t *testing.T) {
		out := filterBody(t, map[string]any{
			"messages": []any{map[string]any{"role": "user", "content": "hi"}},
			"tools":    []any{},
		})
		if _, has := out["tools"]; has {
			t.Fatalf("tools kept: %+v", out)
		}
	})
	t.Run("claude tools normalised", func(t *testing.T) {
		out := filterBody(t, map[string]any{
			"messages": []any{map[string]any{"role": "user", "content": "hi"}},
			"tools": []any{
				map[string]any{"name": "w", "description": "d", "input_schema": map[string]any{"type": "object"}},
				map[string]any{"functionDeclarations": []any{
					map[string]any{"name": "g", "parameters": map[string]any{"type": "object"}},
				}},
			},
		})
		tools := out["tools"].([]any)
		if len(tools) != 2 {
			t.Fatalf("tools = %+v", tools)
		}
		fn := tools[0].(map[string]any)
		if fn["type"] != "function" || fn["function"].(map[string]any)["name"] != "w" {
			t.Fatalf("tool = %+v", fn)
		}
	})
	t.Run("tool_choice normalised", func(t *testing.T) {
		out := filterBody(t, map[string]any{
			"messages":    []any{map[string]any{"role": "user", "content": "hi"}},
			"tool_choice": map[string]any{"type": "any"},
		})
		if out["tool_choice"] != "required" {
			t.Fatalf("tool_choice = %+v", out["tool_choice"])
		}
		out = filterBody(t, map[string]any{
			"messages":    []any{map[string]any{"role": "user", "content": "hi"}},
			"tool_choice": map[string]any{"type": "tool", "name": "w"},
		})
		tc := out["tool_choice"].(map[string]any)
		if tc["function"].(map[string]any)["name"] != "w" {
			t.Fatalf("tool_choice = %+v", tc)
		}
	})
	t.Run("tool_result kept", func(t *testing.T) {
		out := filterBody(t, map[string]any{"messages": []any{
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "1", "content": "ok"},
			}},
		}})
		parts := out["messages"].([]any)[0].(map[string]any)["content"].([]any)
		if len(parts) != 1 || parts[0].(map[string]any)["type"] != "tool_result" {
			t.Fatalf("parts = %+v", parts)
		}
	})
}

func TestStripContentTypes(t *testing.T) {
	body := func() map[string]any {
		return map[string]any{"messages": []any{
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "hi"},
				map[string]any{"type": "image_url", "image_url": map[string]any{}},
				map[string]any{"type": "input_audio", "input_audio": map[string]any{}},
			}},
			map[string]any{"role": "user", "content": "plain"},
		}}
	}
	b := body()
	StripContentTypes(b, []string{"image", "audio"})
	parts := b["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if len(parts) != 1 || parts[0].(map[string]any)["type"] != "text" {
		t.Fatalf("parts = %+v", parts)
	}
	if c := b["messages"].([]any)[1].(map[string]any)["content"]; c != "plain" {
		t.Fatalf("string content touched: %v", c)
	}
	b = body()
	StripContentTypes(b, []string{"image"})
	parts = b["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if len(parts) != 2 {
		t.Fatalf("partial strip = %+v", parts)
	}
	b = body()
	StripContentTypes(b, nil)
	if len(b["messages"].([]any)[0].(map[string]any)["content"].([]any)) != 3 {
		t.Fatal("empty list stripped")
	}
	// emptied array becomes "".
	b = map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "image_url"},
		}},
	}}
	StripContentTypes(b, []string{"image"})
	if b["messages"].([]any)[0].(map[string]any)["content"] != "" {
		t.Fatalf("emptied = %+v", b["messages"])
	}
}
