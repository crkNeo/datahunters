// Command meanbt isolates what drags the meanrev (乖離回歸) book: the EXIT, not the
// entry. Entries are the UNCHANGED live meanRevSignal (SL 3ATR, TP=EMA20, EMA200
// trend gate, 10% maxSL). For each entry we replay several exit engines on the same
// OKX candles:
//
//	單目標      — barrier to TP=EMA20 (+overshoot) or SL, 24-bar timeout.
//	分批梯      — the live multi-TP ladder (tpMeanRevFront 60/25/15 @0.45/0.60,
//	              TP1→保本, TP2→鎖TP1), faithfully replayed.
//
// Notional round-trip fee is charged once/trade (leg fractions sum to 1 ⇒ same fee).
// Conservative SL-first per bar (matches microRun). Relative tool only.
//
//	go run ./cmd/meanbt
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"datahunter/internal/exchange"
)

var coins = []string{
	"BTC", "ETH", "SOL", "BNB", "XRP", "ADA", "AVAX", "SUI", "LTC", "DOT",
	"TRX", "NEAR", "APT", "ATOM", "TON", "LINK", "UNI", "AAVE", "ENA", "INJ",
	"DOGE", "SHIB", "PEPE", "WIF", "WLD", "FET", "ARB", "OP", "TIA", "SEI",
	"FIL", "ICP", "BCH", "ETC", "RUNE", "GALA", "SAND", "AXS", "LDO", "JUP",
}

// ---- indicators (faithful copies of internal/cache) ----

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

func atrSeries(cs []exchange.Candle, p int) []float64 {
	n := len(cs)
	out := make([]float64, n)
	if n < p+1 {
		return out
	}
	tr := func(i int) float64 {
		v := cs[i].High - cs[i].Low
		if d := math.Abs(cs[i].High - cs[i-1].Close); d > v {
			v = d
		}
		if d := math.Abs(cs[i].Low - cs[i-1].Close); d > v {
			v = d
		}
		return v
	}
	var sum float64
	for i := 1; i <= p; i++ {
		sum += tr(i)
	}
	atr := sum / float64(p)
	out[p] = atr
	for i := p + 1; i < n; i++ {
		atr = (atr*float64(p-1) + tr(i)) / float64(p)
		out[i] = atr
	}
	return out
}

func pct(a, b float64) float64 {
	if a == 0 {
		return 0
	}
	return (b - a) / a * 100
}

// signed pnl% of a long/short move entry→px.
func pnlPct(long bool, entry, px float64) float64 {
	if long {
		return pct(entry, px)
	}
	return -pct(entry, px)
}

func okxCandles(coin string, pages int) []exchange.Candle {
	inst := coin + "-USDT-SWAP"
	seen := map[int64]bool{}
	var all []exchange.Candle
	after := ""
	for p := 0; p < pages; p++ {
		url := fmt.Sprintf("https://www.okx.com/api/v5/market/candles?instId=%s&bar=1H&limit=300", inst)
		if after != "" {
			url += "&after=" + after
		}
		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("User-Agent", "Mozilla/5.0")
		resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
		if err != nil {
			break
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var env struct {
			Code string     `json:"code"`
			Data [][]string `json:"data"`
		}
		if json.Unmarshal(body, &env) != nil || env.Code != "0" || len(env.Data) == 0 {
			break
		}
		var oldest int64
		for _, r := range env.Data {
			if len(r) < 6 {
				continue
			}
			ts, _ := strconv.ParseInt(r[0], 10, 64)
			if seen[ts] {
				continue
			}
			seen[ts] = true
			o, _ := strconv.ParseFloat(r[1], 64)
			h, _ := strconv.ParseFloat(r[2], 64)
			l, _ := strconv.ParseFloat(r[3], 64)
			c, _ := strconv.ParseFloat(r[4], 64)
			v, _ := strconv.ParseFloat(r[5], 64)
			all = append(all, exchange.Candle{Ts: ts, Open: o, High: h, Low: l, Close: c, Volume: v})
			if oldest == 0 || ts < oldest {
				oldest = ts
			}
		}
		after = strconv.FormatInt(oldest, 10)
		time.Sleep(120 * time.Millisecond)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Ts < all[j].Ts })
	return all
}

// ---- exit engines ----

type ladder struct {
	a, b       float64 // TP1/TP2 placement as fraction of entry→TP3
	w1, w2, w3 float64
	beBuf      float64
	minSplit   float64
}

var meanRevFront = &ladder{a: 0.45, b: 0.60, w1: 0.60, w2: 0.25, w3: 0.15, beBuf: 0.0005, minSplit: 0.008}
var evenSplit = &ladder{a: 0.45, b: 0.60, w1: 0.40, w2: 0.30, w3: 0.30, beBuf: 0.0005, minSplit: 0.008}

