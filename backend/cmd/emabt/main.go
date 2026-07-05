// Command emabt backtests the EMA books. Two data sources:
//
//	-src okx      (default) OHLCV only → runs the standalone 純EMA book (Tag B)
//	              and a no-filter cross reference. Gamble books need OI/CVD (skip).
//	-src binance  full: also reconstructs the gamble ignition score and the
//	              狙擊+EMA book (Tag A). Needs Binance (rate-limited).
//
// Faithful approximation: live "15m 站穩 EMA200" ≈ "1h close vs 1h EMA50" (same
// ~50h lookback). No overlap per coin; fee netted out. Relative tool only.
//
//	go run ./cmd/emabt                 # OKX, Tag B now
//	go run ./cmd/emabt -src binance    # full, once Binance unbans
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
	"datahunter/internal/indicator"
)

const hourMs = 3600000

var coins = []string{
	"BTC", "ETH", "SOL", "BNB", "XRP", "ADA", "AVAX", "SUI", "LTC", "DOT",
	"TRX", "NEAR", "APT", "ATOM", "TON", "LINK", "UNI", "AAVE", "ENA", "INJ",
	"DOGE", "SHIB", "PEPE", "WIF", "WLD", "FET", "ARB", "OP", "TIA", "SEI",
	"FIL", "ICP", "BCH", "ETC", "RUNE", "GALA", "SAND", "AXS", "LDO", "JUP",
}

func pct(a, b float64) float64 {
	if a == 0 {
		return 0
	}
	return (b - a) / a * 100
}
func clampf(x, lo, hi float64) float64 {
	return math.Max(lo, math.Min(hi, x))
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

func earlyScore(volSpike, oiAccum, whale, accel, cvd, earliness float64) int {
	vp := clampf((volSpike-1)*6, 0, 40)
	oi := clampf(math.Abs(oiAccum)*1.8, 0, 30)
	wh := clampf((whale-1)*1.0, 0, 12)
	ac := clampf(math.Abs(accel)*1.0, 0, 12)
	cv := 0.0
	if (accel >= 0) == (cvd >= 0) {
		cv = clampf(math.Abs(cvd)*0.4, 0, 8)
	}
	return int(math.Round((vp + oi + wh + ac + cv) * earliness))
}

const tpMult, slMult = 0.618, 0.5

func entryLevels(kl []exchange.Candle, pump bool) (tp, sl float64) {
	n := len(kl)
	w := 12
	if n < w {
		w = n
	}
	hi, lo := kl[n-1].High, kl[n-1].Low
	for i := n - w; i < n; i++ {
		if kl[i].High > hi {
			hi = kl[i].High
		}
		if kl[i].Low < lo {
			lo = kl[i].Low
		}
	}
	rng := hi - lo
	cur := kl[n-1].Close
	if pump {
		return cur + tpMult*rng, cur - slMult*rng
	}
	return cur - tpMult*rng, cur + slMult*rng
}

func emaSeries(kl []exchange.Candle, p int) []float64 {
	out := make([]float64, len(kl))
	if len(kl) == 0 {
		return out
	}
	k := 2.0 / float64(p+1)
	out[0] = kl[0].Close
	for i := 1; i < len(kl); i++ {
		out[i] = kl[i].Close*k + out[i-1]*(1-k)
	}
	return out
}

func atr14(kl []exchange.Candle, end int) float64 {
	if end < 14 {
		return 0
	}
	var sum float64
	for i := end - 13; i <= end; i++ {
		tr := kl[i].High - kl[i].Low
		if d := math.Abs(kl[i].High - kl[i-1].Close); d > tr {
			tr = d
		}
		if d := math.Abs(kl[i].Low - kl[i-1].Close); d > tr {
			tr = d
		}
		sum += tr
	}
	return sum / 14
}

// swingTPSL sets SL to the 20-bar swing extreme before entry and TP to 1:1 of
// that risk (EMA-strategy rule). ok=false if there's no valid stop.
func swingTPSL(kl []exchange.Candle, i int, long bool, entry float64) (tp, sl float64, ok bool) {
	if i < 20 {
		return 0, 0, false
	}
	lo, hi := kl[i].Low, kl[i].High
	for j := i - 19; j <= i; j++ {
		if kl[j].Low < lo {
			lo = kl[j].Low
		}
		if kl[j].High > hi {
			hi = kl[j].High
		}
	}
	if long {
		risk := entry - lo
		if risk <= 0 {
			return 0, 0, false
		}
		return entry + risk, lo, true
	}
	risk := hi - entry
	if risk <= 0 {
		return 0, 0, false
	}
	return entry - risk, hi, true
}

func simExit(kl []exchange.Candle, i, h int, long bool, tp, sl float64) (float64, bool) {
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
				return slRet, false
			}
			if kl[j].High >= tp {
				return tpRet, true
			}
		} else {
			if kl[j].High >= sl {
				return slRet, false
			}
			if kl[j].Low <= tp {
				return tpRet, true
			}
		}
	}
	ret := pct(entry, kl[end].Close)
	if !long {
		ret = -ret
	}
	return ret, ret > 0
}

