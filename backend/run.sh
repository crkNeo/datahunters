#!/usr/bin/env bash
#
# run.sh — 建置並啟動儀表板 server。
#
#   ./run.sh
#
# 需要提權時(server 綁 80/443)整支腳本一起 sudo:
#
#   sudo ./run.sh
#
# 註:原本的資料採集器 collector 已隨「爆發型態」一併移除,現在只剩 server。
#
set -uo pipefail
cd "$(dirname "$0")"

if [[ ! -f .env ]]; then
  echo "run.sh: 找不到 backend/.env — server 會從這裡讀 MYSQL_DSN" >&2
fi

# 先建置再執行,而不是用 go run:go run 會另外生一層行程,Ctrl-C 之後常留下孤兒。
mkdir -p bin
echo "run.sh: building…"
go build -o bin/server ./cmd/server || exit 1

srv_pid=""
cleanup() {
  trap - INT TERM EXIT
  [[ -n "$srv_pid" ]] && kill "$srv_pid" 2>/dev/null
  for _ in 1 2 3; do
    { [[ -n "$srv_pid" ]] && kill -0 "$srv_pid" 2>/dev/null; } || break
    sleep 1
  done
  [[ -n "$srv_pid" ]] && kill -9 "$srv_pid" 2>/dev/null
  wait 2>/dev/null
}
trap cleanup INT TERM EXIT

# 用行程替換而非管線,這樣 $! 拿到的是程式本身的 PID 而不是 sed 的。
./bin/server > >(sed -u 's/^/[server] /') 2>&1 &
srv_pid=$!
echo "run.sh: server pid=$srv_pid"

wait "$srv_pid"
echo "run.sh: server 結束了" >&2
exit 1
