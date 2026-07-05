// Command emacheck is a read-only diagnostic: for each symbol it fetches 1h
// klines and prints the last few CLOSED bars' EMA5/EMA20/EMA50 + the strategy
// flags (golden = EMA5>EMA20, above50 = close>EMA50), marking where EMA5 crossed
// EMA20. Use it to see a coin's REAL 1h state vs what the live scan shows.
//
//	go run ./cmd/emacheck -symbols ORDI,BTC,AAVE
package main

import (
	"flag"
	"fmt"
	"strings"
	"time"

	"datahunter/internal/exchange"
)

func emaSeries(cs []exchange.Candle, p int) []float64 {
	out := make([]float64, len(cs))
	if len(cs) == 0 {
		return out
	}
	k := 2.0 / float64(p+1)
	out[0] = cs[0].Close
	for i := 1; i < len(cs); i++ {
		out[i] = cs[i].Close*k + out[i-1]*(1-k)
	}
	return out
}

func main() {
	symbolsCSV := flag.String("symbols", "ORDI,BTC,AAVE,SEI", "coins")
	bars := flag.Int("bars", 6, "last N closed bars to print")
	flag.Parse()
	ex := exchange.NewClient()

	for _, coin := range strings.Split(*symbolsCSV, ",") {
		coin = strings.TrimSpace(coin)
		kl, err := ex.BinanceKlines(coin+"USDT", "1h", 120)
		if err != nil || len(kl) < 60 {
			fmt.Printf("%-6s 抓取失敗: %v\n", coin, err)
			continue
		}
		c := kl[:len(kl)-1] // drop the still-forming bar → CLOSED bars only
		e5 := emaSeries(c, 5)
		e20 := emaSeries(c, 20)
		e50 := emaSeries(c, 50)
		n := len(c) - 1
		fmt.Printf("\n=== %s (最後收盤 K: %s UTC) ===\n", coin,
			time.UnixMilli(c[n].Ts).UTC().Format("01-02 15:04"))
		fmt.Printf("%-16s %10s %10s %10s %10s  %s\n", "收K時間(UTC)", "收盤", "EMA5", "EMA20", "EMA50", "旗標")
		for i := n - *bars + 1; i <= n; i++ {
			if i < 1 {
				continue
			}
			golden := e5[i] > e20[i]
			above := c[i].Close > e50[i]
			cross := ""
			if e5[i] > e20[i] && e5[i-1] <= e20[i-1] {
				cross = "  ← 金叉!"
			} else if e5[i] < e20[i] && e5[i-1] >= e20[i-1] {
				cross = "  ← 死叉!"
			}
			gl := "死叉"
			if golden {
				gl = "金叉"
			}
			ab := "跌破50"
			if above {
				ab = "站上50"
			}
			fmt.Printf("%-16s %10.4g %10.4g %10.4g %10.4g  %s/%s%s\n",
				time.UnixMilli(c[i].Ts).UTC().Format("01-02 15:04"),
				c[i].Close, e5[i], e20[i], e50[i], ab, gl, cross)
		}
	}
	fmt.Println("\n註: 策略用的是『已收盤 1H』;若你圖上是 5m/15m 的金叉, 跟這裡的 1H 狀態不一定一致。")
}
