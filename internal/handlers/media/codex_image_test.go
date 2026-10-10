package media

import (
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

// codexImageUpstream is a fake Responses endpoint that records the request it
// received and replies with a canned event stream.
type codexImageUpstream struct {
	server *httptest.Server
	path   string
	body   map[string]any
	calls  int
}

func newCodexImageUpstream(t *testing.T, events string, status int) *codexImageUpstream {
	t.Helper()
	up := &codexImageUpstream{}
	up.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up.calls++
		up.path = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &up.body)
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"message":"no entitlement"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(events))
	}))
	t.Cleanup(up.server.Close)
	return up
}

// codexImageStream builds the event stream the Responses endpoint emits for a
// generated image, with the usage object supplied by the caller.
func codexImageStream(imageB64, usage string) string {
	return "event: response.created\n" +
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
		"event: response.image_generation_call.partial_image\n" +
		"data: {\"partial_image_b64\":\"cG9ydGlhbA==\",\"partial_image_index\":0}\n\n" +
		"event: response.output_item.done\n" +
		fmt.Sprintf("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"image_generation_call\",\"result\":\"%s\"}}\n\n", imageB64) +
		"event: response.completed\n" +
		fmt.Sprintf("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"usage\":%s}}\n\n", usage)
}

func overrideCodexProvider(t *testing.T, cfg providers.ProviderConfig) {
	t.Helper()
	orig, existed := providers.KnownProviders["codex"]
	providers.KnownProviders["codex"] = cfg
	t.Cleanup(func() {
		if existed {
			providers.KnownProviders["codex"] = orig
		} else {
			delete(providers.KnownProviders, "codex")
		}
	})
}

// codexUsageHistorySchema is the subset of usageHistory the image lane writes
// to. The shared media test database creates the connection tables by hand and
// never bootstraps the usage schema, so the row this lane is judged on would
// otherwise have nowhere to land.
const codexUsageHistorySchema = `CREATE TABLE usageHistory (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	timestamp TEXT NOT NULL,
	provider TEXT,
	model TEXT,
	connectionId TEXT,
	apiKey TEXT,
	endpoint TEXT,
	promptTokens INTEGER DEFAULT 0,
	completionTokens INTEGER DEFAULT 0,
	cost REAL DEFAULT 0,
	status TEXT,
	tokens TEXT,
	meta TEXT
)`

