#!/usr/bin/env bash
# Local end-to-end run: Go e2e suite + browser run for the two SOP alerts,
# then an HTML report with screenshots. See docs/e2e.md.
#
#   scripts/e2e-local.sh                      # MySQL at 127.0.0.1:3306, user root, no password
#   MYSQL_PASSWORD=secret scripts/e2e-local.sh
#   E2E_DRIVER=sqlite scripts/e2e-local.sh    # no MySQL needed
#
# Env: MYSQL_HOST MYSQL_PORT MYSQL_USER MYSQL_PASSWORD
#      E2E_DB (default bywork_e2e — DROPPED and recreated every run)
#      E2E_PORT (default 18890)  E2E_DRIVER (mysql|sqlite, default mysql)
#      SKIP_GO=1 SKIP_BROWSER=1 SKIP_BUILD=1  HEADED=1  PW_CHROMIUM=/path/to/chromium
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="$ROOT/e2e/out"
MYSQL_HOST="${MYSQL_HOST:-127.0.0.1}"
MYSQL_PORT="${MYSQL_PORT:-3306}"
MYSQL_USER="${MYSQL_USER:-root}"
MYSQL_PASSWORD="${MYSQL_PASSWORD:-}"
E2E_DB="${E2E_DB:-bywork_e2e}"
E2E_PORT="${E2E_PORT:-18890}"
E2E_DRIVER="${E2E_DRIVER:-mysql}"
BASE_URL="http://127.0.0.1:${E2E_PORT}"

say() { printf '\n\033[1;32m==> %s\033[0m\n' "$*"; }
die() { printf '\033[1;31m%s\033[0m\n' "$*" >&2; exit 1; }

case "$E2E_DB" in *[!a-zA-Z0-9_]*|"") die "E2E_DB 只能包含字母、数字和下划线: $E2E_DB";; esac
for t in go node npm curl; do command -v "$t" >/dev/null || die "缺少命令: $t"; done
mkdir -p "$OUT"

MYSQL_BASE_DSN="${MYSQL_USER}:${MYSQL_PASSWORD}@tcp(${MYSQL_HOST}:${MYSQL_PORT})/"
if [ "$E2E_DRIVER" = mysql ]; then
  command -v mysql >/dev/null || die "缺少 mysql 客户端（或用 E2E_DRIVER=sqlite）"
  say "重建数据库 ${E2E_DB} (${MYSQL_HOST}:${MYSQL_PORT})"
  MYSQL_PWD="$MYSQL_PASSWORD" mysql -h"$MYSQL_HOST" -P"$MYSQL_PORT" -u"$MYSQL_USER" \
    -e "DROP DATABASE IF EXISTS \`${E2E_DB}\`; CREATE DATABASE \`${E2E_DB}\` CHARACTER SET utf8mb4;" \
    || die "无法连接 MySQL 或没有 CREATE/DROP DATABASE 权限"
  export BYWORK_E2E_DSN="${MYSQL_BASE_DSN}${E2E_DB}?charset=utf8mb4"
else
  rm -f "$OUT/e2e.db" "$OUT/e2e.db-shm" "$OUT/e2e.db-wal"
  export BYWORK_E2E_DSN="file:$OUT/e2e.db"
fi
export BYWORK_E2E_DRIVER="$E2E_DRIVER" BYWORK_E2E_PORT="$E2E_PORT" BYWORK_E2E_STATIC="$ROOT/frontend/dist"

GO_RC=0
if [ "${SKIP_GO:-}" != 1 ]; then
  say "Go 测试（${E2E_DRIVER}）"
  if [ "$E2E_DRIVER" = mysql ]; then export BYWORK_TEST_MYSQL_DSN="$MYSQL_BASE_DSN"; fi
  (cd "$ROOT/backend" && go test -count=1 -v ./... 2>&1) | tee "$OUT/gotest.txt" | grep -E '^(--- |ok|FAIL)'
  GO_RC=${PIPESTATUS[0]}
  unset BYWORK_TEST_MYSQL_DSN
else
  rm -f "$OUT/gotest.txt"
fi

BROWSER_RC=0
if [ "${SKIP_BROWSER:-}" != 1 ]; then
  if [ "${SKIP_BUILD:-}" != 1 ] || [ ! -f "$ROOT/frontend/dist/index.html" ]; then
    say "构建前端"
    (cd "$ROOT/frontend" && { [ -d node_modules ] || npm install --no-audit --no-fund; } && npm run build) >"$OUT/frontend-build.log" 2>&1 \
      || die "前端构建失败，见 $OUT/frontend-build.log"
  fi
  say "构建并启动后端 ${BASE_URL}"
  (cd "$ROOT/backend" && go build -o "$OUT/bywork" .) || die "后端构建失败"
  (cd "$ROOT/backend" && exec "$OUT/bywork" -f etc/bywork.e2e.yaml) >"$OUT/server.log" 2>&1 &
  SERVER_PID=$!
  trap 'kill $SERVER_PID 2>/dev/null' EXIT
  for _ in $(seq 1 60); do
    curl -fs -H 'X-Bywork-User: admin' "$BASE_URL/api/me" >/dev/null 2>&1 && break
    kill -0 $SERVER_PID 2>/dev/null || die "后端启动失败，见 $OUT/server.log"
    sleep 1
  done
  curl -fs -H 'X-Bywork-User: admin' "$BASE_URL/api/me" >/dev/null || die "后端未就绪，见 $OUT/server.log"

  say "浏览器端到端（Playwright）"
  cd "$ROOT/e2e"
  [ -d node_modules ] || npm install --no-audit --no-fund >/dev/null 2>&1 || die "e2e 依赖安装失败"
  if [ -z "${PW_CHROMIUM:-}" ]; then npx playwright install chromium >/dev/null 2>&1 || die "Chromium 安装失败（可设置 PW_CHROMIUM 使用本机浏览器）"; fi
  BASE_URL="$BASE_URL" E2E_OUT="$OUT" node sop.e2e.mjs
  BROWSER_RC=$?
  kill $SERVER_PID 2>/dev/null
fi

if [ -f "$OUT/results.json" ]; then
  say "生成报告"
  (cd "$ROOT/e2e" && E2E_DB="$E2E_DRIVER" node report.mjs "$OUT")
fi

if [ "$GO_RC" != 0 ] || [ "$BROWSER_RC" != 0 ]; then
  die "失败：go=$GO_RC browser=$BROWSER_RC（报告：$OUT/report.html）"
fi
say "全部通过 → $OUT/report.html"
