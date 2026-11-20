package chat

import (
	"context"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy/executor"
)

// TestJailbreakNativeWire exercises the FULL handler path (handleSingleModel)
// for the two providers whose upstream wire is not OpenAI: Gemini-native
// (antigravity/gemini-cli, Format "gemini-native") and Kiro (conversationState
// envelope). Both translate the OpenAI body AFTER the jailbreak prompt was
// prepended into it, so the question is whether the prompt survives that
// translation into the provider's own native field.
//
// The upstream is captured with httptest: the connection's baseUrl override
// points at the test server, which is the same route a self-hosted mirror
// connection takes in production.
func TestJailbreakNativeWire(t *testing.T) {
	executor.RegisterAll() // kiro needs its executor registered, else the default passthrough runs

	const geminiPrompt = "PERSONA_NATIVE_GEMINI_RULE"
	const kiroPrompt = "PERSONA_NATIVE_KIRO_RULE"

	tests := []struct {
		name       string
		provider   string
		model      string
		clientID   string
		connID     string
		connData   map[string]any
		prompt     string
		nativePath []string // path in the captured JSON where the prompt must appear
		absent     string   // control: this must NOT appear when unarmed
	}{
		{
			name:     "gemini",
			provider: "antigravity",
			model:    "gemini-3-flash",
			clientID: "ag/gemini-3-flash",
			connID:   "conn-jbw-gemini",
			connData: map[string]any{
				"apiKey":    "tok-ag",
				"projectId": "proj-test-1",
			},
			prompt:     geminiPrompt,
			nativePath: []string{"request", "systemInstruction", "parts", "0", "text"},
			absent:     `"systemInstruction"`,
		},
		{
			name:     "kiro",
			provider: "kiro",
			model:    "claude-sonnet-4.5",
			clientID: "kr/claude-sonnet-4.5",
			connID:   "conn-jbw-kiro",
			connData: map[string]any{
				"apiKey":     "tok-kiro",
				"authMethod": "social",
			},
			prompt:     kiroPrompt,
			nativePath: []string{"conversationState", "currentMessage", "userInputMessage", "content"},
			absent:     kiroPrompt,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody atomic.Value
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				gotBody.Store(string(raw))
				if tc.provider == "kiro" {
					w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
					w.WriteHeader(http.StatusOK)
					w.Write(providers.EncodeEventFrame(
						map[string]string{":event-type": "assistantResponseEvent"},
						[]byte(`{"content":"ok"}`),
					))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`))
			}))
			defer srv.Close()

			database, cleanup := setupChatTestDB(t)
			defer cleanup()
			if _, err := database.Exec(`DELETE FROM providerConnections WHERE id IN ('conn-1','conn-2')`); err != nil {
				t.Fatalf("clear seeded connections: %v", err)
			}
			connData := tc.connData
			connData["baseUrl"] = srv.URL
			dataJSON, _ := json.Marshal(connData)
			if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES (?, ?, 'apikey', 'Test', 1, 1, ?, '2026-07-18T00:00:00Z','2026-07-18T00:00:00Z')`, tc.connID, tc.provider, string(dataJSON)); err != nil {
				t.Fatalf("seed connection: %v", err)
			}

			repo := db.NewRepo(database)
			h := NewChatHandler(repo)

			preset := &db.JailbreakDoc{ID: "wire", Name: "Wire", Prompt: tc.prompt}
			if err := repo.SaveJailbreakPreset(preset); err != nil {
				t.Fatalf("save preset: %v", err)
			}
			if err := repo.SetJailbreakModelPreset(tc.clientID, "wire", true); err != nil {
				t.Fatalf("arm model: %v", err)
			}

			// Control first: unarmed model must produce a body without the prompt.
			body := mustJSON(map[string]any{
				"model":      tc.clientID,
				"max_tokens": 50,
				"messages":   []any{map[string]any{"role": "system", "content": "be helpful"}, map[string]any{"role": "user", "content": "hi"}},
			})
			modelInfo := &ModelInfo{Provider: tc.provider, Model: tc.model, ConnectionID: tc.connID}
			// Unarmed: use a different client id, same connection.
			unarmedID := tc.clientID + "-unarmed"
			unarmedBody := mustJSON(map[string]any{
				"model":      unarmedID,
				"max_tokens": 50,
				"messages":   []any{map[string]any{"role": "system", "content": "be helpful"}, map[string]any{"role": "user", "content": "hi"}},
			})
			h.handleSingleModel(context.Background(), httptest.NewRecorder(), unarmedBody, modelInfo, false, false)
			unarmed, _ := gotBody.Load().(string)
			if unarmed == "" {
				t.Fatalf("%s control: upstream never called", tc.name)
			}
			if strings.Contains(unarmed, tc.prompt) {
				t.Errorf("%s control leaked prompt into native body: %s", tc.name, unarmed)
			}
			t.Logf("%s CONTROL (unarmed) outbound body: %s", tc.name, unarmed)

			// Armed.
			gotBody.Store("")
			h.handleSingleModel(context.Background(), httptest.NewRecorder(), body, modelInfo, false, false)
			armed, _ := gotBody.Load().(string)
			if armed == "" {
				t.Fatalf("%s armed: upstream never called", tc.name)
			}
			t.Logf("%s ARMED outbound body: %s", tc.name, armed)

			var parsed map[string]any
			if err := json.Unmarshal([]byte(armed), &parsed); err != nil {
				t.Fatalf("%s armed: outbound body is not JSON: %v", tc.name, err)
			}
			cur := any(parsed)
			for _, key := range tc.nativePath {
				switch node := cur.(type) {
				case map[string]any:
					cur = node[key]
				case []any:
					idx, err := strconv.Atoi(key)
					if err != nil || idx < 0 || idx >= len(node) {
						t.Fatalf("%s armed: path %v bad index %q: %s", tc.name, tc.nativePath, key, armed)
					}
					cur = node[idx]
				default:
					t.Fatalf("%s armed: path %v is not a container at %q: %s", tc.name, tc.nativePath, key, armed)
				}
			}
			text, _ := cur.(string)
			if !strings.Contains(text, tc.prompt) {
				t.Errorf("%s GAP: prompt %q not in native field %v (got %q). body: %s", tc.name, tc.prompt, tc.nativePath, text, armed)
			}
		})
	}
}

func mustJSON(v any) []byte {
	out, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return out
}
