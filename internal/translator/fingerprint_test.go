package translator

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

// Uji penyamaran + pemulihan nama tool quartet untuk gate free-tier OpenCode.
// Regresi yang dijaga: mengirim "Bash" bersama "bash" membuat upstream menolak
// dengan HTTP 500 (duplikat), sedangkan "Bash" tanpa huruf kecil -> 403.

func toolNames(t *testing.T, body []byte) []string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	raw, _ := m["tools"].([]any)
	names := make([]string, 0, len(raw))
	for _, tool := range raw {
		names = append(names, toolNameOf(tool))
	}
	return names
}

func TestConcealFingerprintToolsTidakMenghasilkanDuplikat(t *testing.T) {
	// Kasus Claude Code CLI: quartet Kapital.
	body := []byte(`{"model":"muse-spark-1.3-contributor-free","tools":[
		{"type":"function","name":"Task"},
		{"type":"function","name":"Bash"},
		{"type":"function","name":"Glob"},
		{"type":"function","name":"Grep"},
		{"type":"function","name":"Read"},
		{"type":"function","name":"Edit"}
	]}`)

	out, map_ := ConcealFingerprintTools(body)
	names := toolNames(t, out)

	seen := map[string]int{}
	for _, n := range names {
		seen[strings.ToLower(n)]++
	}
	for n, c := range seen {
		if c > 1 {
			t.Errorf("nama tool %q muncul %d kali (duplikat memicu HTTP 500 upstream)", n, c)
		}
	}
	if len(map_) != 4 {
		t.Errorf("harus ada 4 nama disamarkan, dapat %d", len(map_))
	}
	if map_["bash"] != "Bash" {
		t.Errorf("peta bash -> %q, ingin \"Bash\"", map_["bash"])
	}
	hasLower := false
	for _, n := range names {
		if n == "bash" {
			hasLower = true
		}
		if n == "Bash" {
			t.Errorf("nama Kapital masih terkirim dalam bentuk Kapital")
		}
	}
	if !hasLower {
		t.Error("quartet huruf kecil tidak ada — upstream akan menolak 403")
	}
	// Tool non-quartet harus utuh.
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "Task") || !strings.Contains(joined, "Edit") {
		t.Errorf("tool non-quartet ikut berubah: %v", names)
	}
}

func TestConcealFingerprintToolsMembuangDuplikatMurni(t *testing.T) {
	body := []byte(`{"tools":[{"name":"Bash"},{"name":"bash"},{"name":"Read"},{"name":"read"}]}`)
	out, _ := ConcealFingerprintTools(body)
	names := toolNames(t, out)
	if len(names) != 4 {
		t.Errorf("ingin 4 tool unik (bash,read + glob,grep disuntik), dapat %d: %v", len(names), names)
	}
}

func TestConcealFingerprintToolsMenyuntikTanpaTool(t *testing.T) {
	// Request tanpa tool tetap ditolak upstream (403), jadi quartet disuntik.
	body := []byte(`{"model":"muse-spark-1.3-contributor-free","input":[]}`)
	out, _ := ConcealFingerprintTools(body)
	names := toolNames(t, out)
	if len(names) != 4 {
		t.Fatalf("ingin 4 tool disuntik, dapat %d: %v", len(names), names)
	}
	for _, n := range names {
		if n != strings.ToLower(n) {
			t.Errorf("tool disuntik harus huruf kecil, dapat %q", n)
		}
	}
}

func TestConcealFingerprintToolsBentukChatCompletions(t *testing.T) {
	// Bentuk chat membungkus nama di dalam "function".
	body := []byte(`{"tools":[{"type":"function","function":{"name":"Bash"}},{"type":"function","function":{"name":"terminal"}}]}`)
	out, map_ := ConcealFingerprintTools(body)
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	raw, _ := m["tools"].([]any)
	foundLower := false
	for _, tool := range raw {
		tm, ok := tool.(map[string]any)
		if !ok {
			continue
		}
		fn, ok := tm["function"].(map[string]any)
		if !ok {
			t.Errorf("struktur .function hilang setelah penyamaran")
			continue
		}
		if fn["name"] == "bash" {
			foundLower = true
		}
	}
	if !foundLower {
		t.Error("nama di dalam .function tidak di-rename ke huruf kecil")
	}
	if map_["bash"] != "Bash" {
		t.Errorf("peta bash -> %q, ingin \"Bash\"", map_["bash"])
	}
}

