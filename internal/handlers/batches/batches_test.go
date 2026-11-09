package batches

import (
	"bytes"
	json "encoding/json/v2"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-chi/chi/v5"

	"9router/proxy/internal/db"
)

func setupTestDB(t *testing.T) (*db.Repo, func()) {
	t.Helper()
	tmpFile, err := os.CreateTemp("", "test_batches_handlers_*.sqlite")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	tmpFile.Close()

	database, err := db.OpenDatabase(tmpFile.Name())
	if err != nil {
		os.Remove(tmpFile.Name())
		t.Fatalf("OpenDatabase failed: %v", err)
	}

	cleanup := func() {
		database.Close()
		os.Remove(tmpFile.Name())
	}

	schema := []string{
		`CREATE TABLE files (
			id TEXT PRIMARY KEY,
			filename TEXT NOT NULL,
			bytes INTEGER NOT NULL,
			purpose TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			content BLOB NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_files_purpose ON files(purpose);`,
		`CREATE INDEX IF NOT EXISTS idx_files_created ON files(created_at DESC);`,
		`CREATE TABLE batches (
			id TEXT PRIMARY KEY,
			input_file_id TEXT NOT NULL,
			endpoint TEXT NOT NULL,
			completion_window TEXT NOT NULL,
			status TEXT NOT NULL,
			output_file_id TEXT,
			error_file_id TEXT,
			created_at INTEGER NOT NULL,
			completed_at INTEGER,
			cancelled_at INTEGER,
			metadata TEXT
		);`,
		`CREATE INDEX IF NOT EXISTS idx_batches_status ON batches(status);`,
		`CREATE INDEX IF NOT EXISTS idx_batches_created ON batches(created_at DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_batches_input_file ON batches(input_file_id);`,
	}

	for _, query := range schema {
		if _, err := database.Exec(query); err != nil {
			cleanup()
			t.Fatalf("failed to create table: %v", err)
		}
	}

	return db.NewRepo(database), cleanup
}

func setupTestRouter(repo *db.Repo) chi.Router {
	r := chi.NewRouter()
	batchHandler := NewBatchHandler(repo)
	fileHandler := NewFileHandler(repo)

	r.Post("/v1/batches", batchHandler.HandleCreateBatch)
	r.Post("/batches", batchHandler.HandleCreateBatch)
	r.Get("/v1/batches", batchHandler.HandleListBatches)
	r.Get("/batches", batchHandler.HandleListBatches)
	r.Get("/v1/batches/{batch_id}", batchHandler.HandleGetBatch)
	r.Get("/batches/{batch_id}", batchHandler.HandleGetBatch)
	r.Post("/v1/batches/{batch_id}/cancel", batchHandler.HandleCancelBatch)
	r.Post("/batches/{batch_id}/cancel", batchHandler.HandleCancelBatch)

	r.Post("/v1/files", fileHandler.HandleCreateFile)
	r.Post("/files", fileHandler.HandleCreateFile)
	r.Get("/v1/files", fileHandler.HandleListFiles)
	r.Get("/files", fileHandler.HandleListFiles)
	r.Get("/v1/files/{file_id}", fileHandler.HandleGetFile)
	r.Get("/files/{file_id}", fileHandler.HandleGetFile)
	r.Get("/v1/files/{file_id}/content", fileHandler.HandleGetFileContent)
	r.Get("/files/{file_id}/content", fileHandler.HandleGetFileContent)
	r.Delete("/v1/files/{file_id}", fileHandler.HandleDeleteFile)
	r.Delete("/files/{file_id}", fileHandler.HandleDeleteFile)

	return r
}

