// Command oicvdbt tests the OI/CVD diagnostic framework against real Binance data:
// does SPOT cvd beat CONTRACT cvd as a demand signal, do OI×price quadrants behave
// as the framework claims, and does adding spot/OI confirmation lift a CVD long
// signal's hit-rate? Uses futures + spot klines (both carry taker-buy) + OI history.
//
// Forward return is measured raw AND market-neutral (minus BTC over the same window)
// to strip beta. Diagnostic tool: bucketed forward returns, not a trade sim.
//
//	go run ./cmd/oicvdbt              # ~30 coins, 12h CVD window, 12h forward
package main

import (
	"flag"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"datahunter/internal/exchange"
)

var coins = []string{
	"BTC", "ETH", "SOL", "BNB", "XRP", "ADA", "AVAX", "SUI", "LTC", "DOT",
	"TRX", "NEAR", "APT", "LINK", "UNI", "AAVE", "ENA", "INJ", "DOGE", "WIF",
	"WLD", "FET", "ARB", "OP", "TIA", "SEI", "FIL", "BCH", "ETC", "LDO",
}

const hourMs = 3600000

func pct(a, b float64) float64 {
	if a == 0 {
		return 0
	}
	return (b - a) / a * 100
}

// rolling trailing-window CVD ratio (%) ending at each bar, keyed by Ts.
func cvdByTs(bars []exchange.Candle, window int) map[int64]float64 {
	out := make(map[int64]float64, len(bars))
	for i := range bars {
		lo := i - window + 1
		if lo < 0 {
			lo = 0
		}
		var net, tot float64
		for j := lo; j <= i; j++ {
			tot += bars[j].Volume
			net += 2*bars[j].TakerBuy - bars[j].Volume
		}
		if tot > 0 {
			out[bars[i].Ts] = net / tot * 100
		}
	}
	return out
}

type stat struct {
	n, win int
	sumRaw, sumRel float64
}

func (s *stat) add(raw, rel float64) {
	s.n++
	if raw > 0 {
		s.win++
	}
	s.sumRaw += raw
	s.sumRel += rel
}
func (s stat) line(label string) {
	if s.n == 0 {
		fmt.Printf("  %-28s n=0\n", label)
		return
	}
	fmt.Printf("  %-28s n=%-5d 勝率%5.1f%%   平均報酬%+6.2f%%   減BTC%+6.2f%%\n",
		label, s.n, 100*float64(s.win)/float64(s.n), s.sumRaw/float64(s.n), s.sumRel/float64(s.n))
}