func TestConcealFingerprintToolsToolChoiceIkutDiarahkan(t *testing.T) {
	body := []byte(`{"tools":[{"name":"Bash"}],"tool_choice":{"type":"tool","name":"Bash"}}`)
	out, _ := ConcealFingerprintTools(body)
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	choice, _ := m["tool_choice"].(map[string]any)
	if choice["name"] != "bash" {
		t.Errorf("tool_choice.name = %v, ingin \"bash\"", choice["name"])
	}
}

func TestConcealFingerprintToolsTanpaPerubahanTidakMengubahBody(t *testing.T) {
	// Body yang sudah punya quartet huruf kecil: peta harus kosong (tak ada yang
	// perlu dipulihkan), dan tidak ada nama kembar yang ditambahkan.
	body := []byte(`{"tools":[{"name":"terminal"},{"name":"bash"},{"name":"glob"},{"name":"grep"},{"name":"read"}]}`)
	out, map_ := ConcealFingerprintTools(body)
	if len(map_) != 0 {
		t.Errorf("peta harus kosong, dapat %v", map_)
	}
	names := toolNames(t, out)
	if len(names) != 5 {
		t.Errorf("tidak boleh ada tambahan, ingin 5 tool, dapat %d: %v", len(names), names)
	}
	seen := map[string]int{}
	for _, n := range names {
		seen[strings.ToLower(n)]++
	}
	for n, c := range seen {
		if c > 1 {
			t.Errorf("nama %q duplikat", n)
		}
	}
}

func TestConcealFingerprintToolsMenyuntikBilaToolsAdaTapiTanpaQuartet(t *testing.T) {
	// Client kirim tool lain tanpa quartet: quartet wajib disuntik (upstream 403
	// tanpa nama huruf kecil itu), dan tool pemanggil tetap utuh.
	body := []byte(`{"tools":[{"name":"terminal"},{"name":"read_file"}]}`)
	out, map_ := ConcealFingerprintTools(body)
	if len(map_) != 0 {
		t.Errorf("tak ada nama Kapital yang di-rename, peta harus kosong: %v", map_)
	}
	names := toolNames(t, out)
	for _, q := range OpenCodeFingerprintTools {
		found := false
		for _, n := range names {
			if n == q {
				found = true
			}
		}
		if !found {
			t.Errorf("quartet %q tidak disuntik: %v", q, names)
		}
	}
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "terminal") || !strings.Contains(joined, "read_file") {
		t.Errorf("tool pemanggil hilang: %v", names)
	}
}

