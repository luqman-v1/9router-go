package translator

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"strings"
)

// Penyamaran nama tool untuk gate free-tier OpenCode Zen.
//
// Gate upstream memfingerprint klien resminya lewat nama tool file-search dalam
// huruf KECIL (bash, glob, grep, read). Claude Code CLI mengirim tool bawaan
// yang sama dalam huruf Kapital (Bash, Glob, Grep, Read).
//
// Diukur langsung ke upstream (2026-09-18):
//
//	Kapital saja          -> HTTP 403 FreeTierError
//	Kapital + huruf kecil -> HTTP 500 server_error (upstream melihat duplikat)
//	huruf kecil saja      -> HTTP 200
//
// Karena itu varian Kapital di-RENAME (bukan ditambah sebagai tool kedua), lalu
// namanya dipulihkan di respons supaya klien tetap mengenali tool-nya sendiri.
// Porting dari fix decolua/9router (opencodeFingerprint.js).

// OpenCodeFingerprintTools adalah nama kanonik yang dicari gate upstream.
var OpenCodeFingerprintTools = []string{"bash", "glob", "grep", "read"}

// FingerprintToolKey mengembalikan nama kanonik huruf kecil bila name adalah
// anggota quartet, atau "" bila bukan.
func FingerprintToolKey(name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, t := range OpenCodeFingerprintTools {
		if lower == t {
			return t
		}
	}
	return ""
}

// toolNameOf mengambil nama tool dari dua bentuk: "name" di level atas (bentuk
// Responses) atau di dalam "function" (bentuk Chat Completions).
func toolNameOf(tool any) string {
	m, ok := tool.(map[string]any)
	if !ok {
		return ""
	}
	if n, ok := m["name"].(string); ok && strings.TrimSpace(n) != "" {
		return strings.TrimSpace(n)
	}
	if fn, ok := m["function"].(map[string]any); ok {
		if n, ok := fn["name"].(string); ok {
			return strings.TrimSpace(n)
		}
	}
	return ""
}

func setToolName(tool any, name string) {
	m, ok := tool.(map[string]any)
	if !ok {
		return
	}
	if _, hasTop := m["name"]; hasTop {
		m["name"] = name
		return
	}
	if fn, ok := m["function"].(map[string]any); ok {
		fn["name"] = name
	}
}

// ConcealFingerprintTools me-rename varian penulisan lain pada quartet menjadi
// huruf kecil, membuang nama kembar (upstream menolak dua tool bernama sama),
// lalu menambahkan quartet yang belum ada sebagai deklarasi no-op.
//
// Mengembalikan body hasil dan peta nama-terkirim -> nama-asli untuk pemulihan
// di respons. Bila tidak ada yang berubah, body asli dikembalikan apa adanya
// beserta peta kosong.
func ConcealFingerprintTools(body []byte) ([]byte, map[string]string) {
	noop := map[string]string{}
	if len(body) == 0 {
		return body, noop
	}

	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body, noop
	}

	// Body tanpa array tools tetap harus disuntik quartet: gate free-tier
	// menolak request tanpa tool (terukur 403, 2026-09-18).
	var tools []any
	if raw, ok := m["tools"].([]any); ok {
		tools = raw
	}

	toolNameMap := map[string]string{}
	seen := map[string]bool{}
	kept := make([]any, 0, len(tools)+len(OpenCodeFingerprintTools))

	for _, tool := range tools {
		current := toolNameOf(tool)
		if current == "" {
			kept = append(kept, tool)
			continue
		}
		key := FingerprintToolKey(current)
		finalName := current
		if key != "" {
			finalName = key
		}
		dedupeKey := strings.ToLower(finalName)
		if seen[dedupeKey] {
			continue
		}
		seen[dedupeKey] = true
		if key != "" && key != current {
			toolNameMap[key] = current
			setToolName(tool, key)
		}
		kept = append(kept, tool)
	}

	// Bentuk yang disuntikkan mengikuti bentuk tool yang sudah ada: Responses
	// memakai "name" di level atas, Chat Completions membungkusnya di "function".
	flat := true
	for _, tool := range kept {
		if tm, ok := tool.(map[string]any); ok {
			if _, has := tm["function"]; has {
				flat = false
			}
			break
		}
	}

	for _, name := range OpenCodeFingerprintTools {
		if seen[name] {
			continue
		}
		entry := map[string]any{
			"type":        "function",
			"description": "OpenCode built-in " + name + " tool",
			"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
		}
		if flat {
			entry["name"] = name
		} else {
			entry["function"] = map[string]any{
				"name":        name,
				"description": "OpenCode built-in " + name + " tool",
				"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
			}
		}
		kept = append(kept, entry)
		seen[name] = true
	}
	m["tools"] = kept

	// Arahkan ulang tool_choice yang menunjuk tool yang baru diganti namanya.
	if choice, ok := m["tool_choice"].(map[string]any); ok {
		if n, ok := choice["name"].(string); ok {
			if key := FingerprintToolKey(n); key != "" && toolNameMap[key] != "" {
				choice["name"] = key
			}
		}
	}

	out, err := json.Marshal(m)
	if err != nil {
		return body, noop
	}
	return out, toolNameMap
}

