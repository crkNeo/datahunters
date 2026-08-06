// Command ema200bt tests an EMA200 trend/regime filter against the REAL July
// trades (not a re-simulation). For each closed trade it looks up, at the trade's
// entry time, whether the coin's price was above/below its own EMA200, and whether
// BTC was above/below its EMA200 (market regime). It then re-partitions the actual
// realized P&L to show what each filter would have kept vs dropped.
//
// No look-ahead: EMA200 at entry uses only bars up to the entry timestamp.
//
//	go run ./cmd/ema200bt -csv "C:\path\7月實績.csv" -books gamble,main,emaonly
package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"datahunter/internal/exchange"
)

func atof(s string) float64 { f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64); return f }

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

// candles fetches ~1000 Binance klines (oldest→newest); 1h ⇒ ~41 days, enough to
// cover July entries plus 200-bar EMA warmup. Returns nil if the symbol is absent.
func candles(ex *exchange.Client, coin, interval string) []exchange.Candle {
	cs, err := ex.BinanceKlines(coin+"USDT", interval, 1000)
	if err != nil || len(cs) < 210 {
		return nil
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].Ts < cs[j].Ts })
	time.Sleep(200 * time.Millisecond)
	return cs
}

// idxAt returns the index of the newest bar whose Ts <= tsMs (the bar in force at
// entry), or -1 if none / insufficient warmup handled by caller.
func idxAt(cs []exchange.Candle, tsMs int64) int {
	lo, hi, ans := 0, len(cs)-1, -1
	for lo <= hi {
		mid := (lo + hi) / 2
		if cs[mid].Ts <= tsMs {
			ans = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return ans
}

type trade struct {
	book, coin, dir string
	entry, pnl      float64
	ts              int64
}

type acc struct {
	n, win     int
	sum        float64
}

func (a *acc) add(pnl float64) {
	a.n++
	a.sum += pnl
	if pnl > 0 {
		a.win++
	}
}
func (a acc) line(label string) {
	if a.n == 0 {
		fmt.Printf("  %-26s n=0\n", label)
		return
	}
	fmt.Printf("  %-26s n=%-4d 勝率%5.1f%%  合計%+8.1f  平均%+6.2f\n",
		label, a.n, 100*float64(a.win)/float64(a.n), a.sum, a.sum/float64(a.n))
}

func main() {
	csvPath := flag.String("csv", `C:\Users\meteo\Desktop\7月實績.csv`, "trades CSV")
	booksArg := flag.String("books", "gamble,main,emaonly", "comma list, or 'all'")
	bar := flag.String("bar", "1h", "EMA200 timeframe: 1h | 4h")
	flag.Parse()

	f, err := os.Open(*csvPath)
	if err != nil {
		fmt.Println("open csv:", err)
		return
	}
	defer f.Close()
	r := csv.NewReader(f)
	rows, err := r.ReadAll()
	if err != nil || len(rows) < 2 {
		fmt.Println("read csv:", err)
		return
	}
	col := map[string]int{}
	for i, h := range rows[0] {
		col[strings.TrimSpace(strings.TrimPrefix(h, "\ufeff"))] = i
	}
	wantAll := *booksArg == "all"
	want := map[string]bool{}
	for _, b := range strings.Split(*booksArg, ",") {
		want[strings.TrimSpace(b)] = true
	}

	var trades []trade
	for _, row := range rows[1:] {
		book := row[col["book"]]
		if row[col["status"]] != "closed" {
			continue
		}
		if !wantAll && !want[book] {
			continue
		}
		rz := atof(row[col["realized"]])
		pnl := rz
		if rz == 0 {
			pnl = atof(row[col["pnl_pct"]])
		}
		trades = append(trades, trade{
			book: book, coin: row[col["coin"]], dir: row[col["dir"]],
			entry: atof(row[col["entry"]]), pnl: pnl,
			ts: int64(atof(row[col["open_time"]])),
		})
	}

	ex := exchange.NewClient()
	// BTC regime series
	btc := candles(ex, "BTC", *bar)
	btcEMA := emaSeries(btc, 200)

	// group by coin, fetch once
	byCoin := map[string][]int{}
	for i, t := range trades {
		byCoin[t.coin] = append(byCoin[t.coin], i)
	}
	type ctx struct {
		coinAligned, btcBull bool
		ok                   bool
	}
	tctx := make([]ctx, len(trades))
	covered, missing := 0, map[string]bool{}
	for coin, idxs := range byCoin {
		cs := candles(ex, coin, *bar)
		if cs == nil {
			missing[coin] = true
			continue
		}
		ema := emaSeries(cs, 200)
		for _, ti := range idxs {
			t := trades[ti]
			bi := idxAt(cs, t.ts)
			if bi < 200 {
				continue
			}
			coinAligned := (t.dir == "long" && t.entry > ema[bi]) || (t.dir == "short" && t.entry < ema[bi])
			btcBull := true
			if len(btc) > 200 {
				if bj := idxAt(btc, t.ts); bj >= 200 {
					btcBull = btc[bj].Close > btcEMA[bj]
				}
			}
			tctx[ti] = ctx{coinAligned, btcBull, true}
			covered++
		}
	}

	// partitions
	base, coinF, btcF, both := &acc{}, &acc{}, &acc{}, &acc{}
	dropCoin, dropBtc := &acc{}, &acc{}
	perBookBase := map[string]*acc{}
	perBookCoin := map[string]*acc{} // coin's own EMA200 filter only
	for i, t := range trades {
		c := tctx[i]
		if !c.ok {
			continue
		}
		if perBookBase[t.book] == nil {
			perBookBase[t.book] = &acc{}
			perBookCoin[t.book] = &acc{}
		}
		base.add(t.pnl)
		perBookBase[t.book].add(t.pnl)
		btcRegimeAligned := (t.dir == "long" && c.btcBull) || (t.dir == "short" && !c.btcBull)
		if c.coinAligned {
			coinF.add(t.pnl)
			perBookCoin[t.book].add(t.pnl)
		} else {
			dropCoin.add(t.pnl)
		}
		if btcRegimeAligned {
			btcF.add(t.pnl)
		} else {
			dropBtc.add(t.pnl)
		}
		if c.coinAligned && btcRegimeAligned {
			both.add(t.pnl)
		}
	}

	fmt.Printf("ema200bt | 書=%s | EMA200@%s | 覆蓋 %d/%d 筆",
		*booksArg, *bar, covered, len(trades))
	if len(missing) > 0 {
		ms := make([]string, 0, len(missing))
		for m := range missing {
			ms = append(ms, m)
		}
		sort.Strings(ms)
		fmt.Printf(" | 無資料幣:%s", strings.Join(ms, ","))
	}
	fmt.Println()
	fmt.Println(strings.Repeat("=", 70))
	fmt.Println("整體(相同交易,套不同濾網):")
	base.line("① 原始(無濾網)")
	coinF.line("② 幣自身>EMA200 同向")
	btcF.line("③ BTC 大盤 EMA200 regime")
	both.line("④ 兩者都要")
	fmt.Println("\n被濾掉的單(edge 來源 = 這些是不是虧的):")
	dropCoin.line("  幣逆EMA200 被丟掉")
	dropBtc.line("  逆大盤regime 被丟掉")
	fmt.Println("\n分書(原始 → 只套②幣自身EMA200濾網):")
	books := make([]string, 0, len(perBookBase))
	for b := range perBookBase {
		books = append(books, b)
	}
	sort.Strings(books)
	for _, b := range books {
		fmt.Printf("  %-9s 原始 ", b)
		perBookBase[b].line("")
		fmt.Printf("  %-9s 濾後 ", b)
		perBookCoin[b].line("")
	}
	fmt.Println("\n判讀: ② 合計 > ① 且『幣逆EMA200被丟掉』為負 → 幣自身EMA200濾網有效")
	fmt.Println("      ③ 若把賺錢單丟掉(『逆大盤regime被丟掉』為正) → BTC regime 濾網反而有害")
}
