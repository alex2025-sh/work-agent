package db

import (
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/zeromicro/go-zero/core/stores/sqlx"
)

// OpenForTest opens a fresh, empty database for one test.
//
// By default it is a SQLite file in the test's temp dir. When
// BYWORK_TEST_MYSQL_DSN is set (e.g. "root:pass@tcp(127.0.0.1:3306)/"), each
// test gets its own MySQL database (bywork_t_<id>), dropped after the test
// unless BYWORK_TEST_KEEP_DB=1.
func OpenForTest(tb testing.TB) (*sql.DB, sqlx.SqlConn) {
	tb.Helper()
	base := os.Getenv("BYWORK_TEST_MYSQL_DSN")
	if base == "" {
		raw, conn, err := Open("sqlite", "file:"+filepath.Join(tb.TempDir(), "t.db"))
		if err != nil {
			tb.Fatal(err)
		}
		tb.Cleanup(func() { _ = raw.Close() })
		return raw, conn
	}
	cfg, err := mysql.ParseDSN(base)
	if err != nil {
		tb.Fatalf("BYWORK_TEST_MYSQL_DSN: %v", err)
	}
	name := fmt.Sprintf("bywork_t_%d_%04d", time.Now().UnixNano()%1e10, rand.Intn(10000))
	cfg.DBName = ""
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		tb.Fatal(err)
	}
	if _, err := admin.Exec("CREATE DATABASE " + name + " CHARACTER SET utf8mb4"); err != nil {
		tb.Fatalf("create test database: %v", err)
	}
	cfg.DBName = name
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Params["charset"] = "utf8mb4"
	raw, conn, err := Open("mysql", cfg.FormatDSN())
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() {
		_ = raw.Close()
		if os.Getenv("BYWORK_TEST_KEEP_DB") != "1" {
			_, _ = admin.Exec("DROP DATABASE " + name)
		} else {
			tb.Logf("kept MySQL database %s", name)
		}
		_ = admin.Close()
	})
	return raw, conn
}
