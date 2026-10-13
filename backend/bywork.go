package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zeromicro/go-zero/core/conf"
	"github.com/zeromicro/go-zero/core/logx"
	"github.com/zeromicro/go-zero/core/stores/sqlx"
	"github.com/zeromicro/go-zero/rest"

	"bywork/internal/config"
	"bywork/internal/db"
	"bywork/internal/handler"
	"bywork/internal/seed"
	"bywork/internal/svc"
)

var (
	configFile  = flag.String("f", "etc/bywork.yaml", "the config file")
	printSchema = flag.String("print-schema", "", "print DDL for a dialect (mysql|sqlite) and exit")
	mockCases   = flag.Int("mock", 0, "add N random mock CRM cases to the configured database, run one scan, and exit")
	mockSeed    = flag.Int64("mock-seed", 0, "random seed for -mock (default: current time)")
)

func main() {
	flag.Parse()
	if *printSchema != "" {
		fmt.Print(db.SchemaSQL(*printSchema))
		return
	}

	var c config.Config
	conf.MustLoad(*configFile, &c, conf.UseEnv())
	sqlx.DisableStmtLog()

	sc, err := svc.NewServiceContext(c)
	if err != nil {
		logx.Must(err)
	}
	if *mockCases > 0 {
		runMock(sc, *mockCases, *mockSeed)
		return
	}
	opts := []rest.RunOption{rest.WithCors()}
	if c.StaticDir != "" {
		opts = append(opts, rest.WithNotFoundHandler(spa(c.StaticDir)))
	}
	server := rest.MustNewServer(c.RestConf, opts...)
	defer server.Stop()

	handler.RegisterHandlers(server, sc)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sc.StartBackground(ctx)

	fmt.Printf("Starting Bywork at %s:%d...\n", c.Host, c.Port)
	server.Start()
}

func runMock(sc *svc.ServiceContext, n int, rs int64) {
	if rs == 0 {
		rs = time.Now().UnixNano()
	}
	ctx := context.Background()
	st, err := seed.Mock(ctx, sc.Conn, sc.Clock.Now(), n, rs)
	if err != nil {
		logx.Must(err)
	}
	fmt.Printf("added %d mock cases (seed %d):\n", st.Cases, rs)
	for k, v := range st.ByScenario {
		fmt.Printf("  %-40s %d\n", k, v)
	}
	// scan right away instead of waiting for each type's next cron fire
	types, err := sc.Tasks.ListTypes(ctx)
	if err != nil {
		logx.Must(err)
	}
	for _, t := range types {
		if t.Status != "enabled" && t.Status != "shadow" {
			continue
		}
		res, err := sc.Tasks.RunNow(ctx, "mock", t.TypeKey)
		if err != nil {
			fmt.Printf("scan %s: %v\n", t.TypeKey, err)
			continue
		}
		fmt.Printf("scan %-20s %-8s rows=%d created=%d %s\n", t.TypeKey, res.Result, res.Rows, res.Created, res.Message)
	}
	sc.Tick(ctx) // deliver the new cards
}

// spa serves the built frontend and falls back to index.html for client routes.
func spa(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		p := filepath.Join(dir, filepath.Clean("/"+r.URL.Path))
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
			return
		}
		fs.ServeHTTP(w, r)
	})
}
