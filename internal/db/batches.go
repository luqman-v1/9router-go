package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

type Batch struct {
	ID               string         `json:"id"`
	InputFileID      string         `json:"input_file_id"`
	Endpoint         string         `json:"endpoint"`
	CompletionWindow string         `json:"completion_window"`
	Status           string         `json:"status"`
	OutputFileID     sql.NullString `json:"output_file_id,omitempty"`
	ErrorFileID      sql.NullString `json:"error_file_id,omitempty"`
	CreatedAt        int64          `json:"created_at"`
	CompletedAt      sql.NullInt64  `json:"completed_at,omitempty"`
	CancelledAt      sql.NullInt64  `json:"cancelled_at,omitempty"`
	Metadata         *string        `json:"metadata,omitempty"`
}

type BatchFilter struct {
	Limit  int
	After  string
	Status string
}

var batchIDSeq int64

func generateBatchID() string {
	seq := atomic.AddInt64(&batchIDSeq, 1)
	return fmt.Sprintf("batch_%d_%d", time.Now().UnixNano(), seq)
}

func (r *Repo) CreateBatch(ctx context.Context, inputFileID, endpoint, completionWindow string, metadata json.RawMessage) (*Batch, error) {
	now := time.Now().Unix()
	id := generateBatchID()

	var metaStr *string
	if len(metadata) > 0 {
		s := string(metadata)
		metaStr = &s
	}

	batch := &Batch{
		ID:               id,
		InputFileID:      inputFileID,
		Endpoint:         endpoint,
		CompletionWindow: completionWindow,
		Status:           "validating",
		CreatedAt:        now,
		Metadata:         metaStr,
	}

	query := `
		INSERT INTO batches (id, input_file_id, endpoint, completion_window, status, created_at, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`
	_, err := r.db.ExecContext(ctx, query, batch.ID, batch.InputFileID, batch.Endpoint, batch.CompletionWindow, batch.Status, batch.CreatedAt, batch.Metadata)
	if err != nil {
		return nil, fmt.Errorf("CreateBatch: %w", err)
	}
	return batch, nil
}

func (r *Repo) GetBatch(ctx context.Context, id string) (*Batch, error) {
	query := `
		SELECT id, input_file_id, endpoint, completion_window, status, output_file_id, error_file_id, created_at, completed_at, cancelled_at, metadata
		FROM batches WHERE id = ?
	`
	row := r.db.QueryRowContext(ctx, query, id)

	var batch Batch
	err := row.Scan(
		&batch.ID,
		&batch.InputFileID,
		&batch.Endpoint,
		&batch.CompletionWindow,
		&batch.Status,
		&batch.OutputFileID,
		&batch.ErrorFileID,
		&batch.CreatedAt,
		&batch.CompletedAt,
		&batch.CancelledAt,
		&batch.Metadata,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("GetBatch: %w", err)
	}
	return &batch, nil
}

func (r *Repo) ListBatches(ctx context.Context, filter BatchFilter) ([]*Batch, error) {
	query := `
		SELECT id, input_file_id, endpoint, completion_window, status, output_file_id, error_file_id, created_at, completed_at, cancelled_at, metadata
		FROM batches
	`
	args := []any{}
	whereAdded := false

	if filter.Status != "" {
		query += " WHERE status = ?"
		args = append(args, filter.Status)
		whereAdded = true
	}

	if filter.After != "" {
		if whereAdded {
			query += " AND id > ?"
		} else {
			query += " WHERE id > ?"
		}
		args = append(args, filter.After)
	}

	query += " ORDER BY created_at DESC"

	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("ListBatches: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var batches []*Batch
	for rows.Next() {
		var batch Batch
		err := rows.Scan(
			&batch.ID,
			&batch.InputFileID,
			&batch.Endpoint,
			&batch.CompletionWindow,
			&batch.Status,
			&batch.OutputFileID,
			&batch.ErrorFileID,
			&batch.CreatedAt,
			&batch.CompletedAt,
			&batch.CancelledAt,
			&batch.Metadata,
		)
		if err != nil {
			return nil, fmt.Errorf("ListBatches scan: %w", err)
		}
		batches = append(batches, &batch)
	}
	return batches, rows.Err()
}

func (r *Repo) UpdateBatchStatus(ctx context.Context, id, status string, outputFileID, errorFileID sql.NullString) error {
	now := time.Now().Unix()
	var completedAt, cancelledAt sql.NullInt64

	if status == "completed" || status == "failed" || status == "expired" {
		completedAt = sql.NullInt64{Int64: now, Valid: true}
	}
	if status == "cancelled" {
		cancelledAt = sql.NullInt64{Int64: now, Valid: true}
	}

	query := `
		UPDATE batches
		SET status = ?, output_file_id = ?, error_file_id = ?, completed_at = ?, cancelled_at = ?
		WHERE id = ?
	`
	result, err := r.db.ExecContext(ctx, query, status, outputFileID, errorFileID, completedAt, cancelledAt, id)
	if err != nil {
		return fmt.Errorf("UpdateBatchStatus: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("UpdateBatchStatus rows: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("UpdateBatchStatus: batch not found")
	}
	return nil
}

func (r *Repo) CancelBatch(ctx context.Context, id string) error {
	query := `UPDATE batches SET status = 'cancelling', cancelled_at = ? WHERE id = ? AND status IN ('validating', 'in_progress', 'finalizing')`
	now := time.Now().Unix()
	result, err := r.db.ExecContext(ctx, query, now, id)
	if err != nil {
		return fmt.Errorf("CancelBatch: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("CancelBatch rows: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("CancelBatch: batch not found or not cancellable")
	}
	return nil
}