type acc struct {
	n, wins       int
	sum           float64
	longN, shortN int
}

func (a *acc) add(ret float64, win, long bool) {
	a.n++
	a.sum += ret
	if win {
		a.wins++
	}
	if long {
		a.longN++
	} else {
		a.shortN++
	}
}
func (a acc) line(name string, fee float64) {
	if a.n == 0 {
		fmt.Printf("  %-18s 無訊號\n", name)
		return
	}
	fmt.Printf("  %-18s n=%-5d 勝率 %5.1f%%   淨期望 %+6.3f%%   (多%d/空%d)\n",
		name, a.n, float64(a.wins)/float64(a.n)*100, a.sum/float64(a.n)-fee, a.longN, a.shortN)
}

func main() {
	src := flag.String("src", "okx", "okx | binance")
	gate := flag.Int("gate", 45, "ignition gate")
	fee := flag.Float64("fee", 0.10, "round-trip fee+slippage %")
	hG := flag.Int("hg", 24, "gamble/EMA-gamble horizon (h)")
	hE := flag.Int("he", 48, "pure-EMA horizon (h)")
	pages := flag.Int("pages", 5, "OKX pages (×300 bars)")
	flag.Parse()

	ex := exchange.NewClient()
	var gamble, gambleEMA, pureEMA, crossNoFilter, longB, shortB acc

	fmt.Printf("EMA 回測 | src=%s | %d 幣 | gate=%d | 狙擊%dh · EMA%dh | 費%.2f%%\n",
		*src, len(coins), *gate, *hG, *hE, *fee)
	fmt.Println(strings.Repeat("=", 74))

	got := 0
	for _, coin := range coins {
		var kl []exchange.Candle
		var oiMap map[int64]float64
		if *src == "binance" {
			k, err := ex.BinanceKlines(coin+"USDT", "1h", 1000)
			if err != nil || len(k) < 100 {
				continue
			}
			kl = k
			oiMap = map[int64]float64{}
			oi, _ := ex.BinanceOIHist(coin+"USDT", "1h", 500)
			for _, p := range oi {
				oiMap[p.Ts/hourMs] = p.SumOIValue
			}
			time.Sleep(400 * time.Millisecond) // throttle: avoid re-ban
		} else {
			kl = okxCandles(coin, *pages)
			if len(kl) < 100 {
				continue
			}
			oiMap = map[int64]float64{}
		}
		got++
		ema5 := emaSeries(kl, 5)
		ema20 := emaSeries(kl, 20)
		ema50 := emaSeries(kl, 50)
		gnext, genext, pnext := 0, 0, 0

		for i := 50; i < len(kl)-1; i++ {
			hb := kl[i].Ts / hourMs
			c := kl[i].Close
			// two latched flags: golden state (EMA5>EMA20) + price above EMA50.
			longTrend := ema5[i] > ema20[i] && c > ema50[i]
			shortTrend := ema5[i] < ema20[i] && c < ema50[i]
			// rising edge: the bar on which BOTH flags first hold (either order).
			prevLong := ema5[i-1] > ema20[i-1] && kl[i-1].Close > ema50[i-1]
			prevShort := ema5[i-1] < ema20[i-1] && kl[i-1].Close < ema50[i-1]
			justLong := longTrend && !prevLong
			justShort := shortTrend && !prevShort
			// reference only: fresh EMA5/20 cross, ignoring EMA50
			gold := ema5[i] > ema20[i] && ema5[i-1] <= ema20[i-1]
			dead := ema5[i] < ema20[i] && ema5[i-1] >= ema20[i-1]

			// gamble ignition (needs OI+CVD; Binance only)
			gambleOK := false
			var score int
			var pump bool
			if oiNow, ok1 := oiMap[hb]; ok1 {
				if oiPast, ok2 := oiMap[hb-12]; ok2 && oiPast > 0 {
					win := kl[i-47 : i+1]
					var recent, base float64
					for j := len(win) - 3; j < len(win); j++ {
						recent += win[j].Volume
					}
					recent /= 3
					for _, b := range win {
						base += b.Volume
					}
					base /= float64(len(win))
					volSpike := 0.0
					if base > 0 {
						volSpike = recent / base
					}
					var rs, bs float64
					var rc, bc int
					for j := len(win) - 3; j < len(win); j++ {
						if win[j].Trades > 0 {
							rs += win[j].QuoteVol / win[j].Trades
							rc++
						}
					}
					for j := 0; j < len(win); j++ {
						if win[j].Trades > 0 {
							bs += win[j].QuoteVol / win[j].Trades
							bc++
						}
					}
					whale := 1.0
					if rc > 0 && bc > 0 && bs > 0 {
						whale = (rs / float64(rc)) / (bs / float64(bc))
					}
					cvd := indicator.CVDFromKlines(win, 6)
					accel := pct(kl[i-3].Close, kl[i].Close)
					earliness := clampf(1-math.Abs(pct(kl[i-24].Close, kl[i].Close))/68, 0.3, 1)
					oiAccum := pct(oiPast, oiNow)
					score = earlyScore(volSpike, oiAccum, whale, accel, cvd, earliness)
					pump = oiAccum >= 0
					if math.Abs(oiAccum) < 1 {
						pump = cvd >= 0
					}
					gambleOK = true
				}
			}

			if gambleOK && score >= *gate && i >= gnext {
				tp, sl := entryLevels(kl[i-47:i+1], pump)
				ret, w := simExit(kl, i, *hG, pump, tp, sl)
				gamble.add(ret, w, pump)
				if end := i + *hG; end < len(kl) {
					gnext = end
				}
			}
			if gambleOK && score >= *gate && i >= genext &&
				((pump && longTrend) || (!pump && shortTrend)) {
				tp, sl := entryLevels(kl[i-47:i+1], pump)
				ret, w := simExit(kl, i, *hG, pump, tp, sl)
				gambleEMA.add(ret, w, pump)
				if end := i + *hG; end < len(kl) {
					genext = end
				}
			}

			// Tag B: both flags just became true (golden state + above EMA50)
			// exit: SL = 20-bar swing extreme before entry, TP = 1:1 of that risk
			dir := 0
			if justLong {
				dir = 1
			} else if justShort {
				dir = -1
			}
			if dir != 0 && i >= pnext {
				long := dir == 1
				if tp, sl, ok := swingTPSL(kl, i, long, c); ok {
					ret, w := simExit(kl, i, *hE, long, tp, sl)
					pureEMA.add(ret, w, long)
					if long {
						longB.add(ret, w, true)
					} else {
						shortB.add(ret, w, false)
					}
					if end := i + *hE; end < len(kl) {
						pnext = end
					}
				}
			}
			// reference: any fresh cross, no EMA50 side filter (same swing 1:1 exit)
			if gold || dead {
				long := gold
				if tp, sl, ok := swingTPSL(kl, i, long, c); ok {
					ret, w := simExit(kl, i, *hE, long, tp, sl)
					crossNoFilter.add(ret, w, long)
				}
			}
		}
	}

	fmt.Printf("成功載入 %d 幣\n", got)
	fmt.Println(strings.Repeat("=", 74))
	if *src == "binance" {
		gamble.line("純狙擊(基準)", *fee)
		gambleEMA.line("狙擊+EMA (A)", *fee)
		fmt.Println("  ↑ 同進場同出場, 唯一差別是 EMA 趨勢過濾 — 看 A 有沒有贏基準")
		fmt.Println()
	} else {
		fmt.Println("  純狙擊 / 狙擊+EMA(A): 需 Binance OI/CVD, 目前 IP 被限流, 待解封後 -src binance")
		fmt.Println()
	}
	pureEMA.line("純EMA (B)", *fee)
	longB.line("  └ B 只做多", *fee)
	shortB.line("  └ B 只做空", *fee)
	crossNoFilter.line("純交叉(無EMA200)", *fee)
	fmt.Println("  ↑ B 有加『站上/跌破 EMA50』; 對照『只看交叉』看過濾有沒有用")

	fmt.Println("\n判讀:")
	fmt.Println("  1. 純EMA(B) 淨期望為正 且 > 純交叉 → EMA50 旗標邏輯有用, Tag B 站得住")
	fmt.Println("  2. B 的多/空拆開看 — 空單常拖累(幣圈長多), 可能只留做多")
	fmt.Println("  3. 邏輯: 金叉旗 && 站上EMA50旗 同時成立那根進場(rising edge); OKX~50天; 無資金費率")
}