// replay returns the net pnl% of one trade under an exit engine (lad==nil ⇒ single
// target). entry at kl[i].Close; tp3/sl are absolute prices; conservative SL-first.
// beAt>0 (ladder off only) is the STANDALONE 保本: once price travels beAt of the way
// entry→TP3, the stop jumps to entry×(1±beBuf). This is the admin `breakeven` mode.
func replay(kl []exchange.Candle, i, h int, long bool, entry, tp3, sl float64, lad *ladder, beAt float64) float64 {
	const beBuf = 0.0005
	end := i + h
	if end >= len(kl) {
		end = len(kl) - 1
	}
	reached := func(level, ext float64) bool {
		if level == 0 {
			return false
		}
		if long {
			return ext >= level
		}
		return ext <= level
	}
	// set up ladder legs
	var tp1, tp2 float64
	useLad := false
	if lad != nil {
		dist := tp3 - entry
		if math.Abs(dist)/entry >= lad.minSplit {
			tp1 = entry + lad.a*dist
			tp2 = entry + lad.b*dist
			useLad = true
		}
	}
	beLvl := 0.0
	if !useLad && beAt > 0 {
		beLvl = entry + beAt*(tp3-entry) // signed; long above, short below
	}
	beDone := false
	legs := 0
	filled, realized := 0.0, 0.0
	for j := i + 1; j <= end; j++ {
		bar := kl[j]
		adv := bar.Low
		fav := bar.High
		if !long {
			adv, fav = bar.High, bar.Low
		}
		// SL-first (conservative), using start-of-bar stop
		if (long && adv <= sl) || (!long && adv >= sl) {
			realized += (1 - filled) * pnlPct(long, entry, sl)
			return realized
		}
		// standalone 保本: once travelled beAt toward TP, move stop to break-even+
		if beLvl != 0 && !beDone && reached(beLvl, fav) {
			if long {
				sl = entry * (1 + beBuf)
			} else {
				sl = entry * (1 - beBuf)
			}
			beDone = true
		}
		if useLad {
			if legs < 1 && reached(tp1, fav) {
				realized += lad.w1 * pnlPct(long, entry, tp1)
				filled += lad.w1
				legs = 1
				if long { // TP1 → break-even+
					sl = entry * (1 + lad.beBuf)
				} else {
					sl = entry * (1 - lad.beBuf)
				}
			}
			if legs < 2 && reached(tp2, fav) {
				realized += lad.w2 * pnlPct(long, entry, tp2)
				filled += lad.w2
				legs = 2
				sl = tp1 // TP2 → lock stop at TP1
			}
		}
		if reached(tp3, fav) { // final target → close remainder
			realized += (1 - filled) * pnlPct(long, entry, tp3)
			return realized
		}
	}
	// timeout: close remainder at last close
	realized += (1 - filled) * pnlPct(long, entry, kl[end].Close)
	return realized
}

// half: 0 = first half of the window, 1 = second half (out-of-sample split).
type acc struct {
	n, wins          int
	sum, gWin, gLoss float64
	hn               [2]int
	hsum             [2]float64
}

func (a *acc) add(ret float64, half int) {
	a.n++
	a.sum += ret
	if ret > 0 {
		a.wins++
		a.gWin += ret
	} else {
		a.gLoss += -ret
	}
	a.hn[half]++
	a.hsum[half] += ret
}

// net expectancy per trade after fee.
func (a acc) net(fee float64) float64 {
	if a.n == 0 {
		return 0
	}
	return a.sum/float64(a.n) - fee
}
func (a acc) halfNet(h int, fee float64) float64 {
	if a.hn[h] == 0 {
		return 0
	}
	return a.hsum[h]/float64(a.hn[h]) - fee
}
func (a acc) pf() float64 {
	if a.gLoss <= 0 {
		return math.Inf(1)
	}
	return a.gWin / a.gLoss
}

// a raw trade, replayed later under every grid cell.
type trade struct {
	kl        []exchange.Candle
	i         int
	long      bool
	price, a  float64
	ema20, _x float64
	half      int
}

type cell struct {
	sl, tp, be float64
	full       acc
}