func main() {
	win := flag.Int("cvdwin", 12, "CVD trailing window (bars)")
	fwd := flag.Int("fwd", 12, "forward horizon (bars)")
	flag.Parse()

	ex := exchange.NewClient()

	// BTC futures for the market-neutral baseline.
	btc, err := ex.BinanceKlines("BTCUSDT", "1h", 1000)
	if err != nil || len(btc) < 100 {
		fmt.Println("BTC klines failed:", err)
		return
	}
	btcClose := map[int64]float64{}
	for _, c := range btc {
		btcClose[c.Ts] = c.Close
	}
	btcFwd := func(ts int64) (float64, bool) {
		c0, ok0 := btcClose[ts]
		c1, ok1 := btcClose[ts+int64(*fwd)*hourMs]
		if !ok0 || !ok1 || c0 == 0 {
			return 0, false
		}
		return pct(c0, c1), true
	}

	// contract/spot CVD × sign buckets, OI×price quadrants, and the A/B filters.
	cCVD := map[string]*stat{"c≤-8": {}, "-8..-2": {}, "-2..2": {}, "2..8": {}, "c≥8": {}}
	sCVD := map[string]*stat{"s≤-8": {}, "-8..-2": {}, "-2..2": {}, "2..8": {}, "s≥8": {}}
	agree := map[string]*stat{"合約+ 現貨+": {}, "合約+ 現貨-": {}, "合約- 現貨+": {}, "合約- 現貨-": {}}
	oiPx := map[string]*stat{"價+OI+ 新多": {}, "價+OI- 軋空": {}, "價-OI+ 新空": {}, "價-OI- 出清": {}}
	// A/B: long trigger = contract CVD > +2 (mimics the score's CVD factor).
	ab := map[string]*stat{"純合約CVD>2": {}, "+現貨CVD>0確認": {}, "+OI 1h>0確認": {}, "+現貨且OI確認": {}}

	order := func(m map[string]*stat, keys []string) {
		for _, k := range keys {
			m[k].line(k)
		}
	}

	got, bars := 0, 0
	for _, coin := range coins {
		sym := coin + "USDT"
		fk, e1 := ex.BinanceKlines(sym, "1h", 1000)
		time.Sleep(250 * time.Millisecond)
		sk, e2 := ex.BinanceSpotKlines(sym, "1h", 1000)
		time.Sleep(250 * time.Millisecond)
		oi, e3 := ex.BinanceOIHist(sym, "1h", 500)
		time.Sleep(250 * time.Millisecond)
		if e1 != nil || e2 != nil || e3 != nil || len(fk) < 100 || len(sk) < 100 || len(oi) < 30 {
			continue
		}
		got++
		cc := cvdByTs(fk, *win)
		sc := cvdByTs(sk, *win)
		fClose := map[int64]float64{}
		for _, c := range fk {
			fClose[c.Ts] = c.Close
		}
		oiByTs := map[int64]float64{}
		for _, p := range oi {
			oiByTs[p.Ts/hourMs*hourMs] = p.SumOIValue
		}

		for i := *win; i < len(fk)-*fwd; i++ {
			ts := fk[i].Ts
			cv, ok1 := cc[ts]
			sv, ok2 := sc[ts]
			if !ok1 || !ok2 {
				continue
			}
			c1, okf := fClose[ts+int64(*fwd)*hourMs]
			if !okf {
				continue
			}
			raw := pct(fk[i].Close, c1)
			bf, okb := btcFwd(ts)
			if !okb {
				continue
			}
			rel := raw - bf
			bars++

			// contract CVD buckets
			cCVD[bucket(cv)].add(raw, rel)
			sCVD[bucketS(sv)].add(raw, rel)

			// CVD agreement 2x2 (only when both are meaningfully non-flat)
			if math.Abs(cv) > 2 && math.Abs(sv) > 2 {
				k := "合約" + sign(cv) + " 現貨" + sign(sv)
				agree[k].add(raw, rel)
			}

			// OI×price quadrant: price = this bar's move, OI = 1h change
			oiNow, okn := oiByTs[ts/hourMs*hourMs]
			oiPrev, okp := oiByTs[(ts-hourMs)/hourMs*hourMs]
			if okn && okp && oiPrev > 0 {
				oichg := pct(oiPrev, oiNow)
				pxchg := pct(fk[i].Open, fk[i].Close)
				if math.Abs(pxchg) > 0.1 && math.Abs(oichg) > 0.1 {
					var k string
					switch {
					case pxchg > 0 && oichg > 0:
						k = "價+OI+ 新多"
					case pxchg > 0 && oichg < 0:
						k = "價+OI- 軋空"
					case pxchg < 0 && oichg > 0:
						k = "價-OI+ 新空"
					default:
						k = "價-OI- 出清"
					}
					oiPx[k].add(raw, rel)
				}
			}

			// A/B long filters on the contract-CVD>2 trigger
			if cv > 2 {
				ab["純合約CVD>2"].add(raw, rel)
				spotOK := sv > 0
				oiOK := false
				if okn && okp && oiPrev > 0 {
					oiOK = pct(oiPrev, oiNow) > 0
				}
				if spotOK {
					ab["+現貨CVD>0確認"].add(raw, rel)
				}
				if oiOK {
					ab["+OI 1h>0確認"].add(raw, rel)
				}
				if spotOK && oiOK {
					ab["+現貨且OI確認"].add(raw, rel)
				}
			}
		}
	}

	fmt.Printf("oicvdbt | Binance %d/%d 幣 | CVD窗%d根 | 前瞻%d根 | 樣本 %d 根\n",
		got, len(coins), *win, *fwd, bars)
	fmt.Println(strings.Repeat("=", 84))
	fmt.Println("【A】合約 CVD 分桶 → 未來報酬 (框架#1: 合約 CVD 的預測力)")
	order(cCVD, []string{"c≤-8", "-8..-2", "-2..2", "2..8", "c≥8"})
	fmt.Println("\n【A'】現貨 CVD 分桶 → 未來報酬 (框架#1: 現貨 CVD 是否更會排序報酬)")
	order(sCVD, []string{"s≤-8", "-8..-2", "-2..2", "2..8", "s≥8"})
	fmt.Println("\n【B】合約×現貨 CVD 一致性 (框架#1 核心: 現貨確認 vs 只有槓桿推)")
	order(agree, []string{"合約+ 現貨+", "合約+ 現貨-", "合約- 現貨+", "合約- 現貨-"})
	fmt.Println("\n【C】OI×價格 象限 (框架#2: 軋空 vs 新多的延續性)")
	order(oiPx, []string{"價+OI+ 新多", "價+OI- 軋空", "價-OI+ 新空", "價-OI- 出清"})
	fmt.Println("\n【D】做多訊號 A/B: 合約CVD>2 加不加確認 (框架能否提升勝率)")
	order(ab, []string{"純合約CVD>2", "+現貨CVD>0確認", "+OI 1h>0確認", "+現貨且OI確認"})
	fmt.Println("\n判讀: 『減BTC』是市場中性報酬(去掉大盤beta); 勝率是 P(未來報酬>0)")
	_ = sort.Strings
}

func bucket(v float64) string {
	switch {
	case v <= -8:
		return "c≤-8"
	case v < -2:
		return "-8..-2"
	case v < 2:
		return "-2..2"
	case v < 8:
		return "2..8"
	default:
		return "c≥8"
	}
}
func bucketS(v float64) string {
	switch {
	case v <= -8:
		return "s≤-8"
	case v < -2:
		return "-8..-2"
	case v < 2:
		return "-2..2"
	case v < 8:
		return "2..8"
	default:
		return "s≥8"
	}
}
func sign(v float64) string {
	if v >= 0 {
		return "+"
	}
	return "-"
}