func TestHandleCreateFile(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	req, err := createMultipartRequest("/v1/files", map[string]string{"purpose": "batch"}, "test.txt", []byte("hello world"))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp["id"] == nil {
		t.Fatal("expected file id in response")
	}
	if resp["object"] != "file" {
		t.Errorf("expected object=file, got %v", resp["object"])
	}
	if resp["filename"] != "test.txt" {
		t.Errorf("expected filename=test.txt, got %v", resp["filename"])
	}
	if resp["purpose"] != "batch" {
		t.Errorf("expected purpose=batch, got %v", resp["purpose"])
	}
	if resp["bytes"] != float64(11) {
		t.Errorf("expected bytes=11, got %v", resp["bytes"])
	}

	fileID := resp["id"].(string)

	// Verify file can be retrieved
	req = httptest.NewRequest(http.MethodGet, "/v1/files/"+fileID, nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var getResp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if getResp["id"] != fileID {
		t.Errorf("expected id=%s, got %v", fileID, getResp["id"])
	}

	// Verify file content
	req = httptest.NewRequest(http.MethodGet, "/v1/files/"+fileID+"/content", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "hello world" {
		t.Errorf("expected content 'hello world', got %q", rec.Body.String())
	}
}

func TestHandleListFiles(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	// Create two files
	for i := 0; i < 2; i++ {
		req, err := createMultipartRequest("/v1/files", map[string]string{"purpose": "batch"}, "test"+string(rune('0'+i))+".txt", []byte("content"+string(rune('0'+i))))
		if err != nil {
			t.Fatalf("create request: %v", err)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusCreated {
			t.Fatalf("create file %d: expected 201, got %d", i, rec.Code)
		}
	}

	// List files
	req := httptest.NewRequest(http.MethodGet, "/v1/files", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	data, ok := resp["data"].([]any)
	if !ok {
		t.Fatalf("expected data array, got %T", resp["data"])
	}
	if len(data) != 2 {
		t.Errorf("expected 2 files, got %d", len(data))
	}
}

func TestHandleDeleteFile(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	// Create a file
	req, err := createMultipartRequest("/v1/files", map[string]string{"purpose": "batch"}, "test.txt", []byte("hello world"))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rec.Code)
	}

	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	fileID := resp["id"].(string)

	// Delete the file
	req = httptest.NewRequest(http.MethodDelete, "/v1/files/"+fileID, nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var delResp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &delResp); err != nil {
		t.Fatalf("decode delete response: %v", err)
	}
	if delResp["deleted"] != true {
		t.Errorf("expected deleted=true, got %v", delResp["deleted"])
	}

	// Verify file is gone
	req = httptest.NewRequest(http.MethodGet, "/v1/files/"+fileID, nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 after delete, got %d", rec.Code)
	}
}

func TestHandleCreateBatch(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	// First create a file to use as input
	req, err := createMultipartRequest("/v1/files", map[string]string{"purpose": "batch"}, "input.jsonl", []byte(`{"model": "gpt-4", "messages": [{"role": "user", "content": "hi"}]}`))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("create file: expected 201, got %d", rec.Code)
	}

	var fileResp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &fileResp)
	inputFileID := fileResp["id"].(string)

	// Create a batch
	batchReq := map[string]any{
		"input_file_id":     inputFileID,
		"endpoint":          "/v1/chat/completions",
		"completion_window": "24h",
		"metadata":          map[string]string{"custom": "value"},
	}
	batchBody, _ := json.Marshal(batchReq)

	req = httptest.NewRequest(http.MethodPost, "/v1/batches", bytes.NewReader(batchBody))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var batchResp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &batchResp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if batchResp["id"] == nil {
		t.Fatal("expected batch id in response")
	}
	if batchResp["object"] != "batch" {
		t.Errorf("expected object=batch, got %v", batchResp["object"])
	}
	if batchResp["input_file_id"] != inputFileID {
		t.Errorf("expected input_file_id=%s, got %v", inputFileID, batchResp["input_file_id"])
	}
	if batchResp["endpoint"] != "/v1/chat/completions" {
		t.Errorf("expected endpoint=/v1/chat/completions, got %v", batchResp["endpoint"])
	}
	if batchResp["completion_window"] != "24h" {
		t.Errorf("expected completion_window=24h, got %v", batchResp["completion_window"])
	}
	if batchResp["status"] != "validating" {
		t.Errorf("expected status=validating, got %v", batchResp["status"])
	}

	batchID := batchResp["id"].(string)

	// Verify batch can be retrieved
	req = httptest.NewRequest(http.MethodGet, "/v1/batches/"+batchID, nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var getResp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &getResp)
	if getResp["id"] != batchID {
		t.Errorf("expected id=%s, got %v", batchID, getResp["id"])
	}
}

func TestHandleListBatches(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	// Create a file first
	req, err := createMultipartRequest("/v1/files", map[string]string{"purpose": "batch"}, "input.jsonl", []byte(`{"model": "gpt-4", "messages": [{"role": "user", "content": "hi"}]}`))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var fileResp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &fileResp)
	inputFileID := fileResp["id"].(string)

	// Create two batches
	for i := 0; i < 2; i++ {
		batchReq := map[string]any{
			"input_file_id":     inputFileID,
			"endpoint":          "/v1/chat/completions",
			"completion_window": "24h",
		}
		batchBody, _ := json.Marshal(batchReq)

		req = httptest.NewRequest(http.MethodPost, "/v1/batches", bytes.NewReader(batchBody))
		req.Header.Set("Content-Type", "application/json")
		rec = httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusCreated {
			t.Fatalf("create batch %d: expected 201, got %d", i, rec.Code)
		}
	}

	// List batches
	req = httptest.NewRequest(http.MethodGet, "/v1/batches", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	data, ok := resp["data"].([]any)
	if !ok {
		t.Fatalf("expected data array, got %T", resp["data"])
	}
	if len(data) != 2 {
		t.Errorf("expected 2 batches, got %d", len(data))
	}
}

