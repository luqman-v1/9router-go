package chat

import (
	"bytes"
	"context"
	"database/sql"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/proxy/executor"
	_ "modernc.org/sqlite"
)


// getRealUserDB returns a throwaway in-memory database seeded with the
// operator's real connections and combos, so a live test exercises the same
// routing and credential handling the installed gateway uses.
//
// It is gated on liveUpstreamEnabled, which every caller here needs and no
// offline test does — so the gate lives here rather than at each call site. A
// new live test that forgets to call requireLiveUpstream still skips, because
// it cannot get this far.
func getRealUserDB(t *testing.T) (*db.Repo, func()) {
	requireLiveUpstream(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot get user home dir")
	}
	dbPath := filepath.Join(home, ".9router", "db", "data.sqlite")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Skipf("real db not found at %s", dbPath)
	}

	roDB, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		t.Fatalf("failed to open real db: %v", err)
	}
	defer roDB.Close()

	memDB, cleanup := setupChatTestDB(t)

	// Seed writable DB from real user DB connections
	rows, err := roDB.Query("SELECT id, provider, authType, name, priority, isActive, data, createdAt, updatedAt FROM providerConnections")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var id, provider, authType, data, createdAt, updatedAt string
			var name sql.NullString
			var priority sql.NullInt64
			var isActive int
			if err := rows.Scan(&id, &provider, &authType, &name, &priority, &isActive, &data, &createdAt, &updatedAt); err == nil {
				_, _ = memDB.Exec(
					"INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
					id, provider, authType, name, priority, isActive, data, createdAt, updatedAt,
				)
			}
		}
	}
	// Seed writable DB with real combos
	cRows, err := roDB.Query("SELECT id, name, kind, models, createdAt, updatedAt FROM combos")
	if err == nil {
		defer cRows.Close()
		for cRows.Next() {
			var id, name, models, createdAt, updatedAt string
			var kind sql.NullString
			if err := cRows.Scan(&id, &name, &kind, &models, &createdAt, &updatedAt); err == nil {
				_, _ = memDB.Exec(
					"INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES (?, ?, ?, ?, ?, ?)",
					id, name, kind, models, createdAt, updatedAt,
				)
			}
		}
	}

	repo := db.NewRepo(memDB)
	return repo, cleanup
}

