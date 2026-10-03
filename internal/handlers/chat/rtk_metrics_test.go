package chat

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	stdlog "log"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/translator"
)

func TestRTKCompressionMetricsCalculationAndLog(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()
	h.TokenSaver.SetRTK(true)

	var logBuf bytes.Buffer
	stdlog.SetOutput(&logBuf)
	defer stdlog.SetOutput(os.Stderr)

	var sb strings.Builder
	for i := 0; i < 300; i++ {
		sb.WriteString("unique log line number ")
		sb.WriteString(strconv.Itoa(i))
		sb.WriteString("\n")
	}
	body, err := json.Marshal(map[string]any{
		"messages": []any{
			map[string]any{"role": "tool", "content": sb.String()},
		},
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	got, origTokens, savedTokens, savedPct := h.applyTokenSavers(body, false)
	if string(got) == string(body) {
		t.Fatal("expected RTK to modify body")
	}
	if origTokens <= 0 {
		t.Fatalf("expected origTokens > 0, got %d", origTokens)
	}
	if savedTokens <= 0 {
		t.Fatalf("expected savedTokens > 0, got %d", savedTokens)
	}
	if savedPct <= 0 {
		t.Fatalf("expected savedPct > 0, got %d", savedPct)
	}

	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "[token_saver] RTK compressed tool output") {
		t.Errorf("expected [token_saver] log in output, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "orig_est=") {
		t.Errorf("expected orig_est in log, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "compressed_est=") {
		t.Errorf("expected compressed_est in log, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "saved_est=") {
		t.Errorf("expected saved_est in log, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "saved_pct=") {
		t.Errorf("expected saved_pct in log, got: %s", logOutput)
	}
}

func TestUsageLogging_WithCompressionMetrics(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	var logBuf bytes.Buffer
	stdlog.SetOutput(&logBuf)
	defer stdlog.SetOutput(os.Stderr)

	info := &UsageLogInfo{
		Provider:            "test-provider",
		Model:               "test-model",
		ConnectionID:        "conn-rtk-test",
		OriginalInputTokens: 1000,
		SavedTokens:         400,
		SavedPercent:        40,
	}
	usage := &translator.OpenAIUsage{
		PromptTokens:     600,
		CompletionTokens: 50,
	}

	h.LogUsage(info, usage, 150, []byte(`{"messages":[{"role":"user","content":"test"}]}`), nil)

	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "[usage] logged") {
		t.Errorf("expected [usage] logged line, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "compressed=1000->600 (40% saved)") {
		t.Errorf("expected compressed=1000->600 (40%%%% saved) in log, got: %s", logOutput)
	}

	// Verify persistence in requestDetails
	var dataStr string
	err := database.QueryRow(`
		SELECT data FROM requestDetails
		WHERE connectionId = 'conn-rtk-test'
		ORDER BY rowid DESC LIMIT 1
	`).Scan(&dataStr)
	if err != nil {
		t.Fatalf("failed to query requestDetails: %v", err)
	}

	var detail struct {
		Tokens map[string]int `json:"tokens"`
	}
	if err := json.Unmarshal([]byte(dataStr), &detail); err != nil {
		t.Fatalf("failed to unmarshal requestDetails data: %v", err)
	}

	if detail.Tokens["original_input_tokens"] != 1000 {
		t.Errorf("expected original_input_tokens 1000, got %d", detail.Tokens["original_input_tokens"])
	}
	if detail.Tokens["saved_tokens"] != 400 {
		t.Errorf("expected saved_tokens 400, got %d", detail.Tokens["saved_tokens"])
	}
	if detail.Tokens["saved_percent"] != 40 {
		t.Errorf("expected saved_percent 40, got %d", detail.Tokens["saved_percent"])
	}
}

func TestUsageLogging_WithoutCompressionMetrics(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	var logBuf bytes.Buffer
	stdlog.SetOutput(&logBuf)
	defer stdlog.SetOutput(os.Stderr)

	info := &UsageLogInfo{
		Provider:     "test-provider",
		Model:        "test-model",
		ConnectionID: "conn-no-rtk-test",
	}
	usage := &translator.OpenAIUsage{
		PromptTokens:     100,
		CompletionTokens: 20,
	}

	h.LogUsage(info, usage, 120, []byte(`{"messages":[{"role":"user","content":"test"}]}`), nil)

	logOutput := logBuf.String()
	if strings.Contains(logOutput, "compressed=") {
		t.Errorf("did not expect compressed in log output, got: %s", logOutput)
	}

	var dataStr string
	err := database.QueryRow(`
		SELECT data FROM requestDetails
		WHERE connectionId = 'conn-no-rtk-test'
		ORDER BY rowid DESC LIMIT 1
	`).Scan(&dataStr)
	if err != nil {
		t.Fatalf("failed to query requestDetails: %v", err)
	}

	var detail struct {
		Tokens map[string]int `json:"tokens"`
	}
	if err := json.Unmarshal([]byte(dataStr), &detail); err != nil {
		t.Fatalf("failed to unmarshal requestDetails: %v", err)
	}

	if _, ok := detail.Tokens["original_input_tokens"]; ok {
		t.Errorf("expected original_input_tokens not to be present in tokens map")
	}
	if _, ok := detail.Tokens["saved_tokens"]; ok {
		t.Errorf("expected saved_tokens not to be present in tokens map")
	}
}

func TestTryForwardWithConnection_RTKEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"ok","choices":[{"message":{"content":"done"}}],"usage":{"prompt_tokens":50,"completion_tokens":10}}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	seedConnDB(t, database, "deepseek", "conn-rtk-e2e", "sk-test", srv.URL)

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)
	h.TokenSaver.SetRTK(true)

	var logBuf bytes.Buffer
	stdlog.SetOutput(&logBuf)
	defer stdlog.SetOutput(os.Stderr)

	var sb strings.Builder
	for i := 0; i < 300; i++ {
		sb.WriteString("unique tool log line ")
		sb.WriteString(strconv.Itoa(i))
		sb.WriteString("\n")
	}
	body, err := json.Marshal(map[string]any{
		"model": "deepseek-chat",
		"messages": []any{
			map[string]any{"role": "tool", "content": sb.String()},
		},
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	rec := httptest.NewRecorder()
	err = h.tryForwardWithConnection(forwardRequestParams{
		Ctx:          context.Background(),
		W:            rec,
		Provider:     "deepseek",
		Model:        "deepseek-chat",
		ConnectionID: "conn-rtk-e2e",
		ConnData:     &ConnectionData{APIKey: "sk-test", BaseURL: srv.URL},
		Body:         body,
		IsStream:     false,
		Endpoint:     "/v1/chat/completions",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	// Verify terminal log has both RTK log and usage compressed log
	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "[token_saver] RTK compressed tool output") {
		t.Errorf("expected token_saver RTK compressed log line, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "compressed=") {
		t.Errorf("expected usage log to contain compressed=, got: %s", logOutput)
	}

	// Verify requestDetails persistence
	var dataStr string
	err = database.QueryRow(`
		SELECT data FROM requestDetails
		WHERE connectionId = 'conn-rtk-e2e'
		ORDER BY rowid DESC LIMIT 1
	`).Scan(&dataStr)
	if err != nil {
		t.Fatalf("failed to query requestDetails: %v", err)
	}

	var detail struct {
		Tokens map[string]int `json:"tokens"`
	}
	if err := json.Unmarshal([]byte(dataStr), &detail); err != nil {
		t.Fatalf("failed to unmarshal requestDetails data: %v", err)
	}

	if detail.Tokens["original_input_tokens"] <= 0 {
		t.Errorf("expected original_input_tokens > 0, got %d", detail.Tokens["original_input_tokens"])
	}
	if detail.Tokens["saved_tokens"] <= 0 {
		t.Errorf("expected saved_tokens > 0, got %d", detail.Tokens["saved_tokens"])
	}
	if detail.Tokens["saved_percent"] <= 0 {
		t.Errorf("expected saved_percent > 0, got %d", detail.Tokens["saved_percent"])
	}
}