func TestHandleCancelBatch(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	// Create a file first
	req, err := createMultipartRequest("/v1/files", map[string]string{"purpose": "batch"}, "input.jsonl", []byte(`{"model": "gpt-4", "messages": [{"role": "user", "content": "hi"}]}`))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var fileResp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &fileResp)
	inputFileID := fileResp["id"].(string)

	// Create a batch
	batchReq := map[string]any{
		"input_file_id":     inputFileID,
		"endpoint":          "/v1/chat/completions",
		"completion_window": "24h",
	}
	batchBody, _ := json.Marshal(batchReq)

	req = httptest.NewRequest(http.MethodPost, "/v1/batches", bytes.NewReader(batchBody))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var batchResp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &batchResp)
	batchID := batchResp["id"].(string)

	// Cancel the batch
	req = httptest.NewRequest(http.MethodPost, "/v1/batches/"+batchID+"/cancel", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var cancelResp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &cancelResp)
	if cancelResp["status"] != "cancelling" {
		t.Errorf("expected status=cancelling, got %v", cancelResp["status"])
	}
	if cancelResp["cancelled_at"] == nil {
		t.Error("expected cancelled_at to be set")
	}
}

func TestHandleCreateBatch_MissingFields(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	testCases := []struct {
		name     string
		body     map[string]any
		wantCode int
		wantMsg  string
	}{
		{
			name:     "missing input_file_id",
			body:     map[string]any{"endpoint": "/v1/chat/completions", "completion_window": "24h"},
			wantCode: http.StatusBadRequest,
			wantMsg:  "Missing required field: input_file_id",
		},
		{
			name:     "missing endpoint",
			body:     map[string]any{"input_file_id": "file_123", "completion_window": "24h"},
			wantCode: http.StatusBadRequest,
			wantMsg:  "Missing required field: endpoint",
		},
		{
			name:     "missing completion_window",
			body:     map[string]any{"input_file_id": "file_123", "endpoint": "/v1/chat/completions"},
			wantCode: http.StatusBadRequest,
			wantMsg:  "Missing required field: completion_window",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			batchBody, _ := json.Marshal(tc.body)
			req := httptest.NewRequest(http.MethodPost, "/v1/batches", bytes.NewReader(batchBody))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != tc.wantCode {
				t.Fatalf("expected %d, got %d: %s", tc.wantCode, rec.Code, rec.Body.String())
			}
			var resp map[string]any
			_ = json.Unmarshal(rec.Body.Bytes(), &resp)
			if resp["error"] == nil {
				t.Error("expected error in response")
			}
		})
	}
}

func TestHandleGetBatch_NotFound(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/v1/batches/batch_nonexistent", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleCancelBatch_NotCancellable(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	// Create a file first
	req, err := createMultipartRequest("/v1/files", map[string]string{"purpose": "batch"}, "input.jsonl", []byte(`{"model": "gpt-4", "messages": [{"role": "user", "content": "hi"}]}`))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var fileResp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &fileResp)
	inputFileID := fileResp["id"].(string)

	// Create a batch
	batchReq := map[string]any{
		"input_file_id":     inputFileID,
		"endpoint":          "/v1/chat/completions",
		"completion_window": "24h",
	}
	batchBody, _ := json.Marshal(batchReq)

	req = httptest.NewRequest(http.MethodPost, "/v1/batches", bytes.NewReader(batchBody))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var batchResp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &batchResp)
	batchID := batchResp["id"].(string)

	// First cancel should succeed
	req = httptest.NewRequest(http.MethodPost, "/v1/batches/"+batchID+"/cancel", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("first cancel: expected 200, got %d", rec.Code)
	}

	// Second cancel should fail (already cancelled)
	req = httptest.NewRequest(http.MethodPost, "/v1/batches/"+batchID+"/cancel", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("second cancel: expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func createMultipartRequest(url string, fields map[string]string, filename string, fileContent []byte) (*http.Request, error) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for k, v := range fields {
		if err := writer.WriteField(k, v); err != nil {
			return nil, err
		}
	}
	if filename != "" && fileContent != nil {
		part, err := writer.CreateFormFile("file", filename)
		if err != nil {
			return nil, err
		}
		if _, err := part.Write(fileContent); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	req := httptest.NewRequest(http.MethodPost, url, body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req, nil
}
