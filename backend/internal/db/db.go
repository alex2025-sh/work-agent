package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	_ "github.com/go-sql-driver/mysql"
	"github.com/zeromicro/go-zero/core/stores/sqlx"
	_ "modernc.org/sqlite"
)

// Open opens the database. driver is "mysql" (production) or "sqlite" (local).
func Open(driver, dsn string) (*sql.DB, sqlx.SqlConn, error) {
	raw, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, nil, err
	}
	if driver == "sqlite" {
		// SQLite allows a single writer; one connection keeps transactions simple.
		raw.SetMaxOpenConns(1)
		for _, p := range []string{"PRAGMA journal_mode=WAL", "PRAGMA busy_timeout=5000", "PRAGMA foreign_keys=OFF"} {
			if _, err := raw.Exec(p); err != nil {
				return nil, nil, err
			}
		}
	}
	if err := raw.Ping(); err != nil {
		return nil, nil, err
	}
	if err := migrate(raw, driver); err != nil {
		return nil, nil, fmt.Errorf("migrate: %w", err)
	}
	return raw, sqlx.NewSqlConnFromDB(raw), nil
}

// SchemaSQL renders the DDL for a dialect (used by migrate and `-print-schema`).
func SchemaSQL(driver string) string {
	return tableDDL(driver) + "\n" + strings.Join(indexes, ";\n") + ";\n"
}

func tableDDL(driver string) string {
	pk, tail := "INTEGER PRIMARY KEY AUTOINCREMENT", ""
	if driver == "mysql" {
		pk, tail = "BIGINT AUTO_INCREMENT PRIMARY KEY", " ENGINE=InnoDB DEFAULT CHARSET=utf8mb4"
	}
	return strings.ReplaceAll(strings.ReplaceAll(schemaTemplate, "{{PK}}", pk), "{{TAIL}}", tail)
}

func migrate(raw *sql.DB, driver string) error {
	var probe string
	fresh := false
	q := "SELECT name FROM sqlite_master WHERE type='table' AND name='bw_task_type'"
	if driver == "mysql" {
		q = "SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = 'bw_task_type'"
	}
	if err := raw.QueryRow(q).Scan(&probe); errors.Is(err, sql.ErrNoRows) {
		fresh = true
	} else if err != nil {
		return err
	}
	for _, stmt := range strings.Split(tableDDL(driver), ";") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := raw.Exec(stmt); err != nil {
			return fmt.Errorf("%w\n%s", err, stmt)
		}
	}
	// indexes: created on a fresh database, and added when a later version
	// introduces new ones (existing indexes are skipped)
	for _, stmt := range indexes {
		if _, err := raw.Exec(stmt); err != nil {
			msg := err.Error()
			if !fresh && (strings.Contains(msg, "already exists") || strings.Contains(msg, "Duplicate key name") || strings.Contains(msg, "Error 1061")) {
				continue
			}
			return fmt.Errorf("%w\n%s", err, stmt)
		}
	}
	return nil
}

// IsDuplicate reports a unique-key violation on either dialect.
func IsDuplicate(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "UNIQUE constraint failed") || strings.Contains(s, "Error 1062") || strings.Contains(s, "Duplicate entry")
}

// IsNotFound reports go-zero's no-rows error.
func IsNotFound(err error) bool {
	return errors.Is(err, sqlx.ErrNotFound) || errors.Is(err, sql.ErrNoRows)
}

// Querier is satisfied by both sqlx.SqlConn and sqlx.Session.
type Querier interface {
	ExecCtx(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowCtx(ctx context.Context, v any, query string, args ...any) error
	QueryRowsCtx(ctx context.Context, v any, query string, args ...any) error
	QueryRowPartialCtx(ctx context.Context, v any, query string, args ...any) error
	QueryRowsPartialCtx(ctx context.Context, v any, query string, args ...any) error
}

// JSON marshals v, returning "null"-safe text for storage.
func JSON(v any) string {
	if v == nil {
		return "{}"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// Insert runs an INSERT and returns the new id.
func Insert(ctx context.Context, q Querier, query string, args ...any) (int64, error) {
	res, err := q.ExecCtx(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Affected runs a statement and returns rows affected.
func Affected(ctx context.Context, q Querier, query string, args ...any) (int64, error) {
	res, err := q.ExecCtx(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
