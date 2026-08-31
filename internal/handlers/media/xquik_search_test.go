package media

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/chat"
)

func TestXquikSearch_Success(t *testing.T) {
	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	// Mock Xquik upstream server
	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-xquik-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("q") != "golang release" {
			http.Error(w, "unexpected query", http.StatusBadRequest)
			return
		}
		if r.URL.Query().Get("queryType") != "Latest" {
			http.Error(w, "unexpected queryType", http.StatusBadRequest)
			return
		}

		resp := map[string]any{
			"tweets": []map[string]any{
				{
					"id":        "1234567890",
					"text":      "Go 1.26 is released!",
					"createdAt": "2026-08-25T12:00:00Z",
					"author": map[string]any{
						"username": "golang",
						"name":     "Go Programming Language",
					},
					"media": []map[string]any{
						{"mediaUrl": "https://pbs.twimg.com/media/example.jpg", "type": "photo"},
					},
				},
			},
			"has_next_page": true,
			"next_cursor":   "cursor-2",
		}
		w.Header().Set("Content-Type", "application/json")
		json.MarshalWrite(w, resp)
	}))
	defer mockUpstream.Close()

	// Insert test connection pointing to mock upstream
	connData, _ := json.Marshal(map[string]any{
		"apiKey":  "test-xquik-key",
		"baseUrl": mockUpstream.URL,
	})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-xq-1', 'xquik', 'apikey', 'Xquik Dev', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(connData)); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	chatH := chat.NewChatHandler(repo, nil)
	mediaH := NewMediaHandler(repo, nil, chatH)

	body := `{"query":"golang release","max_results":5,"provider_options":{"queryType":"Latest"}}`
	req := httptest.NewRequest("POST", "/v1/search", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	modelInfo := &chat.ModelInfo{
		Provider: "xquik",
		Model:    "xquik",
	}

	err := mediaH.handleXquikSearch(rec, req, []byte(body), modelInfo)
	if err != nil {
		t.Fatalf("handleXquikSearch returned error: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if res["provider"] != "xquik" {
		t.Errorf("expected provider xquik, got %v", res["provider"])
	}

	results, ok := res["results"].([]any)
	if !ok || len(results) != 1 {
		t.Fatalf("expected 1 result, got %v", results)
	}

	first := results[0].(map[string]any)
	if first["title"] != "@golang on X" {
		t.Errorf("expected title '@golang on X', got %v", first["title"])
	}
	if first["url"] != "https://x.com/golang/status/1234567890" {
		t.Errorf("expected url 'https://x.com/golang/status/1234567890', got %v", first["url"])
	}
	if first["display_url"] != "x.com/golang/status/1234567890" {
		t.Errorf("expected display_url 'x.com/golang/status/1234567890', got %v", first["display_url"])
	}

	meta := first["metadata"].(map[string]any)
	if meta["author"] != "@golang" {
		t.Errorf("expected author @golang, got %v", meta["author"])
	}
	if meta["image_url"] != "https://pbs.twimg.com/media/example.jpg" {
		t.Errorf("expected image_url, got %v", meta["image_url"])
	}

	usage := res["usage"].(map[string]any)
	if usage["provider_credits_used"] != float64(1) {
		t.Errorf("expected 1 credit used, got %v", usage["provider_credits_used"])
	}

	pagination := res["pagination"].(map[string]any)
	if pagination["has_more"] != true || pagination["next_cursor"] != "cursor-2" {
		t.Errorf("expected pagination has_more=true, next_cursor=cursor-2, got %v", pagination)
	}
}

func TestXquikSearch_InvalidQueryType(t *testing.T) {
	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	chatH := chat.NewChatHandler(repo, nil)
	mediaH := NewMediaHandler(repo, nil, chatH)

	body := `{"query":"test","provider_options":{"queryType":"Popular"}}`
	req := httptest.NewRequest("POST", "/v1/search", strings.NewReader(body))
	rec := httptest.NewRecorder()

	modelInfo := &chat.ModelInfo{Provider: "xquik", Model: "xquik"}
	err := mediaH.handleXquikSearch(rec, req, []byte(body), modelInfo)
	if err == nil || !strings.Contains(err.Error(), "Xquik queryType must be Latest or Top") {
		t.Fatalf("expected queryType validation error, got %v", err)
	}
}

func TestXquikSearch_AuthorUnavailableFallback(t *testing.T) {
	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"tweets": []map[string]any{
				{
					"id":   "9876543210",
					"text": "Anonymous post text",
				},
			},
			"has_next_page": false,
		}
		w.Header().Set("Content-Type", "application/json")
		json.MarshalWrite(w, resp)
	}))
	defer mockUpstream.Close()

	connData, _ := json.Marshal(map[string]any{
		"apiKey":  "test-xquik-key",
		"baseUrl": mockUpstream.URL,
	})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-xq-2', 'xquik', 'apikey', 'Xquik Dev', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(connData)); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	chatH := chat.NewChatHandler(repo, nil)
	mediaH := NewMediaHandler(repo, nil, chatH)

	body := `{"query":"anonymous post"}`
	req := httptest.NewRequest("POST", "/v1/search", strings.NewReader(body))
	rec := httptest.NewRecorder()

	modelInfo := &chat.ModelInfo{Provider: "xquik", Model: "xquik"}
	err := mediaH.handleXquikSearch(rec, req, []byte(body), modelInfo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var res map[string]any
	json.Unmarshal(rec.Body.Bytes(), &res)
	results := res["results"].([]any)
	first := results[0].(map[string]any)
	if first["title"] != "X post" {
		t.Errorf("expected title 'X post', got %v", first["title"])
	}
	if first["url"] != "https://x.com/i/web/status/9876543210" {
		t.Errorf("expected url 'https://x.com/i/web/status/9876543210', got %v", first["url"])
	}
}
