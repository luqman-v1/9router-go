package batches

import (
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
)

// FileHandler handles file-related API endpoints for batch processing.
type FileHandler struct {
	repo *db.Repo
}

func NewFileHandler(repo *db.Repo) *FileHandler {
	return &FileHandler{repo: repo}
}

// HandleCreateFile handles POST /v1/files and POST /files
// Accepts multipart/form-data with "file" field and optional "purpose" field.
func (h *FileHandler) HandleCreateFile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	err := r.ParseMultipartForm(32 << 20) // 32 MB max
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Failed to parse multipart form: "+err.Error())
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	file, header, err := r.FormFile("file")
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Missing 'file' field in multipart form")
		return
	}
	defer func() { _ = file.Close() }()

	purpose := r.FormValue("purpose")
	if purpose == "" {
		purpose = "batch"
	}

	content, err := io.ReadAll(file)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to read file content: "+err.Error())
		return
	}

	fileID := db.GenerateFileID()
	now := time.Now().Unix()

	f := &db.File{
		ID:        fileID,
		Filename:  header.Filename,
		Bytes:     int64(len(content)),
		Purpose:   purpose,
		CreatedAt: now,
		Content:   content,
	}

	if err := h.repo.SaveFile(ctx, f); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to save file: "+err.Error())
		return
	}

	response := map[string]any{
		"id":         fileID,
		"object":     "file",
		"bytes":      f.Bytes,
		"created_at": f.CreatedAt,
		"filename":   f.Filename,
		"purpose":    f.Purpose,
	}

	handlerutil.WriteJSON(w, http.StatusCreated, response)
}

// HandleListFiles handles GET /v1/files and GET /files
func (h *FileHandler) HandleListFiles(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	purpose := r.URL.Query().Get("purpose")

	files, err := h.repo.ListFiles(ctx, purpose)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to list files: "+err.Error())
		return
	}

	data := make([]map[string]any, len(files))
	for i, f := range files {
		data[i] = map[string]any{
			"id":         f.ID,
			"object":     "file",
			"bytes":      f.Bytes,
			"created_at": f.CreatedAt,
			"filename":   f.Filename,
			"purpose":    f.Purpose,
		}
	}

	response := map[string]any{
		"object": "list",
		"data":   data,
	}

	handlerutil.WriteJSON(w, http.StatusOK, response)
}

// HandleGetFile handles GET /v1/files/{file_id} and GET /files/{file_id}
func (h *FileHandler) HandleGetFile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	fileID := r.PathValue("file_id")
	if fileID == "" {
		fileID = chi.URLParam(r, "file_id")
	}
	if fileID == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Missing file_id")
		return
	}

	f, err := h.repo.GetFile(ctx, fileID)
	if err != nil {
		if errors.Is(err, db.ErrFileNotFound) {
			handlerutil.WriteJSONError(w, http.StatusNotFound, "File not found")
			return
		}
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to get file: "+err.Error())
		return
	}

	response := map[string]any{
		"id":         f.ID,
		"object":     "file",
		"bytes":      f.Bytes,
		"created_at": f.CreatedAt,
		"filename":   f.Filename,
		"purpose":    f.Purpose,
	}

	handlerutil.WriteJSON(w, http.StatusOK, response)
}

// HandleGetFileContent handles GET /v1/files/{file_id}/content and GET /files/{file_id}/content
func (h *FileHandler) HandleGetFileContent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	fileID := r.PathValue("file_id")
	if fileID == "" {
		fileID = chi.URLParam(r, "file_id")
	}
	if fileID == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Missing file_id")
		return
	}

	content, err := h.repo.GetFileContent(ctx, fileID)
	if err != nil {
		if errors.Is(err, db.ErrFileNotFound) {
			handlerutil.WriteJSONError(w, http.StatusNotFound, "File not found")
			return
		}
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to get file content: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/binary")
	_, _ = w.Write(content)
}

// HandleDeleteFile handles DELETE /v1/files/{file_id} and DELETE /files/{file_id}
func (h *FileHandler) HandleDeleteFile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	fileID := r.PathValue("file_id")
	if fileID == "" {
		fileID = chi.URLParam(r, "file_id")
	}
	if fileID == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Missing file_id")
		return
	}

	err := h.repo.DeleteFile(ctx, fileID)
	if err != nil {
		if errors.Is(err, db.ErrFileNotFound) {
			handlerutil.WriteJSONError(w, http.StatusNotFound, "File not found")
			return
		}
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to delete file: "+err.Error())
		return
	}

	response := map[string]any{
		"id":      fileID,
		"object":  "file",
		"deleted": true,
	}

	handlerutil.WriteJSON(w, http.StatusOK, response)
}
