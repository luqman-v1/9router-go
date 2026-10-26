package batches

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
)

// BatchHandler handles batch-related API endpoints.
type BatchHandler struct {
	repo *db.Repo
}

func NewBatchHandler(repo *db.Repo) *BatchHandler {
	return &BatchHandler{repo: repo}
}

// CreateBatchRequest represents the request body for creating a batch.
type CreateBatchRequest struct {
	InputFileID      string          `json:"input_file_id"`
	Endpoint         string          `json:"endpoint"`
	CompletionWindow string          `json:"completion_window"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`
}

// HandleCreateBatch handles POST /v1/batches and POST /batches
func (h *BatchHandler) HandleCreateBatch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req CreateBatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	if req.InputFileID == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Missing required field: input_file_id")
		return
	}
	if req.Endpoint == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Missing required field: endpoint")
		return
	}
	if req.CompletionWindow == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Missing required field: completion_window")
		return
	}

	batch, err := h.repo.CreateBatch(ctx, req.InputFileID, req.Endpoint, req.CompletionWindow, req.Metadata)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to create batch: "+err.Error())
		return
	}

	response := batchToResponse(batch)
	handlerutil.WriteJSON(w, http.StatusCreated, response)
}

// HandleListBatches handles GET /v1/batches and GET /batches
func (h *BatchHandler) HandleListBatches(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	after := r.URL.Query().Get("after")
	status := r.URL.Query().Get("status")

	filter := db.BatchFilter{
		Limit:  limit,
		After:  after,
		Status: status,
	}

	batches, err := h.repo.ListBatches(ctx, filter)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to list batches: "+err.Error())
		return
	}

	data := make([]map[string]any, len(batches))
	for i, b := range batches {
		data[i] = batchToResponse(b)
	}

	response := map[string]any{
		"object": "list",
		"data":   data,
	}

	handlerutil.WriteJSON(w, http.StatusOK, response)
}

// HandleGetBatch handles GET /v1/batches/{batch_id} and GET /batches/{batch_id}
func (h *BatchHandler) HandleGetBatch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	batchID := r.PathValue("batch_id")
	if batchID == "" {
		batchID = chi.URLParam(r, "batch_id")
	}
	if batchID == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Missing batch_id")
		return
	}

	batch, err := h.repo.GetBatch(ctx, batchID)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to get batch: "+err.Error())
		return
	}
	if batch == nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "Batch not found")
		return
	}

	response := batchToResponse(batch)
	handlerutil.WriteJSON(w, http.StatusOK, response)
}

// HandleCancelBatch handles POST /v1/batches/{batch_id}/cancel and POST /batches/{batch_id}/cancel
func (h *BatchHandler) HandleCancelBatch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	batchID := r.PathValue("batch_id")
	if batchID == "" {
		batchID = chi.URLParam(r, "batch_id")
	}
	if batchID == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Missing batch_id")
		return
	}

	err := h.repo.CancelBatch(ctx, batchID)
	if err != nil {
		if err.Error() == "CancelBatch: batch not found or not cancellable" {
			handlerutil.WriteJSONError(w, http.StatusNotFound, "Batch not found or not cancellable")
			return
		}
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to cancel batch: "+err.Error())
		return
	}

	// Return the updated batch
	batch, err := h.repo.GetBatch(ctx, batchID)
	if err != nil || batch == nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to get batch after cancellation")
		return
	}

	response := batchToResponse(batch)
	handlerutil.WriteJSON(w, http.StatusOK, response)
}

func batchToResponse(b *db.Batch) map[string]any {
	resp := map[string]any{
		"id":                b.ID,
		"object":            "batch",
		"input_file_id":     b.InputFileID,
		"endpoint":          b.Endpoint,
		"completion_window": b.CompletionWindow,
		"status":            b.Status,
		"created_at":        b.CreatedAt,
	}

	if b.OutputFileID.Valid {
		resp["output_file_id"] = b.OutputFileID.String
	}
	if b.ErrorFileID.Valid {
		resp["error_file_id"] = b.ErrorFileID.String
	}
	if b.CompletedAt.Valid {
		resp["completed_at"] = b.CompletedAt.Int64
	}
	if b.CancelledAt.Valid {
		resp["cancelled_at"] = b.CancelledAt.Int64
	}
	if b.Metadata != nil && len(*b.Metadata) > 0 {
		resp["metadata"] = json.RawMessage(*b.Metadata)
	}

	return resp
}
