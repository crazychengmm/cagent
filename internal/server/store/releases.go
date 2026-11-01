package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Release 是 releases 表的一行（包文件本身存数据目录，这里只存元数据）。
type Release struct {
	Version    string
	Size       int64
	SHA256Hex  string
	Signature  []byte
	UploadedBy string
	UploadedAt time.Time
}

// InsertRelease 登记新版本；版本重复时返回错误（不静默覆盖）。
func (s *Store) InsertRelease(ctx context.Context, r Release) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO releases (version, size, sha256_hex, signature, uploaded_by, uploaded_at)
VALUES (?, ?, ?, ?, ?, ?)`,
		r.Version, r.Size, r.SHA256Hex, r.Signature, r.UploadedBy,
		formatTime(r.UploadedAt))
	if err != nil {
		return fmt.Errorf("插入 release %s: %w", r.Version, err)
	}
	return nil
}

// GetRelease 按版本查询；不存在返回 ErrNotFound。
func (s *Store) GetRelease(ctx context.Context, version string) (*Release, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT version, size, sha256_hex, signature, uploaded_by, uploaded_at
FROM releases WHERE version = ?`, version)

	var (
		r          Release
		uploadedAt string
	)
	err := row.Scan(&r.Version, &r.Size, &r.SHA256Hex, &r.Signature, &r.UploadedBy, &uploadedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("扫描 release: %w", err)
	}
	if r.UploadedAt, err = time.Parse(time.RFC3339Nano, uploadedAt); err != nil {
		return nil, err
	}
	return &r, nil
}

// ListReleases 按上传时间倒序列出已发布版本。
func (s *Store) ListReleases(ctx context.Context) ([]Release, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT version, size, sha256_hex, signature, uploaded_by, uploaded_at
FROM releases ORDER BY uploaded_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("查询 releases: %w", err)
	}
	defer rows.Close()

	var releases []Release
	for rows.Next() {
		var (
			r          Release
			uploadedAt string
		)
		if err := rows.Scan(&r.Version, &r.Size, &r.SHA256Hex, &r.Signature,
			&r.UploadedBy, &uploadedAt); err != nil {
			return nil, fmt.Errorf("扫描 release: %w", err)
		}
		if r.UploadedAt, err = time.Parse(time.RFC3339Nano, uploadedAt); err != nil {
			return nil, err
		}
		releases = append(releases, r)
	}
	return releases, rows.Err()
}
