package chat

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

// DeepSeek answers 400 "Tool names must be unique" for a request that declares
// the same tool twice, which kills the whole request (upstream 7f5bd155,
// open-sse/utils/toolDeduper.js). The collapse runs on the final body, so this
// drives the real dispatch path: a duplicate pair would 400 the fake DeepSeek
// upstream unless it was collapsed, and a same-name pair aimed at another
// provider must still arrive intact.

func toolNameServer(t *testing.T, sawNames *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		names := declaredToolNames(t, raw)
		*sawNames = names
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func declaredToolNames(t *testing.T, raw []byte) []string {
	t.Helper()
	var req struct {
		Tools []struct {
			Name     string `json:"name"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Errorf("fake upstream got invalid JSON: %v", err)
		return nil
	}
	names := make([]string, 0, len(req.Tools))
	for _, tool := range req.Tools {
		name := tool.Name
		if name == "" {
			name = tool.Function.Name
		}
		names = append(names, name)
	}
	return names
}

func TestHandleAccountFallback_DeepSeekDuplicateToolsCollapsed(t *testing.T) {
	var names []string
	srv := toolNameServer(t, &names)

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	clearSeededConns(t, database)
	seedConnDB(t, database, "deepseek", "conn-ds-dup", "sk-dup", srv.URL)

	h := NewChatHandler(db.NewRepo(database))
	body := []byte(`{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}],"tools":[
		{"type":"function","function":{"name":"Bash","description":"first"}},
		{"type":"function","function":{"name":"Bash","description":"duplicate"}}
	]}`)

	rec := httptest.NewRecorder()
	if err := h.handleAccountFallback(t.Context(), rec, "deepseek", "deepseek-chat", "", body, false, false, "/v1/chat/completions"); err != nil {
		t.Fatalf("forward: %v", err)
	}

	if got := strings.Join(names, ","); got != "Bash" {
		t.Errorf("upstream received tools [%s], want the single declaration Bash", got)
	}
}

// TestHandleAccountFallback_NonDeepSeekDuplicateToolsUntouched is the scope half
// of the same contract: GLM and friends accept duplicate names, so the collapse
// must not run for them.
func TestHandleAccountFallback_NonDeepSeekDuplicateToolsUntouched(t *testing.T) {
	var names []string
	srv := toolNameServer(t, &names)

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	clearSeededConns(t, database)
	seedConnDB(t, database, "groq", "conn-groq-dup", "gsk-dup", srv.URL)

	h := NewChatHandler(db.NewRepo(database))
	body := []byte(`{"model":"llama-3.3-70b-versatile","messages":[{"role":"user","content":"hi"}],"tools":[
		{"type":"function","function":{"name":"Bash","description":"first"}},
		{"type":"function","function":{"name":"Bash","description":"duplicate"}}
	]}`)

	rec := httptest.NewRecorder()
	if err := h.handleAccountFallback(t.Context(), rec, "groq", "llama-3.3-70b-versatile", "", body, false, false, "/v1/chat/completions"); err != nil {
		t.Fatalf("forward: %v", err)
	}

	if got := strings.Join(names, ","); got != "Bash,Bash" {
		t.Errorf("upstream received tools [%s], want both declarations for a non-DeepSeek model", got)
	}
}
