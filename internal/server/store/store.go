// Package store 是 server 的 SQLite 持久化层（WAL 模式）。
// 时间一律以 UTC RFC3339Nano 文本存储；公开方法的第一个参数为 ctx。
package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Store 封装数据库连接池。
type Store struct {
	db *sql.DB
}

// Open 打开（必要时创建）path 处的数据库，开启 WAL 并执行迁移。
func Open(ctx context.Context, path string) (*Store, error) {
	// foreign_keys/busy_timeout 是连接级 PRAGMA，写在 DSN 中保证池内每条连接都生效。
	dsn := "file:" + path + "?" +
		"_pragma=busy_timeout(5000)&" +
		"_pragma=foreign_keys(1)&" +
		"_pragma=synchronous(NORMAL)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开 sqlite %s: %w", path, err)
	}
	// SQLite 写串行；单连接彻底避免写锁竞争，千级 agent 心跳量足够。
	db.SetMaxOpenConns(1)

	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode=WAL;`); err != nil {
		db.Close()
		return nil, fmt.Errorf("开启 WAL: %w", err)
	}

	if err := migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close 释放连接。
func (s *Store) Close() error {
	return s.db.Close()
}

// Ping 探活，供启动健康检查使用。
func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func migrate(ctx context.Context, db *sql.DB) error {
	var version int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version;`).Scan(&version); err != nil {
		return fmt.Errorf("读取 user_version: %w", err)
	}
	if version >= schemaVersion {
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("迁移建表事务: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, schemaV1); err != nil {
		return fmt.Errorf("执行 schema v%d: %w", schemaVersion, err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d;`, schemaVersion)); err != nil {
		return fmt.Errorf("写入 user_version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交迁移: %w", err)
	}
	return nil
}

const schemaVersion = 1

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// nullableTime 把时间指针转成数据库可扫描类型；nil 存 NULL。
func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}

func parseOptionalTime(raw sql.NullString) (*time.Time, error) {
	if !raw.Valid || raw.String == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339Nano, raw.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
