#!/usr/bin/env bash
#
# run.sh — 一條指令同時啟動儀表板 server 與 collector(精簡為「爆發型態」專用)。
#
# collector 已縮到只餵「爆發型態」分頁的最小管線(snap_1m → pattern_hits),
# 其餘採集(深度/現貨/解鎖/labels)預設關閉。詳見下方 patterns_only 旗標。
#
#   ./run.sh                    兩個都跑
#   ./run.sh -universe 50       多餘參數會轉給 collector(可覆蓋預設旗標)
#   ./run.sh --only server      只跑其中一個
#   ./run.sh --only collector
#
# 兩者是各自獨立的行程,不是同一個程式的兩條執行緒 —— 這是刻意的:
# collector 打幣安 API,被限速或掛掉時不能有機會把網站一起帶走。這支
# 腳本只是把「啟動」和「關閉」綁在一起,行程隔離仍然成立。
#
# Ctrl-C 會同時收掉兩個;任何一邊自己死掉,另一邊也會被收掉並回報是誰。
#
# 需要提權時(server 綁 80/443)整支腳本一起 sudo:
#
#   sudo ./run.sh
#
set -uo pipefail
cd "$(dirname "$0")"

want_server=1
want_collector=1
args=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --only)
      case "${2:-}" in
        server)    want_collector=0 ;;
        collector) want_server=0 ;;
        *) echo "run.sh: --only 需要 server 或 collector" >&2; exit 2 ;;
      esac
      shift 2
      ;;
    -h|--help)
      sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *) args+=("$1"); shift ;;
  esac
done

if [[ ! -f .env ]]; then
  echo "run.sh: 找不到 backend/.env — server 與 collector 都會從這裡讀 MYSQL_DSN" >&2
fi

# 先建置再執行,而不是用兩個 go run:go run 會另外生一層行程,Ctrl-C
# 之後常常留下孤兒。第一次在 sudo 下建置會重跑一次編譯(root 的 GOCACHE
# 是空的),之後就有快取了。
mkdir -p bin
echo "run.sh: building…"
[[ $want_server    == 1 ]] && { go build -o bin/server    ./cmd/server    || exit 1; }
[[ $want_collector == 1 ]] && { go build -o bin/collector ./cmd/collector || exit 1; }

srv_pid=""
col_pid=""

cleanup() {
  trap - INT TERM EXIT
  for p in "$srv_pid" "$col_pid"; do
    [[ -n "$p" ]] && kill "$p" 2>/dev/null
  done
  # 給它們一點時間把手上的寫入收乾淨(collector 正在寫的那一分鐘)
  for _ in 1 2 3 4 5; do
    if ! { [[ -n "$srv_pid" ]] && kill -0 "$srv_pid" 2>/dev/null; } &&
       ! { [[ -n "$col_pid" ]] && kill -0 "$col_pid" 2>/dev/null; }; then
      break
    fi
    sleep 1
  done
  for p in "$srv_pid" "$col_pid"; do
    [[ -n "$p" ]] && kill -9 "$p" 2>/dev/null
  done
  wait 2>/dev/null
}
trap cleanup INT TERM EXIT

# 用行程替換而非管線,這樣 $! 拿到的是程式本身的 PID 而不是 sed 的 ——
# 管線版本會讓 cleanup 殺錯對象,程式反而活下來。
if [[ $want_server == 1 ]]; then
  ./bin/server > >(sed -u 's/^/[server]    /') 2>&1 &
  srv_pid=$!
  echo "run.sh: server pid=$srv_pid"
fi
if [[ $want_collector == 1 ]]; then
  # store-free 模式:collector 已不寫任何逐分鐘快照到 DB —— 偵測吃記憶體滾動窗、
  # 結果回填抓即時 K 線,只有 pattern_hits(訊號+結果)持久化供「爆發型態」分頁。
  # 這些旗標省掉深度/現貨的無謂抓取,並確保 labeler/unlock 不去寫已移除的表。
  # 放在使用者 args 之前,你仍可覆蓋。
  patterns_only=(-depth-every 0 -spot-every 0 -no-unlocks -no-labeler)
  ./bin/collector "${patterns_only[@]}" "${args[@]+"${args[@]}"}" > >(sed -u 's/^/[collector] /') 2>&1 &
  col_pid=$!
  echo "run.sh: collector pid=$col_pid (patterns-only) ${args[*]+${args[*]}}"
fi

if [[ -z "$srv_pid$col_pid" ]]; then
  echo "run.sh: 沒有要啟動的東西" >&2
  exit 2
fi

# 任一邊結束就整組收掉,而不是留下半套在跑 —— 半套狀態最難察覺:
# 網站看起來好好的,資料卻早就斷了好幾小時。
wait -n
dead="unknown"
[[ -n "$srv_pid" ]] && ! kill -0 "$srv_pid" 2>/dev/null && dead="server"
[[ -n "$col_pid" ]] && ! kill -0 "$col_pid" 2>/dev/null && dead="collector"
echo "run.sh: $dead 結束了 — 一併收掉另一個" >&2
exit 1
