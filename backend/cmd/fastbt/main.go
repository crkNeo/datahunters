// Command fastbt backtests fast (15m) entry ideas on the burst population:
//  1) Pure short-term scalp: enter on a volume-burst breakout, exit after a
//     fixed SHORT hold (15m/30m/1h/2h) regardless of direction afterwards.
//  2) Two-timeframe: slow OI accumulation selects + sets direction, the 15m
//     breakout only times the entry.
// All returns are net of a round-trip fee/slippage estimate.
package main

import (
	"flag"
	"fmt"
	"strings"

	"datahunter/internal/exchange"
)

const bucket15 = 900000 // 15m in ms

var defaultCoins = []string{
	"BTC", "ETH", "SOL", "BNB", "XRP", "ADA", "AVAX", "SUI", "LTC", "DOT", "TRX",
	"NEAR", "APT", "ATOM", "TON", "ICP", "FIL", "SEI", "TIA", "BCH", "ARB", "OP",
	"LINK", "UNI", "AAVE", "ENA", "JUP", "INJ", "DOGE", "WIF", "TRUMP", "WLD",
	"FET", "ORDI", "PEOPLE", "JTO", "PYTH", "TNSR", "STRK", "W", "ETHFI", "PNUT",
	"ACT", "GOAT", "TURBO", "MEW", "POPCAT", "ZRO", "ALT", "OM", "ONDO", "PENDLE",
	"AR", "TAO", "RUNE", "ZK", "JASMY", "GALA", "SAND", "AXS", "HIVE",
}

type sample struct {
	volSpike  float64 // last 45m avg vol / last 12h avg vol
	upBar     bool    // current 15m bar closed up
	brokeHigh bool    // close broke the prior 8-bar (2h) high (breakout now)
	oiAccum   float64 // OI change over last 12h (slow accumulation)
	ret       [4]float64 // signed return at +1,+2,+4,+8 bars (15m,30m,1h,2h)
}

func pct(a, b float64) float64 {
	if a == 0 {
		return 0
	}
	return (b - a) / a * 100
}

func main() {
	fee := flag.Float64("fee", 0.10, "round-trip fee+slippage %")
	coinsCSV := flag.String("coins", strings.Join(defaultCoins, ","), "coins")
	flag.Parse()
	coins := strings.Split(*coinsCSV, ",")
	ex := exchange.NewClient()

	var s []sample
	for _, c := range coins {
		sym := c + "USDT"
		kl, err := ex.BinanceKlines(sym, "15m", 1000)
		if err != nil || len(kl) < 60 {
			continue
		}
		oi, _ := ex.BinanceOIHist(sym, "15m", 500)
		oiMap := map[int64]float64{}
		for _, p := range oi {
			oiMap[p.Ts/bucket15] = p.SumOIValue
		}
		for i := 48; i < len(kl)-8; i++ {
			var v3, v48 float64
			for j := i - 2; j <= i; j++ {
				v3 += kl[j].Volume
			}
			for j := i - 47; j <= i; j++ {
				v48 += kl[j].Volume
			}
			vs := 0.0
			if v48 > 0 {
				vs = (v3 / 3) / (v48 / 48)
			}
			ph := kl[i-1].High
			for j := i - 8; j < i; j++ {
				if kl[j].High > ph {
					ph = kl[j].High
				}
			}
			hb := kl[i].Ts / bucket15
			oiAccum := 0.0
			if a, ok := oiMap[hb-48]; ok && a > 0 {
				if b, ok2 := oiMap[hb]; ok2 {
					oiAccum = pct(a, b)
				}
			}
			var ret [4]float64
			for k, off := range []int{1, 2, 4, 8} {
				ret[k] = pct(kl[i].Close, kl[i+off].Close)
			}
			s = append(s, sample{
				volSpike:  vs,
				upBar:     kl[i].Close >= kl[i].Open,
				brokeHigh: kl[i].Close > ph,
				oiAccum:   oiAccum,
				ret:       ret,
			})
		}
		fmt.Printf("  %-6s %d bars\n", c, len(kl))
	}
	if len(s) == 0 {
		fmt.Println("no samples")
		return
	}

	holds := []string{"15m", "30m", "1h", "2h"}
	// helper: over a population, long return at hold k, net of fee
	stats := func(pop []sample, k int) (n int, win, exp float64) {
		if len(pop) == 0 {
			return
		}
		w := 0
		var sum float64
		for _, x := range pop {
			r := x.ret[k] - *fee
			sum += r
			if r > 0 {
				w++
			}
		}
		return len(pop), float64(w) / float64(len(pop)) * 100, sum / float64(len(pop))
	}
	filter := func(ok func(sample) bool) []sample {
		var out []sample
		for _, x := range s {
			if ok(x) {
				out = append(out, x)
			}
		}
		return out
	}

	// populations
	all := s
	burst := filter(func(x sample) bool { return x.volSpike >= 2.5 && x.upBar && x.brokeHigh })
	burstOI := filter(func(x sample) bool { return x.volSpike >= 2.5 && x.upBar && x.brokeHigh && x.oiAccum > 0 })

	fmt.Printf("\n=== 短線剝頭皮: 量爆突破當下進場(做多), 固定持有後出場 (扣 %.2f%% 手續費) ===\n", *fee)
	fmt.Printf("樣本: 全部=%d, 量爆突破=%d, 量爆突破+OI升=%d\n", len(all), len(burst), len(burstOI))
	report := func(name string, pop []sample) {
		fmt.Printf("\n%-18s\n", name)
		fmt.Printf("  %-8s %8s %8s %10s\n", "持有", "交易數", "勝率", "每筆淨報酬")
		for k := range holds {
			n, win, exp := stats(pop, k)
			fmt.Printf("  %-8s %8d %7.1f%% %+9.3f%%\n", holds[k], n, win, exp)
		}
	}
	report("基準(全市場隨機)", all)
	report("① 量爆突破(短線爆發)", burst)
	report("② 量爆突破 + OI升(雙框架)", burstOI)

	fmt.Println("\n勝率與每筆淨報酬都要 > 0/正;對照基準看訊號有沒有加值")
	fmt.Println("短線持有越短手續費佔比越重;扣費後仍正才有實戰意義")
}