func TestLiveE2E_Antigravity_RealWeeklyQuota(t *testing.T) {
	repo, cleanup := getRealUserDB(t)
	defer cleanup()

	// Find an active antigravity connection with an access token
	conns, err := repo.GetProviderConnections("antigravity", true)
	if err != nil || len(conns) == 0 {
		t.Skip("no active antigravity connections found")
	}

	conn := conns[0]
	var dataMap map[string]any
	if err := json.Unmarshal([]byte(conn.Data), &dataMap); err != nil {
		t.Skipf("cannot parse connection data: %v", err)
	}

	token, _ := dataMap["accessToken"].(string)
	projectID, _ := dataMap["projectId"].(string)
	if token == "" {
		t.Skip("no accessToken in antigravity connection")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	client := &http.Client{Timeout: 15 * time.Second}
	weekly, err := FetchAntigravityWeeklyQuota(ctx, client, token, projectID)
	if err != nil {
		t.Logf("FetchAntigravityWeeklyQuota real upstream returned error: %v (token may need refresh)", err)
		return
	}

	t.Logf("Real upstream weekly quota result: %+v", weekly)
}

func TestLiveE2E_DeepSeek_RealUpstream(t *testing.T) {
	repo, cleanup := getRealUserDB(t)
	defer cleanup()

	conns, err := repo.GetProviderConnections("deepseek", true)
	if err != nil || len(conns) == 0 {
		t.Skip("no active deepseek connection")
	}

	executor.RegisterAll()
	handler := NewChatHandler(repo)

	reqBody := `{
		"model": "deepseek/deepseek-chat",
		"messages": [
			{"role": "user", "content": "respond with PONG only"}
		],
		"max_tokens": 10,
		"stream": false
	}`

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(reqBody)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	t.Logf("DeepSeek response code: %d", rec.Code)
	t.Logf("DeepSeek response body: %s", rec.Body.String())

	requireLiveOK(t, rec.Code, rec.Body.String())
	if !strings.Contains(strings.ToUpper(rec.Body.String()), "PONG") {
		t.Errorf("expected PONG in DeepSeek response, got: %s", rec.Body.String())
	}
}

func TestLiveE2E_Antigravity_RealChat(t *testing.T) {
	repo, cleanup := getRealUserDB(t)
	defer cleanup()

	conns, err := repo.GetProviderConnections("antigravity", true)
	if err != nil || len(conns) == 0 {
		t.Skip("no active antigravity connections")
	}

	executor.RegisterAll()
	handler := NewChatHandler(repo)

	reqBody := `{
		"model": "ag/gemini-2.5-flash",
		"messages": [
			{"role": "user", "content": "Say hello in one word"}
		],
		"max_tokens": 20,
		"stream": false
	}`

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(reqBody)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	t.Logf("Antigravity response code: %d", rec.Code)
	t.Logf("Antigravity response body: %s", rec.Body.String())

	requireLiveOK(t, rec.Code, rec.Body.String())
}

func TestLiveE2E_Antigravity_RealStream(t *testing.T) {
	repo, cleanup := getRealUserDB(t)
	defer cleanup()

	conns, err := repo.GetProviderConnections("antigravity", true)
	if err != nil || len(conns) == 0 {
		t.Skip("no active antigravity connections")
	}

	executor.RegisterAll()
	handler := NewChatHandler(repo)

	reqBody := `{
		"model": "ag/gemini-2.5-flash",
		"messages": [
			{"role": "user", "content": "Count from 1 to 3 separated by commas"}
		],
		"max_tokens": 50,
		"stream": true
	}`

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(reqBody)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	t.Logf("Antigravity stream response code: %d", rec.Code)
	t.Logf("Antigravity stream response body:\n%s", rec.Body.String())

	requireLiveSSE(t, rec.Code, rec.Body.String())
}

func TestLiveE2E_DeepSeek_RealStream(t *testing.T) {
	repo, cleanup := getRealUserDB(t)
	defer cleanup()

	conns, err := repo.GetProviderConnections("deepseek", true)
	if err != nil || len(conns) == 0 {
		t.Skip("no active deepseek connection")
	}

	executor.RegisterAll()
	handler := NewChatHandler(repo)

	reqBody := `{
		"model": "deepseek/deepseek-chat",
		"messages": [
			{"role": "user", "content": "Count from 1 to 3 separated by commas"}
		],
		"max_tokens": 50,
		"stream": true
	}`

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(reqBody)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	t.Logf("DeepSeek stream response code: %d", rec.Code)
	t.Logf("DeepSeek stream response body:\n%s", rec.Body.String())

	requireLiveSSE(t, rec.Code, rec.Body.String())
}

func TestLiveE2E_Cline_SmartCombo(t *testing.T) {
	repo, cleanup := getRealUserDB(t)
	defer cleanup()

	executor.RegisterAll()
	handler := NewChatHandler(repo)

	reqBody := `{
		"model": "smart-combo",
		"messages": [
			{"role": "user", "content": "Say 'hello from smart combo'"}
		],
		"max_tokens": 50,
		"stream": true
	}`

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(reqBody)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	t.Logf("SmartCombo stream response code: %d", rec.Code)
	t.Logf("SmartCombo stream response body:\n%s", rec.Body.String())

	requireLiveSSE(t, rec.Code, rec.Body.String())
}

func TestLiveE2E_Antigravity_MultiToolCall(t *testing.T) {
	repo, cleanup := getRealUserDB(t)
	defer cleanup()

	conns, err := repo.GetProviderConnections("antigravity", true)
	if err != nil || len(conns) == 0 {
		t.Skip("no active antigravity connections")
	}

	executor.RegisterAll()
	handler := NewChatHandler(repo)

	// Step 1: Send request expecting parallel tool calls
	reqBody := `{
		"model": "ag/gemini-2.5-flash",
		"messages": [
			{"role": "user", "content": "What is the weather in Tokyo and Paris? Call the get_weather tool for BOTH cities separately in parallel."}
		],
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "get_weather",
					"description": "Get current temperature for a city",
					"parameters": {
						"type": "object",
						"properties": {
							"city": {"type": "string"}
						},
						"required": ["city"]
					}
				}
			}
		],
		"tool_choice": "auto",
		"max_tokens": 1000,
		"stream": false
	}`

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(reqBody)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	t.Logf("Antigravity MultiToolCall Response Code: %d", rec.Code)
	t.Logf("Antigravity MultiToolCall Response Body: %s", rec.Body.String())

	requireLiveOK(t, rec.Code, rec.Body.String())

	var resp struct {
		Choices []struct {
			Message struct {
				Role      string `json:"role"`
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if len(resp.Choices) == 0 {
		t.Fatal("expected at least one choice")
	}

	toolCalls := resp.Choices[0].Message.ToolCalls
	t.Logf("Antigravity received %d tool calls: %+v", len(toolCalls), toolCalls)
	if len(toolCalls) < 2 {
		t.Fatalf("expected multiple parallel tool calls (at least 2), got %d", len(toolCalls))
	}

	// Step 2: Feed back both tool results in multi-turn to ensure second turn succeeds
	multiTurnBody := map[string]any{
		"model": "ag/gemini-2.5-flash",
		"messages": []any{
			map[string]any{"role": "user", "content": "What is the weather in Tokyo and Paris? Call the get_weather tool for BOTH cities separately in parallel."},
			resp.Choices[0].Message,
			map[string]any{"role": "tool", "tool_call_id": toolCalls[0].ID, "content": `{"temperature": "22C"}`},
			map[string]any{"role": "tool", "tool_call_id": toolCalls[1].ID, "content": `{"temperature": "18C"}`},
		},
		"stream": false,
	}
	turnJSON, _ := json.Marshal(multiTurnBody)
	req2 := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(turnJSON))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()

	handler.HandleChatCompletions(rec2, req2)

	t.Logf("Antigravity MultiTurn Response Code: %d", rec2.Code)
	t.Logf("Antigravity MultiTurn Response Body: %s", rec2.Body.String())

	if rec2.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200 on multi-turn tool response, got %d: %s", rec2.Code, rec2.Body.String())
	}
}

func TestLiveE2E_DeepSeek_MultiToolCall(t *testing.T) {
	repo, cleanup := getRealUserDB(t)
	defer cleanup()

	conns, err := repo.GetProviderConnections("deepseek", true)
	if err != nil || len(conns) == 0 {
		t.Skip("no active deepseek connection")
	}

	executor.RegisterAll()
	handler := NewChatHandler(repo)

	reqBody := `{
		"model": "deepseek/deepseek-chat",
		"messages": [
			{"role": "user", "content": "What is the weather in Tokyo and Paris? Call the get_weather tool for BOTH cities in parallel."}
		],
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "get_weather",
					"description": "Get current temperature for a city",
					"parameters": {
						"type": "object",
						"properties": {
							"city": {"type": "string"}
						},
						"required": ["city"]
					}
				}
			}
		],
		"tool_choice": "auto",
		"max_tokens": 1000,
		"stream": false
	}`

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(reqBody)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	t.Logf("DeepSeek MultiToolCall Response Code: %d", rec.Code)
	t.Logf("DeepSeek MultiToolCall Response Body: %s", rec.Body.String())

	requireLiveOK(t, rec.Code, rec.Body.String())

	var resp struct {
		Choices []struct {
			Message struct {
				Role      string `json:"role"`
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if len(resp.Choices) == 0 {
		t.Fatal("expected at least one choice")
	}

	toolCalls := resp.Choices[0].Message.ToolCalls
	t.Logf("DeepSeek received %d tool calls: %+v", len(toolCalls), toolCalls)
	if len(toolCalls) < 2 {
		t.Fatalf("expected multiple tool calls, got %d", len(toolCalls))
	}
}

func TestLiveE2E_Gemini38_FlashHigh_MultiToolCall(t *testing.T) {
	repo, cleanup := getRealUserDB(t)
	defer cleanup()

	conns, err := repo.GetProviderConnections("antigravity", true)
	if err != nil || len(conns) == 0 {
		t.Skip("no active antigravity connections")
	}

	executor.RegisterAll()
	handler := NewChatHandler(repo)

	// Step 1: Send request expecting parallel tool calls on gemini-3.8-flash-high
	reqBody := `{
		"model": "ag/gemini-3.8-flash-high",
		"messages": [
			{"role": "user", "content": "What is the weather in Tokyo and Paris right now? Call get_weather for BOTH cities separately in parallel."}
		],
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "get_weather",
					"description": "Get current weather temperature for a given city",
					"parameters": {
						"type": "object",
						"properties": {
							"city": {"type": "string"}
						},
						"required": ["city"]
					}
				}
			}
		],
		"tool_choice": "auto",
		"max_tokens": 1000,
		"stream": false
	}`

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(reqBody)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	t.Logf("Gemini 3.8 Flash High MultiToolCall Response Code: %d", rec.Code)
	t.Logf("Gemini 3.8 Flash High MultiToolCall Response Body: %s", rec.Body.String())

	requireLiveOK(t, rec.Code, rec.Body.String())

	var resp struct {
		Choices []struct {
			Message struct {
				Role      string `json:"role"`
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if len(resp.Choices) == 0 {
		t.Fatal("expected at least one choice")
	}

	toolCalls := resp.Choices[0].Message.ToolCalls
	t.Logf("Gemini 3.8 Flash High received %d tool calls: %+v", len(toolCalls), toolCalls)
	if len(toolCalls) < 2 {
		t.Fatalf("expected multiple parallel tool calls (at least 2), got %d", len(toolCalls))
	}

	// Step 2: Feed back both tool results in multi-turn with Gemini 3.8 Flash High
	multiTurnBody := map[string]any{
		"model": "ag/gemini-3.8-flash-high",
		"messages": []any{
			map[string]any{"role": "user", "content": "What is the weather in Tokyo and Paris right now? Call get_weather for BOTH cities separately in parallel."},
			resp.Choices[0].Message,
			map[string]any{"role": "tool", "tool_call_id": toolCalls[0].ID, "content": `{"temperature": "24C"}`},
			map[string]any{"role": "tool", "tool_call_id": toolCalls[1].ID, "content": `{"temperature": "19C"}`},
		},
		"stream": false,
	}
	turnJSON, _ := json.Marshal(multiTurnBody)
	req2 := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(turnJSON))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()

	handler.HandleChatCompletions(rec2, req2)

	t.Logf("Gemini 3.8 Flash High MultiTurn Response Code: %d", rec2.Code)
	t.Logf("Gemini 3.8 Flash High MultiTurn Response Body: %s", rec2.Body.String())

	if rec2.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200 on multi-turn tool response with Gemini 3.8, got %d: %s", rec2.Code, rec2.Body.String())
	}
}

func TestLiveE2E_Gemini38_FlashHigh_RealStream(t *testing.T) {
	repo, cleanup := getRealUserDB(t)
	defer cleanup()

	conns, err := repo.GetProviderConnections("antigravity", true)
	if err != nil || len(conns) == 0 {
		t.Skip("no active antigravity connections")
	}

	executor.RegisterAll()
	handler := NewChatHandler(repo)

	reqBody := `{
		"model": "ag/gemini-3.8-flash-high",
		"messages": [
			{"role": "user", "content": "Count from 1 to 3 separated by commas"}
		],
		"max_tokens": 50,
		"stream": true
	}`

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(reqBody)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	t.Logf("Gemini 3.8 stream response code: %d", rec.Code)
	t.Logf("Gemini 3.8 stream response body:\n%s", rec.Body.String())

	requireLiveSSE(t, rec.Code, rec.Body.String())
}

func TestLiveE2E_SpaceBunny_Free(t *testing.T) {
	repo, cleanup := getRealUserDB(t)
	defer cleanup()

	handler := NewChatHandler(repo)

	reqBody := `{
		"model": "oc/space-bunny-free",
		"messages": [
			{"role": "user", "content": "hi"}
		],
		"stream": false
	}`

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(reqBody)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	t.Logf("Space bunny response code: %d", rec.Code)
	t.Logf("Space bunny response body:\n%s", rec.Body.String())

	requireLiveOK(t, rec.Code, rec.Body.String())
}

func TestLiveE2E_HandleTestModel_SpaceBunny(t *testing.T) {
	repo, cleanup := getRealUserDB(t)
	defer cleanup()

	handler := NewChatHandler(repo)

	for _, modelName := range []string{"oc/space-bunny-free"} {
		body, _ := json.Marshal(map[string]string{"model": modelName})
		req := httptest.NewRequest("POST", "/api/models/test", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		handler.HandleTestModel(rec, req)

		t.Logf("HandleTestModel %s -> %d: %s", modelName, rec.Code, rec.Body.String())
		requireLiveOK(t, rec.Code, rec.Body.String())
		if !strings.Contains(rec.Body.String(), `"ok":true`) {
			t.Fatalf("expected ok:true for %s, got: %s", modelName, rec.Body.String())
		}
	}
}