func main() {
	fee := flag.Float64("fee", 0.10, "round-trip fee+slippage %")
	pages := flag.Int("pages", 6, "OKX pages (×300 bars)")
	horizon := flag.Int("h", 24, "timeout bars")
	cooldown := flag.Int("cd", 4, "same-coin cooldown bars")
	maxSL := flag.Float64("maxsl", 10.0, "skip entries whose baseline SL%% exceeds this")
	longOnly := flag.Bool("long", true, "long-only (recommended); -long=false for 多空")
	flag.Parse()

	// ---- pass 1: load candles, collect entries, find window midpoint ----
	type coinData struct {
		kl                  []exchange.Candle
		ema20, ema200, atr14 []float64
	}
	var data []coinData
	var minTs, maxTs int64
	got := 0
	for _, coin := range coins {
		kl := okxCandles(coin, *pages)
		if len(kl) < 250 {
			continue
		}
		got++
		data = append(data, coinData{kl, emaSeries(kl, 20), emaSeries(kl, 200), atrSeries(kl, 14)})
		if minTs == 0 || kl[0].Ts < minTs {
			minTs = kl[0].Ts
		}
		if kl[len(kl)-1].Ts > maxTs {
			maxTs = kl[len(kl)-1].Ts
		}
	}
	midTs := (minTs + maxTs) / 2

	var trades []trade
	for _, d := range data {
		kl, ema20, ema200, atr := d.kl, d.ema20, d.ema200, d.atr14
		next := 0
		for i := 210; i < len(kl)-1; i++ {
			a := atr[i]
			if a <= 0 || ema200[i] <= 0 || i < next {
				continue
			}
			price := kl[i].Close
			dev := price - ema20[i]
			var long bool
			ok := false
			if price > ema200[i] && dev < -2.0*a {
				long, ok = true, true
			} else if price < ema200[i] && dev > 2.0*a {
				long, ok = false, true
			}
			if !ok {
				continue
			}
			if *longOnly && !long {
				continue
			}
			var baseSL float64 // baseline 10% maxSL filter (3ATR stop)
			if long {
				baseSL = price - 3.0*a
				if pct(baseSL, price) > *maxSL {
					continue
				}
			} else {
				baseSL = price + 3.0*a
				if pct(price, baseSL) > *maxSL {
					continue
				}
			}
			half := 0
			if kl[i].Ts >= midTs {
				half = 1
			}
			trades = append(trades, trade{kl, i, long, price, a, ema20[i], 0, half})
			next = i + *cooldown
		}
	}

	// ---- pass 2: grid scan SL × TP × 保本 ----
	sls := []float64{2.0, 2.5, 3.0}
	tps := []float64{0.0, 0.5, 1.0}
	bes := []float64{0.0, 0.60, 0.70, 0.80}
	var cells []cell
	for _, sl := range sls {
		for _, tp := range tps {
			for _, be := range bes {
				c := cell{sl: sl, tp: tp, be: be}
				for _, t := range trades {
					var slPx, tp3 float64
					if t.long {
						slPx = t.price - sl*t.a
						tp3 = t.ema20 + tp*t.a
					} else {
						slPx = t.price + sl*t.a
						tp3 = t.ema20 - tp*t.a
					}
					ret := replay(t.kl, t.i, *horizon, t.long, t.price, tp3, slPx, nil, be)
					c.full.add(ret, t.half)
				}
				cells = append(cells, c)
			}
		}
	}

	dir := "只做多"
	if !*longOnly {
		dir = "多空"
	}
	fmt.Printf("meanbt 網格 (乖離·%s) | OKX %d幣 | ~%d天 | 逾時%d根 | 冷卻%d根 | 費%.2f%%\n",
		dir, got, *pages*300/24, *horizon, *cooldown, *fee)
	fmt.Printf("進場 %d 筆 (前半 %d / 後半 %d, 以視窗中點切分)\n", len(trades), half0(trades), len(trades)-half0(trades))
	fmt.Println(strings.Repeat("=", 92))

	// sort by 穩健度 = 兩半較差者 (worst-half net), desc.
	beLbl := func(b float64) string {
		if b == 0 {
			return "關 "
		}
		return fmt.Sprintf("%.0f%%", b*100)
	}
	sort.Slice(cells, func(x, y int) bool {
		wx := math.Min(cells[x].full.halfNet(0, *fee), cells[x].full.halfNet(1, *fee))
		wy := math.Min(cells[y].full.halfNet(0, *fee), cells[y].full.halfNet(1, *fee))
		return wx > wy
	})
	fmt.Printf("%-4s %-5s %-6s %-4s %-6s %-5s %-8s %-8s %-8s %s\n",
		"SL", "TP", "保本", "n", "勝率", "PF", "淨期望", "前半", "後半", "穩健(較差半)")
	fmt.Println(strings.Repeat("-", 92))
	for _, c := range cells {
		f := c.full
		worst := math.Min(f.halfNet(0, *fee), f.halfNet(1, *fee))
		flag := ""
		if f.halfNet(0, *fee) > 0 && f.halfNet(1, *fee) > 0 {
			flag = " ✓兩半皆正"
		}
		fmt.Printf("%.1f  %.1f   %-6s n=%-3d %4.0f%%  %4.2f  %+6.3f%%  %+6.3f%%  %+6.3f%%  %+6.3f%%%s\n",
			c.sl, c.tp, beLbl(c.be), f.n, float64(f.wins)/float64(f.n)*100, f.pf(),
			f.net(*fee), f.halfNet(0, *fee), f.halfNet(1, *fee), worst, flag)
	}
	fmt.Println("\n判讀:")
	fmt.Println("  · 排序 = 穩健度(前/後半兩者較差的那個淨期望); 越上面越不挑行情")
	fmt.Println("  · ✓兩半皆正 = 前後兩段行情都賺 → 較可能是真 edge, 不是單段運氣")
	fmt.Println("  · 保本『關』通常淨期望最高但兩半波動大; 想要穩就找『兩半皆正且保本略開』那格")
	fmt.Println("  · -long=false 看多空全開; 預設只做多(前面驗過空側拖累)")
}

func half0(ts []trade) int {
	c := 0
	for _, t := range ts {
		if t.half == 0 {
			c++
		}
	}
	return c
}