func newCodexImageHandler(t *testing.T) (*MediaHandler, *db.Repo) {
	t.Helper()
	database, cleanup := setupMultimodalTestDB(t)
	t.Cleanup(cleanup)
	if _, err := database.Exec(codexUsageHistorySchema); err != nil {
		t.Fatalf("create usageHistory: %v", err)
	}

	connData, err := json.Marshal(map[string]any{"apiKey": "sk-codex-TEST"})
	if err != nil {
		t.Fatalf("marshal conn data: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-codex-img', 'codex', 'apikey', 'Codex Image Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(connData)); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	repo := db.NewRepo(database)
	return newTestMediaHandler(repo), repo
}

// A Codex image request must reach the Responses endpoint the provider names,
// come back as a base64 image, and land in usageHistory with the exact counters
// the upstream reported — that row is what the dashboard bills from, and before
// this lane every Codex image turn was recorded with zeros.
func TestHandleImages_Codex_RecordsExactUsage(t *testing.T) {
	const usage = `{"input_tokens":120,"output_tokens":45,"total_tokens":165,"input_tokens_details":{"cached_tokens":30},"output_tokens_details":{"reasoning_tokens":12}}`
	up := newCodexImageUpstream(t, codexImageStream("aW1hZ2U=", usage), http.StatusOK)
	overrideCodexProvider(t, providers.ProviderConfig{
		BaseURL:    up.server.URL + "/backend-api/codex/responses",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	})
	handler, repo := newCodexImageHandler(t)

	req := httptest.NewRequest("POST", "/v1/images/generations", strings.NewReader(`{"model":"codex/gpt-5.5-image","prompt":"a red fox"}`))
	rec := httptest.NewRecorder()

	handler.HandleImages(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if up.path != "/backend-api/codex/responses" {
		t.Errorf("expected the Responses endpoint, got %q", up.path)
	}
	if up.body["model"] != "gpt-5.5" {
		t.Errorf("the -image suffix must be stripped for the wire, got %v", up.body["model"])
	}
	var served map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &served); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	data, _ := served["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("expected one image, got %v", served["data"])
	}
	if got := data[0].(map[string]any)["b64_json"]; got != "aW1hZ2U=" {
		t.Errorf("b64_json = %v, want the generated image", got)
	}

	row := codexUsageRow(t, repo, "gpt-5.5")
	if row.PromptTokens != 120 || row.CompletionTokens != 45 {
		t.Errorf("usage row = %+v, want prompt=120 completion=45", row)
	}
	if !strings.Contains(row.Tokens, `"cached_tokens":30`) {
		t.Errorf("cached tokens must survive into the stored row, got %s", row.Tokens)
	}
	if !strings.Contains(row.Tokens, `"reasoning_tokens":12`) {
		t.Errorf("reasoning tokens must survive into the stored row, got %s", row.Tokens)
	}
}

// A turn the upstream never completed must not produce a zero row: a zero row
// is indistinguishable from a turn that was never billed.
func TestHandleImages_Codex_NoImageIsAnErrorAndRecordsNothing(t *testing.T) {
	stream := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":9,\"output_tokens\":1}}}\n\n"
	up := newCodexImageUpstream(t, stream, http.StatusOK)
	overrideCodexProvider(t, providers.ProviderConfig{
		BaseURL:    up.server.URL + "/backend-api/codex/responses",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	})
	handler, repo := newCodexImageHandler(t)

	req := httptest.NewRequest("POST", "/v1/images/generations", strings.NewReader(`{"model":"codex/gpt-5.5-image","prompt":"a red fox"}`))
	rec := httptest.NewRecorder()

	handler.HandleImages(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("a stream with no image must not answer 200: %s", rec.Body.String())
	}
	if got := codexUsageRowOrNil(repo, "gpt-5.5"); got != nil {
		t.Errorf("no usage row may be written when no image came back, got %+v", got)
	}
}

func codexUsageRow(t *testing.T, repo *db.Repo, model string) *db.UsageHistoryRow {
	t.Helper()
	row := codexUsageRowOrNil(repo, model)
	if row == nil {
		t.Fatalf("expected a usage row for %s, found none", model)
	}
	return row
}

func codexUsageRowOrNil(repo *db.Repo, model string) *db.UsageHistoryRow {
	rows, err := repo.GetRecentUsageHistory(50)
	if err != nil {
		return nil
	}
	for i := range rows {
		if rows[i].Model == model {
			return &rows[i]
		}
	}
	return nil
}

func TestCodexImageModels(t *testing.T) {
	tests := []struct {
		name          string
		model         string
		wantClean     string
		wantResponses string
		wantToolModel string
	}{
		{
			name:          "tool model answers with the main model",
			model:         "gpt-image-2.5",
			wantClean:     "gpt-image-2.5",
			wantResponses: codexImagesMainModel,
			wantToolModel: "gpt-image-2.5",
		},
		{
			name:          "suffixed model strips the suffix and carries no tool",
			model:         "gpt-5.5-image",
			wantClean:     "gpt-5.5",
			wantResponses: "gpt-5.5",
		},
		{
			name:          "a bare id is used as-is",
			model:         "gpt-5.5",
			wantClean:     "gpt-5.5",
			wantResponses: "gpt-5.5",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clean, responses, tool := codexImageModels(tt.model)
			if clean != tt.wantClean || responses != tt.wantResponses || tool != tt.wantToolModel {
				t.Errorf("got (%q, %q, %q), want (%q, %q, %q)", clean, responses, tool, tt.wantClean, tt.wantResponses, tt.wantToolModel)
			}
		})
	}
}

func TestCodexImageDataURL(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "blank is dropped", input: "  ", want: ""},
		{name: "data url passes through", input: "data:image/webp;base64,AAA", want: "data:image/webp;base64,AAA"},
		{name: "https url passes through", input: "https://cdn.example/a.png", want: "https://cdn.example/a.png"},
		{name: "http url passes through", input: "http://cdn.example/a.png", want: "http://cdn.example/a.png"},
		{name: "bare base64 is wrapped", input: "AAA", want: "data:image/png;base64,AAA"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := codexImageDataURL(tt.input); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// The usage counters decide what the dashboard bills, so a malformed or
// negative figure must discard the whole object rather than record half of it.
func TestNormalizeCodexImageUsage(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantNil    bool
		wantPrompt int
		wantTotal  int
		wantCached *int
		wantReason *int
	}{
		{
			name:       "responses style counters",
			input:      `{"input_tokens":10,"output_tokens":5}`,
			wantPrompt: 10,
			wantTotal:  15,
		},
		{
			name:       "chat style counters",
			input:      `{"prompt_tokens":7,"completion_tokens":3}`,
			wantPrompt: 7,
			wantTotal:  10,
		},
		{
			name:       "explicit total is honoured",
			input:      `{"input_tokens":10,"output_tokens":5,"total_tokens":99}`,
			wantPrompt: 10,
			wantTotal:  99,
		},
		{
			name:       "cached and reasoning details are carried",
			input:      `{"input_tokens":10,"output_tokens":5,"input_tokens_details":{"cached_tokens":4},"output_tokens_details":{"reasoning_tokens":2}}`,
			wantPrompt: 10,
			wantTotal:  15,
			wantCached: new(4),
			wantReason: new(2),
		},
		{name: "missing input", input: `{"output_tokens":5}`, wantNil: true},
		{name: "missing output", input: `{"input_tokens":5}`, wantNil: true},
		{name: "negative input", input: `{"input_tokens":-1,"output_tokens":5}`, wantNil: true},
		{name: "fractional output", input: `{"input_tokens":1,"output_tokens":1.5}`, wantNil: true},
		{name: "not an object", input: `"nope"`, wantNil: true},
		{
			name:       "unusable cached detail is dropped, not invented",
			input:      `{"input_tokens":10,"output_tokens":5,"input_tokens_details":{"cached_tokens":-2}}`,
			wantPrompt: 10,
			wantTotal:  15,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var raw any
			if err := json.Unmarshal([]byte(tt.input), &raw); err != nil {
				t.Fatalf("parse fixture: %v", err)
			}
			got := normalizeCodexImageUsage(raw)
			if tt.wantNil {
				if got != nil {
					t.Fatalf("expected the usage to be discarded, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected a usage object, got nil")
			}
			if got.PromptTokens != tt.wantPrompt || got.TotalTokens != tt.wantTotal {
				t.Errorf("usage = %+v, want prompt=%d total=%d", got, tt.wantPrompt, tt.wantTotal)
			}
			assertOptionalToken(t, "cached", got.CachedTokens, tt.wantCached)
			assertOptionalToken(t, "reasoning", got.ReasoningTokens, tt.wantReason)
		})
	}
}

func assertOptionalToken(t *testing.T, name string, got, want *int) {
	t.Helper()
	switch {
	case want == nil && got != nil:
		t.Errorf("%s tokens = %d, want them absent", name, *got)
	case want != nil && got == nil:
		t.Errorf("%s tokens absent, want %d", name, *want)
	case want != nil && *got != *want:
		t.Errorf("%s tokens = %d, want %d", name, *got, *want)
	}
}

func TestParseCodexImageStream(t *testing.T) {
	t.Run("usage nested under response", func(t *testing.T) {
		stream := codexImageStream("aW1hZ2U=", `{"input_tokens":8,"output_tokens":2}`)
		image, usage := parseCodexImageStream(strings.NewReader(stream))
		if image != "aW1hZ2U=" {
			t.Errorf("image = %q, want aW1hZ2U=", image)
		}
		if usage == nil || usage.PromptTokens != 8 || usage.TotalTokens != 10 {
			t.Errorf("usage = %+v, want prompt=8 total=10", usage)
		}
	})

	t.Run("usage at the top level and response.done", func(t *testing.T) {
		stream := "event: response.output_item.done\n" +
			`data: {"item":{"type":"image_generation_call","result":"aW1n"}}` + "\n\n" +
			"event: response.done\n" +
			`data: {"usage":{"input_tokens":3,"output_tokens":4,"total_tokens":7}}` + "\n\n"
		image, usage := parseCodexImageStream(strings.NewReader(stream))
		if image != "aW1n" {
			t.Errorf("image = %q, want aW1n", image)
		}
		if usage == nil || usage.PromptTokens != 3 || usage.TotalTokens != 7 {
			t.Errorf("usage = %+v, want prompt=3 total=7", usage)
		}
	})

	t.Run("no usage reported", func(t *testing.T) {
		stream := "event: response.output_item.done\n" +
			`data: {"item":{"type":"image_generation_call","result":"aW1n"}}` + "\n\n"
		_, usage := parseCodexImageStream(strings.NewReader(stream))
		if usage != nil {
			t.Errorf("usage = %+v, want none", usage)
		}
	})

	t.Run("no image reported", func(t *testing.T) {
		stream := "event: response.completed\n" +
			`data: {"response":{"usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n"
		image, _ := parseCodexImageStream(strings.NewReader(stream))
		if image != "" {
			t.Errorf("image = %q, want none so the caller errors", image)
		}
	})

	t.Run("a later image wins over an earlier one", func(t *testing.T) {
		stream := "event: response.output_item.done\n" +
			`data: {"item":{"type":"image_generation_call","result":"first"}}` + "\n\n" +
			"event: response.output_item.done\n" +
			`data: {"item":{"type":"image_generation_call","result":"second"}}` + "\n\n"
		image, _ := parseCodexImageStream(strings.NewReader(stream))
		if image != "second" {
			t.Errorf("image = %q, want the final one", image)
		}
	})
}

// The tool id has to ride the tool definition, and the caller has to be pinned
// to it, or the Responses call answers with text instead of an image.
func TestCodexImageBody_ToolModelPinsTheImageTool(t *testing.T) {
	req := &codexImageRequest{Prompt: "a red fox", Size: "1024x1024", Quality: "high"}
	_, responsesModel, toolModel := codexImageModels("gpt-image-2.5")
	body, err := codexImageBody(req, responsesModel, toolModel)
	if err != nil {
		t.Fatalf("build body: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	tools, _ := payload["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("expected one tool, got %v", payload["tools"])
	}
	tool := tools[0].(map[string]any)
	if tool["type"] != "image_generation" || tool["model"] != "gpt-image-2.5" || tool["action"] != "generate" {
		t.Errorf("tool = %v, want the image_generation tool pinned to the gpt-image model", tool)
	}
	if tool["size"] != "1024x1024" || tool["quality"] != "high" {
		t.Errorf("size/quality must ride the tool: %v", tool)
	}
	choice, _ := payload["tool_choice"].(map[string]any)
	if choice["type"] != "image_generation" {
		t.Errorf("tool_choice = %v, want the image_generation tool pinned", payload["tool_choice"])
	}
	if payload["stream"] != true || payload["store"] != false {
		t.Errorf("a Responses image call must stream and not store: %v", payload)
	}
}

// A reference image turns a generate into an edit, and each reference is
// bracketed by a marker so the model can tell them apart.
func TestCodexImageBody_ReferencesBecomeEditWithMarkers(t *testing.T) {
	req := &codexImageRequest{
		Prompt: "make it blue",
		Images: []string{"AAA", "https://cdn.example/b.png"},
	}
	_, responsesModel, toolModel := codexImageModels("gpt-image-2.5")
	body, err := codexImageBody(req, responsesModel, toolModel)
	if err != nil {
		t.Fatalf("build body: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	tool := payload["tools"].([]any)[0].(map[string]any)
	if tool["action"] != "edit" {
		t.Errorf("action = %v, want edit when a reference is present", tool["action"])
	}
	input := payload["input"].([]any)[0].(map[string]any)
	content := input["content"].([]any)
	if len(content) != 7 {
		t.Fatalf("expected 2 bracketed references plus the prompt, got %d parts", len(content))
	}
	if text := content[0].(map[string]any)["text"]; text != "<image name=image1>" {
		t.Errorf("first marker = %v", text)
	}
	second := content[4].(map[string]any)
	if second["image_url"] != "https://cdn.example/b.png" {
		t.Errorf("a remote reference must pass through unchanged, got %v", second["image_url"])
	}
	if detail := second["detail"]; detail != codexImageRefDetail {
		t.Errorf("detail = %v, want %q", detail, codexImageRefDetail)
	}
}

// Without a tool model the request must not pin a tool, and must not send the
// reasoning object the tool path needs.
func TestCodexImageBody_SuffixedModelCarriesNoTool(t *testing.T) {
	req := &codexImageRequest{Prompt: "a red fox"}
	_, responsesModel, toolModel := codexImageModels("gpt-5.5-image")
	body, err := codexImageBody(req, responsesModel, toolModel)
	if err != nil {
		t.Fatalf("build body: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	if payload["tool_choice"] != "auto" {
		t.Errorf("tool_choice = %v, want auto", payload["tool_choice"])
	}
	if _, present := payload["reasoning"]; present {
		t.Errorf("no tool model means no reasoning object: %v", payload)
	}
}