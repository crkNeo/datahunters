// Command microbt A/B-tests exit-policy tweaks for the bollfade / meanrev books.
//
// Entries are the UNCHANGED live signals (bollFadeSignal / meanRevSignal, incl.
// the baseline RR gate and the 10% maxSL filter). For each entry we then replay
// several EXIT policies on the same OKX candles — varying the SL ATR-multiple and
// pushing the TP past the mean — and report win%, profit factor and net
// expectancy. Same entry set across policies ⇒ a clean isolation of the exit fix.
//
// Single-target barrier exit (no partial ladder), 24-bar timeout, fee netted.
// Relative tool only — absolute numbers differ from the live partial-TP engine.
//
//	go run ./cmd/microbt            # OKX ~40 coins, 6 pages (~75 days)
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

func smaSeries(cs []exchange.Candle, p int) []float64 {
	n := len(cs)
	out := make([]float64, n)
	if n < p {
		return out
	}
	var sum float64
	for i := 0; i < n; i++ {
		sum += cs[i].Close
		if i >= p {
			sum -= cs[i-p].Close
		}
		if i >= p-1 {
			out[i] = sum / float64(p)
		}
	}
	return out
}

func stdevSeries(cs []exchange.Candle, p int) []float64 {
	n := len(cs)
	out := make([]float64, n)
	if n < p {
		return out
	}
	for i := p - 1; i < n; i++ {
		var m float64
		for j := i - p + 1; j <= i; j++ {
			m += cs[j].Close
		}
		m /= float64(p)
		var v float64
		for j := i - p + 1; j <= i; j++ {
			d := cs[j].Close - m
			v += d * d
		}
		out[i] = math.Sqrt(v / float64(p))
	}
	return out
}

// atrSeries — Wilder smoothing, matching internal/cache/indicators.go.
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

// okxCandles paginates OKX 1H candles back `pages`×300 bars, oldest→newest.
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

// an entry the backtester replays exit policies over.
type entry struct {
	i     int     // bar index of entry (enter at close)
	long  bool
	price float64
	mid   float64 // bollfade mid band / meanrev EMA20 target reference
	std   float64 // bollfade 20-bar stdev (for pushing TP past mid)
	atr   float64 // ATR(14) at entry
}

// exit policy: SL = entry ± slATR*atr; TP = mean ± tpPush (units depend on book).
type policy struct {
	name   string
	slATR  float64
	tpPush float64 // bollfade: multiples of std past mid; meanrev: multiples of atr past ema20
}

type acc struct {
	n, wins            int
	sum, gWin, gLoss   float64
	longN, shortN      int
	sumL, sumS         float64
}

func (a *acc) add(ret float64, long bool) {
	a.n++
	a.sum += ret
	if ret > 0 {
		a.wins++
		a.gWin += ret
	} else {
		a.gLoss += -ret
	}
	if long {
		a.longN++
		a.sumL += ret
	} else {
		a.shortN++
		a.sumS += ret
	}
}

func (a acc) line(name string, fee float64) {
	if a.n == 0 {
		fmt.Printf("  %-22s 無訊號\n", name)
		return
	}
	pf := math.Inf(1)
	if a.gLoss > 0 {
		pf = a.gWin / a.gLoss
	}
	fmt.Printf("  %-22s n=%-4d 勝率%5.1f%%  PF %4.2f  淨期望%+6.3f%%  合計%+7.1f  (多%+.0f/空%+.0f)\n",
		name, a.n, float64(a.wins)/float64(a.n)*100, pf, a.sum/float64(a.n)-fee, a.sum-fee*float64(a.n),
		a.sumL, a.sumS)
}

// simExit — single-target barrier with h-bar timeout. Conservative: same-bar SL
// wins over TP (matches microRun SL-first ordering).
func simExit(kl []exchange.Candle, i, h int, long bool, tp, sl float64) float64 {
	entry := kl[i].Close
	end := i + h
	if end >= len(kl) {
		end = len(kl) - 1
	}
	var tpRet, slRet float64
	if long {
		tpRet, slRet = pct(entry, tp), pct(entry, sl)
	} else {
		tpRet, slRet = pct(tp, entry), pct(sl, entry)
	}
	for j := i + 1; j <= end; j++ {
		if long {
			if kl[j].Low <= sl {
				return slRet
			}
			if kl[j].High >= tp {
				return tpRet
			}
		} else {
			if kl[j].High >= sl {
				return slRet
			}
			if kl[j].Low <= tp {
				return tpRet
			}
		}
	}
	ret := pct(entry, kl[end].Close)
	if !long {
		ret = -ret
	}
	return ret
}

