package cache

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// tradelog.go: 把「開單相關」事件寫到 logs/trades.log(開單歷史 + 實盤各帳號下單結果),
// 方便事後查「哪個帳號沒開到什麼幣、為什麼」。同時印到 stdout 保留即時可見性。
// 開單事件頻率低,不會像先前的逐根評估 log 那樣洗版。

var (
	tradeLogMu sync.Mutex
	tradeLog   *log.Logger // nil = 開檔失敗,只走 stdout
)

// initTradeLog 開 logs/trades.log(append);開檔失敗就只印 stdout,不影響主流程。
func initTradeLog() {
	if err := os.MkdirAll("logs", 0o755); err != nil {
		log.Printf("trade log: 建 logs/ 失敗,改只印 stdout: %v", err)
		return
	}
	f, err := os.OpenFile(filepath.Join("logs", "trades.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		log.Printf("trade log: 開 logs/trades.log 失敗,改只印 stdout: %v", err)
		return
	}
	tradeLog = log.New(f, "", log.LstdFlags)
}

// tradeLogf 寫一筆開單日誌:印到 stdout(即時)也寫進 logs/trades.log(歷史)。
func tradeLogf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	log.Print(line)
	tradeLogMu.Lock()
	if tradeLog != nil {
		tradeLog.Println(line)
	}
	tradeLogMu.Unlock()
}
