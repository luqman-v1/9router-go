package db

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type File struct {
	ID        string `json:"id"`
	Filename  string `json:"filename"`
	Bytes     int64  `json:"bytes"`
	Purpose   string `json:"purpose"`
	CreatedAt int64  `json:"created_at"`
	Content   []byte `json:"-"`
}

var (
	ErrFileNotFound = errors.New("file not found")
)

func (r *Repo) SaveFile(ctx context.Context, f *File) error {
	const q = `
		INSERT INTO files (id, filename, bytes, purpose, created_at, content)
		VALUES (?, ?, ?, ?, ?, ?)
	`
	_, err := r.db.ExecContext(ctx, q, f.ID, f.Filename, f.Bytes, f.Purpose, f.CreatedAt, f.Content)
	return err
}

func (r *Repo) GetFile(ctx context.Context, id string) (*File, error) {
	const q = `SELECT id, filename, bytes, purpose, created_at FROM files WHERE id = ?`
	row := r.db.QueryRowContext(ctx, q, id)
	var f File
	err := row.Scan(&f.ID, &f.Filename, &f.Bytes, &f.Purpose, &f.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, ErrFileNotFound
	}
	return &f, err
}

func (r *Repo) GetFileContent(ctx context.Context, id string) ([]byte, error) {
	const q = `SELECT content FROM files WHERE id = ?`
	var content []byte
	err := r.db.QueryRowContext(ctx, q, id).Scan(&content)
	if err == sql.ErrNoRows {
		return nil, ErrFileNotFound
	}
	return content, err
}

func (r *Repo) ListFiles(ctx context.Context, purpose string) ([]*File, error) {
	var q string
	var args []any
	if purpose != "" {
		q = `SELECT id, filename, bytes, purpose, created_at FROM files WHERE purpose = ? ORDER BY created_at DESC`
		args = append(args, purpose)
	} else {
		q = `SELECT id, filename, bytes, purpose, created_at FROM files ORDER BY created_at DESC`
	}
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []*File
	for rows.Next() {
		var f File
		if err := rows.Scan(&f.ID, &f.Filename, &f.Bytes, &f.Purpose, &f.CreatedAt); err != nil {
			return nil, err
		}
		files = append(files, &f)
	}
	return files, rows.Err()
}

func (r *Repo) DeleteFile(ctx context.Context, id string) error {
	const q = `DELETE FROM files WHERE id = ?`
	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrFileNotFound
	}
	return nil
}

func generateFileID() string {
	return "file_" + time.Now().UTC().Format("20060102150405") + "_" + randString(16)
}

func randString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[randInt(len(letters))]
	}
	return string(b)
}

func randInt(n int) int {
	return int(time.Now().UnixNano()) % n
}