func main() {
	fee := flag.Float64("fee", 0.10, "round-trip fee+slippage %")
	pages := flag.Int("pages", 6, "OKX pages (×300 bars)")
	horizon := flag.Int("h", 24, "timeout bars")
	cooldown := flag.Int("cd", 4, "same-coin cooldown bars")
	maxSL := flag.Float64("maxsl", 10.0, "skip entries whose baseline SL%% exceeds this")
	flag.Parse()

	bollPolicies := []policy{
		{"B0 基準 SL2.5·TP中軌", 2.5, 0.0},
		{"B1 SL2.0", 2.0, 0.0},
		{"B2 SL1.5", 1.5, 0.0},
		{"B3 TP過中軌0.5σ", 2.5, 0.5},
		{"B4 TP過中軌1.0σ", 2.5, 1.0},
		{"B5 SL2.0+TP0.5σ", 2.0, 0.5},
		{"B6 SL1.5+TP1.0σ", 1.5, 1.0},
	}
	meanPolicies := []policy{
		{"M0 基準 SL3.0·TP EMA20", 3.0, 0.0},
		{"M1 SL2.5", 2.5, 0.0},
		{"M2 SL2.0", 2.0, 0.0},
		{"M3 TP過EMA20 0.5ATR", 3.0, 0.5},
		{"M4 TP過EMA20 1.0ATR", 3.0, 1.0},
		{"M5 SL2.5+TP0.5ATR", 2.5, 0.5},
		{"M6 SL2.0+TP1.0ATR", 2.0, 1.0},
	}

	bollAcc := make([]acc, len(bollPolicies))
	meanAcc := make([]acc, len(meanPolicies))
	var bollEntries, meanEntries int

	fmt.Printf("microbt | OKX %d幣 | %d頁(~%d天) | 逾時%d根 | 冷卻%d根 | maxSL%.0f%% | 費%.2f%%\n",
		len(coins), *pages, *pages*300/24, *horizon, *cooldown, *maxSL, *fee)
	fmt.Println(strings.Repeat("=", 96))

	got := 0
	for _, coin := range coins {
		kl := okxCandles(coin, *pages)
		if len(kl) < 250 {
			continue
		}
		got++
		n := len(kl)
		sma20 := smaSeries(kl, 20)
		std20 := stdevSeries(kl, 20)
		ema20 := emaSeries(kl, 20)
		ema200 := emaSeries(kl, 200)
		atr := atrSeries(kl, 14)

		bollNext, meanNext := 0, 0
		for i := 210; i < n-1; i++ {
			a := atr[i]
			if a <= 0 || ema200[i] <= 0 {
				continue
			}
			price, prev, mid := kl[i].Close, kl[i-1].Close, sma20[i]

			// ---- bollfade entry (baseline, unchanged) ----
			if i >= bollNext && std20[i] > 0 && std20[i-1] > 0 {
				upPrev, loPrev := sma20[i-1]+2*std20[i-1], sma20[i-1]-2*std20[i-1]
				upNow, loNow := mid+2*std20[i], mid-2*std20[i]
				var e *entry
				if prev > upPrev && price <= upNow && price < ema200[i] && mid < price {
					s := price + 2.5*a
					if rr := (price - mid) / (s - price); rr >= 0.4 && rr <= 3.0 {
						if pct(price, s) <= *maxSL { // SL% within cap (short: s>price)
							e = &entry{i, false, price, mid, std20[i], a}
						}
					}
				} else if prev < loPrev && price >= loNow && price > ema200[i] && mid > price {
					s := price - 2.5*a
					if rr := (mid - price) / (price - s); rr >= 0.4 && rr <= 3.0 {
						if pct(s, price) <= *maxSL { // long SL%: (price-s)/price
							e = &entry{i, true, price, mid, std20[i], a}
						}
					}
				}
				if e != nil {
					bollEntries++
					for pi, p := range bollPolicies {
						var tp, sl float64
						if e.long {
							sl = e.price - p.slATR*e.atr
							tp = e.mid + p.tpPush*e.std // long: target mid, push further up
						} else {
							sl = e.price + p.slATR*e.atr
							tp = e.mid - p.tpPush*e.std
						}
						bollAcc[pi].add(simExit(kl, e.i, *horizon, e.long, tp, sl), e.long)
					}
					if end := i + *cooldown; end > bollNext {
						bollNext = end
					}
				}
			}

			// ---- meanrev entry (baseline, unchanged) ----
			if i >= meanNext {
				dev := price - ema20[i]
				var e *entry
				if price > ema200[i] && dev < -2.0*a {
					if pct(price-3.0*a, price) <= *maxSL {
						e = &entry{i, true, price, ema20[i], 0, a}
					}
				} else if price < ema200[i] && dev > 2.0*a {
					if pct(price, price+3.0*a) <= *maxSL {
						e = &entry{i, false, price, ema20[i], 0, a}
					}
				}
				if e != nil {
					meanEntries++
					for pi, p := range meanPolicies {
						var tp, sl float64
						if e.long {
							sl = e.price - p.slATR*e.atr
							tp = e.mid + p.tpPush*e.atr // overshoot past EMA20
						} else {
							sl = e.price + p.slATR*e.atr
							tp = e.mid - p.tpPush*e.atr
						}
						meanAcc[pi].add(simExit(kl, e.i, *horizon, e.long, tp, sl), e.long)
					}
					if end := i + *cooldown; end > meanNext {
						meanNext = end
					}
				}
			}
		}
	}

	fmt.Printf("成功載入 %d 幣  |  bollfade 進場 %d 筆  ·  meanrev 進場 %d 筆\n", got, bollEntries, meanEntries)
	fmt.Println(strings.Repeat("=", 96))
	fmt.Println("【bollfade 布林重回】相同進場, 不同出場:")
	for pi, p := range bollPolicies {
		bollAcc[pi].line(p.name, *fee)
	}
	fmt.Println()
	fmt.Println("【meanrev 乖離回歸】相同進場, 不同出場:")
	for pi, p := range meanPolicies {
		meanAcc[pi].line(p.name, *fee)
	}
	fmt.Println("\n判讀: 淨期望與 PF 同時 > 基準(B0/M0) 才算改善; 合計 = Σret−費. 單目標無分批, 相對比較用.")
}
