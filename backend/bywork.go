package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/zeromicro/go-zero/core/conf"
	"github.com/zeromicro/go-zero/core/logx"
	"github.com/zeromicro/go-zero/core/stores/sqlx"
	"github.com/zeromicro/go-zero/rest"

	"bywork/internal/config"
	"bywork/internal/db"
	"bywork/internal/handler"
	"bywork/internal/svc"
)

var (
	configFile  = flag.String("f", "etc/bywork.yaml", "the config file")
	printSchema = flag.String("print-schema", "", "print DDL for a dialect (mysql|sqlite) and exit")
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