func TestRestoreToolNamesClaudeContentBlockStart(t *testing.T) {
	map_ := map[string]string{"bash": "Bash", "read": "Read"}
	chunk := []byte(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"bash","input":{}}}`)
	out := RestoreToolNames(chunk, map_)
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	block := m["content_block"].(map[string]any)
	if block["name"] != "Bash" {
		t.Errorf("nama = %v, ingin \"Bash\"", block["name"])
	}
}

func TestRestoreToolNamesStreamSEDeltaToolCalls(t *testing.T) {
	map_ := map[string]string{"bash": "Bash"}
	chunk := []byte(`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"bash","arguments":""}}]}}]}`)
	out := RestoreToolNames(chunk, map_)
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	choices := m["choices"].([]any)
	delta := choices[0].(map[string]any)["delta"].(map[string]any)
	calls := delta["tool_calls"].([]any)
	fn := calls[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "Bash" {
		t.Errorf("nama = %v, ingin \"Bash\"", fn["name"])
	}
}

func TestRestoreToolNamesNonStreamMessageToolCalls(t *testing.T) {
	map_ := map[string]string{"grep": "Grep"}
	body := []byte(`{"id":"chatcmpl-1","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"grep","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
	out := RestoreToolNames(body, map_)
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	choices := m["choices"].([]any)
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	calls := msg["tool_calls"].([]any)
	fn := calls[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "Grep" {
		t.Errorf("nama = %v, ingin \"Grep\"", fn["name"])
	}
}

func TestRestoreToolNamesResponsesOutput(t *testing.T) {
	map_ := map[string]string{"read": "Read"}
	body := []byte(`{"id":"resp_1","output":[{"type":"function_call","call_id":"c1","name":"read","arguments":"{}"}]}`)
	out := RestoreToolNames(body, map_)
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	output := m["output"].([]any)
	item := output[0].(map[string]any)
	if item["name"] != "Read" {
		t.Errorf("nama = %v, ingin \"Read\"", item["name"])
	}
}

func TestRestoreToolNamesNoOpTanpaMap(t *testing.T) {
	body := []byte(`{"choices":[{"delta":{"tool_calls":[{"function":{"name":"bash"}}]}}]}`)
	if out := RestoreToolNames(body, nil); string(out) != string(body) {
		t.Error("tanpa peta, body harus dikembalikan apa adanya")
	}
	if out := RestoreToolNames(body, map[string]string{}); string(out) != string(body) {
		t.Error("dengan peta kosong, body harus dikembalikan apa adanya")
	}
}

func TestRestoreToolNamesInSSEMempertahankanFraming(t *testing.T) {
	map_ := map[string]string{"bash": "Bash"}
	chunk := []byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"tool_use\",\"name\":\"bash\"}}\n\n")
	out := string(RestoreToolNamesInSSE([]byte(chunk), map_))
	if !strings.Contains(out, "event: content_block_start") {
		t.Error("baris 'event:' hilang")
	}
	if !strings.Contains(out, `"name":"Bash"`) {
		t.Errorf("nama tidak dipulihkan: %s", out)
	}
	if !strings.HasSuffix(out, "\n\n") {
		t.Error("pemisah frame SSE hilang")
	}
}

func TestRestoreToolNamesInSSEDoneTidakDiubah(t *testing.T) {
	map_ := map[string]string{"bash": "Bash"}
	chunk := []byte("data: [DONE]\n\n")
	if out := RestoreToolNamesInSSE(chunk, map_); string(out) != string(chunk) {
		t.Errorf("[DONE] harus apa adanya, dapat %q", string(out))
	}
}

func TestRestoreToolNamesInPayloadMemilihBentuk(t *testing.T) {
	map_ := map[string]string{"bash": "Bash"}

	// Body JSON non-streaming
	jsonBody := []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"bash"}}]}}]}`)
	if out := string(RestoreToolNamesInPayload(jsonBody, map_)); !strings.Contains(out, `"name":"Bash"`) {
		t.Errorf("jalur JSON gagal: %s", out)
	}

	// Frame SSE
	sse := []byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"function\":{\"name\":\"bash\"}}]}}]}\n\n")
	if out := string(RestoreToolNamesInPayload(sse, map_)); !strings.Contains(out, `"name":"Bash"`) {
		t.Errorf("jalur SSE gagal: %s", out)
	}
}

func TestFingerprintToolKey(t *testing.T) {
	cases := map[string]string{
		"Bash": "bash", " bash ": "bash", "GLOB": "glob",
		"Read": "read", "Grep": "grep",
		"Edit": "", "terminal": "", "": "",
	}
	for in, want := range cases {
		if got := FingerprintToolKey(in); got != want {
			t.Errorf("FingerprintToolKey(%q) = %q, ingin %q", in, got, want)
		}
	}
}

func TestToolNameMapContext(t *testing.T) {
	map_ := map[string]string{"bash": "Bash"}
	ctx := WithToolNameMap(nil, map_)
	if got := ToolNameMapFromContext(ctx); got["bash"] != "Bash" {
		t.Errorf("peta tidak tersimpan di context: %v", got)
	}
	if got := ToolNameMapFromContext(nil); got != nil {
		t.Errorf("context nil harus menghasilkan peta nil, dapat %v", got)
	}
}
