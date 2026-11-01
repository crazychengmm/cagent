package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"
)

// EnrollmentToken 是 enrollment_tokens 表的一行。明文不落库。
type EnrollmentToken struct {
	ID          string
	Description string
	CreatedBy   string
	CreatedAt   time.Time
	ExpiresAt   time.Time
	RevokedAt   *time.Time
	LastUsedAt  *time.Time
}

// HashToken 返回明文 token 的 SHA-256 十六进制（库中只存哈希）。
func HashToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// CreateEnrollmentToken 内部对明文做哈希后存储；任何记录都不含明文。
func (s *Store) CreateEnrollmentToken(ctx context.Context, t EnrollmentToken, plaintext string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO enrollment_tokens (id, token_hash, description, created_by, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?)`,
		t.ID, HashToken(plaintext), t.Description, t.CreatedBy,
		formatTime(t.CreatedAt), formatTime(t.ExpiresAt))
	if err != nil {
		return fmt.Errorf("插入 enrollment token %s: %w", t.ID, err)
	}
	return nil
}

// GetEnrollmentTokenByPlaintext 注册时用明文反查；不存在返回 ErrNotFound。
func (s *Store) GetEnrollmentTokenByPlaintext(ctx context.Context, plaintext string) (*EnrollmentToken, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, description, created_by, created_at, expires_at, revoked_at, last_used_at
FROM enrollment_tokens WHERE token_hash = ?`, HashToken(plaintext))

	t, err := scanToken(row)
	if err != nil {
		return nil, err
	}
	return t, nil
}

// ListEnrollmentTokens 列出令牌元数据（天然不含明文）。
func (s *Store) ListEnrollmentTokens(ctx context.Context) ([]EnrollmentToken, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, description, created_by, created_at, expires_at, revoked_at, last_used_at
FROM enrollment_tokens ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("列出 enrollment tokens: %w", err)
	}
	defer rows.Close()

	var tokens []EnrollmentToken
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, *t)
	}
	return tokens, rows.Err()
}

// RevokeEnrollmentToken 吊销令牌；返回 false 表示令牌不存在或已吊销。
func (s *Store) RevokeEnrollmentToken(ctx context.Context, id string, at time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE enrollment_tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
		formatTime(at), id)
	if err != nil {
		return false, fmt.Errorf("吊销 token %s: %w", id, err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// TouchEnrollment 使用记录注册成功的时间（last_used_at）。
func (s *Store) TouchEnrollmentToken(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE enrollment_tokens SET last_used_at = ? WHERE id = ?`, formatTime(at), id)
	if err != nil {
		return fmt.Errorf("更新 token 使用时间 %s: %w", id, err)
	}
	return nil
}

func scanToken(row rowScanner) (*EnrollmentToken, error) {
	var (
		t          EnrollmentToken
		createdAt  string
		expiresAt  string
		revokedAt  sql.NullString
		lastUsedAt sql.NullString
	)
	err := row.Scan(&t.ID, &t.Description, &t.CreatedBy, &createdAt, &expiresAt,
		&revokedAt, &lastUsedAt)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("扫描 enrollment token: %w", err)
	}
	if t.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return nil, err
	}
	if t.ExpiresAt, err = time.Parse(time.RFC3339Nano, expiresAt); err != nil {
		return nil, err
	}
	if t.RevokedAt, err = parseOptionalTime(revokedAt); err != nil {
		return nil, err
	}
	if t.LastUsedAt, err = parseOptionalTime(lastUsedAt); err != nil {
		return nil, err
	}
	return &t, nil
}