// RestoreToolNames memulihkan nama asli tool pada payload JSON respons.
// Menangani: Claude content_block_start (tool_use), Chat Completions
// choices[].{delta,message}.tool_calls[].function.name, dan Responses output[]
// dengan item function_call / custom_tool_call.
func RestoreToolNames(payload []byte, toolNameMap map[string]string) []byte {
	if len(toolNameMap) == 0 || len(payload) == 0 {
		return payload
	}

	var m map[string]any
	if err := json.Unmarshal(payload, &m); err != nil {
		return payload
	}

	changed := false

	// Claude: content_block_start dengan content_block.type == "tool_use"
	if block, ok := m["content_block"].(map[string]any); ok && block["type"] == "tool_use" {
		if n, ok := block["name"].(string); ok {
			if orig, found := toolNameMap[n]; found {
				block["name"] = orig
				changed = true
			}
		}
	}

	// OpenAI: choices[].delta.tool_calls[] dan choices[].message.tool_calls[]
	if choices, ok := m["choices"].([]any); ok {
		for _, c := range choices {
			choice, ok := c.(map[string]any)
			if !ok {
				continue
			}
			for _, holder := range []string{"delta", "message"} {
				h, ok := choice[holder].(map[string]any)
				if !ok {
					continue
				}
				calls, ok := h["tool_calls"].([]any)
				if !ok {
					continue
				}
				for _, call := range calls {
					cm, ok := call.(map[string]any)
					if !ok {
						continue
					}
					fn, ok := cm["function"].(map[string]any)
					if !ok {
						continue
					}
					n, ok := fn["name"].(string)
					if !ok || n == "" {
						continue
					}
					if orig, found := toolNameMap[n]; found {
						fn["name"] = orig
						changed = true
					}
				}
			}
		}
	}

	// Responses: output[] dengan item function_call / custom_tool_call
	if output, ok := m["output"].([]any); ok {
		for _, item := range output {
			im, ok := item.(map[string]any)
			if !ok {
				continue
			}
			t, _ := im["type"].(string)
			if t != "function_call" && t != "custom_tool_call" {
				continue
			}
			if n, ok := im["name"].(string); ok {
				if orig, found := toolNameMap[n]; found {
					im["name"] = orig
					changed = true
				}
			}
		}
	}

	if !changed {
		return payload
	}
	out, err := json.Marshal(m)
	if err != nil {
		return payload
	}
	return out
}

// RestoreToolNamesInSSE menerapkan RestoreToolNames pada frame SSE dengan tetap
// mempertahankan awalan "data: " dan bentuk baris aslinya. Frame yang bukan
// data-JSON (mis. "event: ..." atau "[DONE]") diteruskan apa adanya.
func RestoreToolNamesInSSE(chunk []byte, toolNameMap map[string]string) []byte {
	if len(toolNameMap) == 0 || len(chunk) == 0 {
		return chunk
	}
	if !bytes.Contains(chunk, []byte("data:")) {
		return chunk
	}

	var out bytes.Buffer
	out.Grow(len(chunk))
	remaining := chunk
	changedAny := false

	for len(remaining) > 0 {
		nl := bytes.IndexByte(remaining, '\n')
		var line []byte
		if nl < 0 {
			line = remaining
			remaining = nil
		} else {
			line = remaining[:nl+1]
			remaining = remaining[nl+1:]
		}

		body := bytes.TrimRight(line, "\r\n")
		if bytes.HasPrefix(body, []byte("data:")) {
			jsonPart := bytes.TrimSpace(body[len("data:"):])
			if len(jsonPart) > 0 && !bytes.Equal(jsonPart, []byte("[DONE]")) {
				restored := RestoreToolNames(jsonPart, toolNameMap)
				if !bytes.Equal(restored, jsonPart) {
					out.WriteString("data: ")
					out.Write(restored)
					out.WriteByte('\n')
					changedAny = true
					continue
				}
			}
		}
		out.Write(line)
	}

	if !changedAny {
		return chunk
	}
	return out.Bytes()
}

// RestoreToolNamesInPayload memilih bentuk yang tepat: frame SSE bila potongan
// berisi baris "data:", atau satu body JSON utuh untuk respons non-streaming.
func RestoreToolNamesInPayload(chunk []byte, toolNameMap map[string]string) []byte {
	if len(toolNameMap) == 0 || len(chunk) == 0 {
		return chunk
	}
	if bytes.Contains(chunk, []byte("data:")) {
		return RestoreToolNamesInSSE(chunk, toolNameMap)
	}
	trimmed := bytes.TrimSpace(chunk)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return chunk
	}
	return RestoreToolNames(trimmed, toolNameMap)
}

// contextKeyToolNameMap adalah kunci context untuk peta nama tool yang disamarkan.
// Jalur respons hanya menerima ctx (bukan Request), jadi peta dititipkan di sini.
type contextKeyToolNameMap struct{}

// WithToolNameMap menyimpan peta nama tool yang disamarkan pada context.
func WithToolNameMap(ctx context.Context, toolNameMap map[string]string) context.Context {
	if len(toolNameMap) == 0 {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, contextKeyToolNameMap{}, toolNameMap)
}

// ToolNameMapFromContext mengambil peta yang disimpan WithToolNameMap.
func ToolNameMapFromContext(ctx context.Context) map[string]string {
	if ctx == nil {
		return nil
	}
	m, _ := ctx.Value(contextKeyToolNameMap{}).(map[string]string)
	return m
}
