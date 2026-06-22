// Command backtest replays the LIVE scorer (internal/scorer.ScoreDetail) over
// ~20 days of real Binance history to answer two questions:
//
//  1. Where should the long/short threshold sit? (threshold sweep)
//  2. Which factors actually predict forward returns? (per-factor correlation)
//
// Real inputs: 1h klines (incl. taker-buy volume for CVD), open-interest
// history, funding history, long/short history — the SAME computation as live.
package main

import (
	"flag"
	"fmt"
	"math"
	"sort"
	"strings"

	"datahunter/internal/exchange"
	"datahunter/internal/indicator"
	"datahunter/internal/scorer"
)

const hourMs = 3600000

var defaultCoins = []string{
	"BTC", "ETH", "SOL", "BNB", "XRP", "ADA", "AVAX", "SUI", "LTC",
	"DOT", "TRX", "NEAR", "APT", "ATOM", "TON", "ICP", "FIL", "SEI", "TIA", "BCH",
	"ARB", "OP", "LINK", "UNI", "AAVE", "ENA", "JUP", "INJ",
	"DOGE", "SHIB", "PEPE", "WIF", "TRUMP", "WLD", "FET", "ORDI",
}

type sample struct {
	score     int
	fwd       float64 // raw forward return %, +ve = price up
	factors   map[string]int
	in        scorer.DetailInput // raw inputs, for re-scoring variants
	normMom24 float64            // 24h momentum normalised by trailing volatility (z-units)
	vol24     float64            // trailing 24h quote (USDT) volume
	// candidate signals under evaluation
	topLong   float64 // top-trader long fraction (0..1); 0 = missing
	fundZ     float64 // funding rate z-score vs trailing history
	avgSizeZ  float64 // avg trade size z-score vs trailing bars
	premium   float64 // perp premium (basis)
	spotCVD   float64 // 12h spot taker-buy CVD ratio
	hasSpot   bool    // spot data available for this sample
}

// stdReturns: stddev of hourly % returns over the trailing window ending at i.
func stdReturns(kl []exchange.Candle, i, window int) float64 {
	lo := i - window + 1
	if lo < 1 {
		lo = 1
	}
	var rs []float64
	for k := lo; k <= i; k++ {
		rs = append(rs, pct(kl[k-1].Close, kl[k].Close))
	}
	if len(rs) < 2 {
		return 0
	}
	var m float64
	for _, r := range rs {
		m += r
	}
	m /= float64(len(rs))
	var v float64
	for _, r := range rs {
		v += (r - m) * (r - m)
	}
	return math.Sqrt(v / float64(len(rs)-1))
}

// avgSizeZ: z-score of the bar-i average trade size vs the trailing window.
func avgSizeZ(kl []exchange.Candle, i, window int) float64 {
	if kl[i].Trades <= 0 {
		return 0
	}
	cur := kl[i].QuoteVol / kl[i].Trades
	lo := i - window + 1
	if lo < 0 {
		lo = 0
	}
	var vals []float64
	for k := lo; k <= i; k++ {
		if kl[k].Trades > 0 {
			vals = append(vals, kl[k].QuoteVol/kl[k].Trades)
		}
	}
	if len(vals) < 2 {
		return 0
	}
	var m float64
	for _, x := range vals {
		m += x
	}
	m /= float64(len(vals))
	var v float64
	for _, x := range vals {
		v += (x - m) * (x - m)
	}
	if v == 0 {
		return 0
	}
	return (cur - m) / math.Sqrt(v/float64(len(vals)-1))
}

func pct(a, b float64) float64 {
	if a == 0 {
		return 0
	}
	return (b - a) / a * 100
}

func bucketize(pts []exchange.OIPoint) map[int64]float64 {
	m := map[int64]float64{}
	for _, p := range pts {
		m[p.Ts/hourMs] = p.SumOIValue
	}
	return m
}

func main() {
	horizon := flag.Int("horizon", 12, "forward horizon in hours")
	klimit := flag.Int("klines", 600, "1h klines per coin")
	feeRT := flag.Float64("fee", 0.10, "round-trip fee+slippage %, subtracted from expectancy")
	coinsCSV := flag.String("coins", strings.Join(defaultCoins, ","), "coins")
	flag.Parse()

	coins := strings.Split(*coinsCSV, ",")
	w := scorer.DefaultDetailWeights()
	ex := exchange.NewClient()

	// BTC 24h momentum series for relative strength, keyed by hour bucket.
	btcK, err := ex.BinanceKlines("BTCUSDT", "1h", *klimit)
	if err != nil {
		fmt.Println("failed to fetch BTC klines:", err)
		return
	}
	btcMom := map[int64]float64{}
	for i := 24; i < len(btcK); i++ {
		btcMom[btcK[i].Ts/hourMs] = pct(btcK[i-24].Close, btcK[i].Close)
	}

	var samples []sample
	for _, coin := range coins {
		sym := coin + "USDT"
		kl, err := ex.BinanceKlines(sym, "1h", *klimit)
		if err != nil || len(kl) < 50 {
			fmt.Printf("  %-6s skip (no klines)\n", coin)
			continue
		}
		oi, _ := ex.BinanceOIHist(sym, "1h", 500)
		fund, _ := ex.BinanceFundingHist(sym, 200)
		ls, _ := ex.BinanceLongShortHist(sym, "1h", 500)
		top, _ := ex.BinanceTopPositionHist(sym, "1h", 500)
		prem, _ := ex.BinancePremiumKlines(sym, "1h", *klimit)
		spotKl, _ := ex.BinanceSpotKlines(sym, "1h", *klimit)

		spotIdx := map[int64]int{} // hour bucket -> index in spotKl
		for j, c := range spotKl {
			spotIdx[c.Ts/hourMs] = j
		}

		oiMap := bucketize(oi)
		lsMap := map[int64]float64{}
		for _, p := range ls {
			lsMap[p.Ts/hourMs] = p.LongAccount
		}
		topMap := map[int64]float64{}
		for _, p := range top {
			topMap[p.Ts/hourMs] = p.LongAccount
		}
		premMap := map[int64]float64{}
		for _, p := range prem {
			premMap[p.Ts/hourMs] = p.Premium
		}
		// sorted funding times for lookup
		sort.Slice(fund, func(i, j int) bool { return fund[i].Ts < fund[j].Ts })
		lastFunding := func(ts int64) float64 {
			idx := sort.Search(len(fund), func(i int) bool { return fund[i].Ts > ts }) - 1
			if idx < 0 {
				return 0
			}
			return fund[idx].Rate
		}
		// funding z-score: how extreme is the current rate vs its trailing history
		fundingZ := func(ts int64, window int) float64 {
			idx := sort.Search(len(fund), func(i int) bool { return fund[i].Ts > ts }) - 1
			if idx < 1 {
				return 0
			}
			lo := idx - window + 1
			if lo < 0 {
				lo = 0
			}
			var m float64
			n := 0
			for k := lo; k <= idx; k++ {
				m += fund[k].Rate
				n++
			}
			m /= float64(n)
			var v float64
			for k := lo; k <= idx; k++ {
				v += (fund[k].Rate - m) * (fund[k].Rate - m)
			}
			if n < 2 || v == 0 {
				return 0
			}
			return (fund[idx].Rate - m) / math.Sqrt(v/float64(n-1))
		}

		used := 0
		for i := 24; i < len(kl)-*horizon; i++ {
			hb := kl[i].Ts / hourMs
			oiNow, ok1 := oiMap[hb]
			oiPrev, ok2 := oiMap[hb-1]
			longAcc, ok3 := lsMap[hb]
			if !ok1 || !ok2 || !ok3 || oiPrev == 0 {
				continue // require real OI + long/short for a clean sample
			}

			// trailing 24h quote (USDT) volume, for liquidity damping
			var vol24 float64
			for k := i - 23; k <= i; k++ {
				vol24 += kl[k].QuoteVol
			}
			structLabel, structDir := indicator.PriceStructure(kl[i-23 : i+1])
			in := scorer.DetailInput{
				Coin:        coin,
				OIChg1h:     pct(oiPrev, oiNow),
				CVDRatio:    indicator.CVDFromKlines(kl[:i+1], 12), // real taker-buy CVD, same as live
				StructLabel: structLabel,
				StructDir:   structDir,
				Mom1h:       pct(kl[i].Open, kl[i].Close),
				Mom24h:      pct(kl[i-24].Close, kl[i].Close),
				FundingRate: lastFunding(kl[i].Ts),
				LongAccount: longAcc,
				RelStrength: pct(kl[i-24].Close, kl[i].Close) - btcMom[hb],
				Vol24h:      vol24,
			}
			res := scorer.ScoreDetail(in, w)
			f := map[string]int{}
			for _, b := range res.Breakdown {
				f[b.Label] = b.Score
			}
			// 24h momentum normalised by trailing 24h-scale volatility (z-units)
			var normMom24 float64
			if sd := stdReturns(kl, i, 48); sd > 0 {
				normMom24 = in.Mom24h / (sd * math.Sqrt(24))
			}
			// spot CVD over the last 12h (real spot taker-buy), aligned by hour
			var spotCVD float64
			hasSpot := false
			if si, ok := spotIdx[hb]; ok && si >= 12 {
				spotCVD = indicator.CVDFromKlines(spotKl[:si+1], 12)
				hasSpot = true
			}
			samples = append(samples, sample{
				score:     res.Total,
				fwd:       pct(kl[i].Close, kl[i+*horizon].Close),
				factors:   f,
				in:        in,
				normMom24: normMom24,
				vol24:     vol24,
				topLong:   topMap[hb],
				fundZ:     fundingZ(kl[i].Ts, 30),
				avgSizeZ:  avgSizeZ(kl, i, 48),
				premium:   premMap[hb],
				spotCVD:   spotCVD,
				hasSpot:   hasSpot,
			})
			used++
		}
		fmt.Printf("  %-6s %d bars, %d samples\n", coin, len(kl), used)
	}

	report(samples, *horizon, *feeRT)
	variants(samples, *feeRT)
	liquidity(samples, *feeRT)
	candidates(samples)
}

func sgn(x float64) float64 {
	if x > 0 {
		return 1
	}
	if x < 0 {
		return -1
	}
	return 0
}
func clampAbs(x, c float64) float64 {
	if x > c {
		return c
	}
	if x < -c {
		return -c
	}
	return x
}

// corrIf: Pearson correlation of f(sample) vs forward return over valid samples.
func corrIf(s []sample, f func(sample) float64, ok func(sample) bool) float64 {
	var n, sx, sy, sxy, sx2, sy2 float64
	for _, x := range s {
		if !ok(x) {
			continue
		}
		xv, yv := f(x), x.fwd
		n++
		sx += xv
		sy += yv
		sxy += xv * yv
		sx2 += xv * xv
		sy2 += yv * yv
	}
	d := math.Sqrt((n*sx2 - sx*sx) * (n*sy2 - sy*sy))
	if d == 0 {
		return 0
	}
	return (n*sxy - sx*sy) / d
}

// candidates compares new candidate signals against forward returns so we can
// see which are worth integrating before touching the scorer. Signals are
// bullish-encoded: r > 0 means the signal predicts up-moves.
func candidates(s []sample) {
	all := func(sample) bool { return true }
	hasTop := func(x sample) bool { return x.topLong > 0 }

	fmt.Println("\n--- 候選訊號診斷 (signal vs 未來報酬 r; 多頭編碼, r>0=有預測力) ---")
	type c struct {
		name string
		fn   func(sample) float64
		ok   func(sample) bool
	}
	for _, cd := range []c{
		{"參考: 動能24H(raw)", func(x sample) float64 { return x.in.Mom24h }, all},
		{"參考: 市場結構(現用)", func(x sample) float64 { return float64(x.factors["市場結構"]) }, all},
		{"T1 OI×價(確認則跟, 否則淡)", func(x sample) float64 {
			c := 1.0
			if x.in.OIChg1h <= 0 {
				c = -0.5
			}
			return clampAbs(x.in.Mom24h, 10) * c
		}, all},
		{"T1 OI×價(僅OI升才計)", func(x sample) float64 {
			if x.in.OIChg1h <= 0 {
				return 0
			}
			return clampAbs(x.in.Mom24h, 10)
		}, all},
		{"T1 大戶跟單(top多-0.5)", func(x sample) float64 { return x.topLong - 0.5 }, hasTop},
		{"T1 散戶背離(top-retail)", func(x sample) float64 { return x.topLong - x.in.LongAccount }, hasTop},
		{"T2 資金費率z(逆勢,-z)", func(x sample) float64 { return -x.fundZ }, all},
		{"T2 鯨魚順勢(avgSizeZ×動能)", func(x sample) float64 { return x.avgSizeZ * sgn(x.in.Mom24h) }, all},
		{"T2 溢價(逆勢,-premium)", func(x sample) float64 { return -x.premium }, all},
		{"參考: 永續CVD(現用)", func(x sample) float64 { return x.in.CVDRatio }, func(x sample) bool { return x.hasSpot }},
		{"現貨 CVD", func(x sample) float64 { return x.spotCVD }, func(x sample) bool { return x.hasSpot }},
		{"現貨−永續 CVD 背離", func(x sample) float64 { return x.spotCVD - x.in.CVDRatio }, func(x sample) bool { return x.hasSpot }},
	} {
		fmt.Printf("  %-26s r = %+.3f\n", cd.name, corrIf(s, cd.fn, cd.ok))
	}

	// OI×price quadrant: avg forward return for each (24h momentum, OI 1h) combo
	fmt.Println("\n--- OI×價格 四象限 (24h動能方向 × OI 1h方向, 平均未來報酬) ---")
	type q struct {
		mUp, oUp bool
		name     string
	}
	for _, qq := range []q{
		{true, true, "價↑ OI↑ (新多單)"},
		{true, false, "價↑ OI↓ (空單回補)"},
		{false, true, "價↓ OI↑ (新空單)"},
		{false, false, "價↓ OI↓ (多單被清)"},
	} {
		var n int
		var sum float64
		for _, x := range s {
			if (x.in.Mom24h > 0) == qq.mUp && (x.in.OIChg1h > 0) == qq.oUp {
				n++
				sum += x.fwd
			}
		}
		if n > 0 {
			fmt.Printf("  %-18s n=%-5d 平均報酬 %+.3f%%\n", qq.name, n, sum/float64(n))
		}
	}

	// integration test: does adding a candidate to the total improve ±20 results?
	fmt.Println("\n--- 整合測試: baseline + 候選因子 (門檻±20, fee 0.10%) ---")
	fmt.Printf("%-22s %7s %7s %7s %9s\n", "設定", "r", "訊號", "命中", "淨期望")
	retailDiv := func(x sample) int {
		if x.topLong <= 0 {
			return 0
		}
		return iround(smoothSatF((x.topLong-x.in.LongAccount)*100, 8, 20))
	}
	prem := func(x sample) int { return iround(-smoothSatF(x.premium*1e4, 8, 3)) }
	spotF := func(x sample) int {
		if !x.hasSpot {
			return 0
		}
		return iround(smoothSatF(x.spotCVD, 12, 8)) // mirror the perp CVD factor
	}
	divF := func(x sample) int {
		if !x.hasSpot {
			return 0
		}
		return iround(smoothSatF(x.spotCVD-x.in.CVDRatio, 8, 10))
	}
	zero := func(sample) int { return 0 }
	prAll := func(name string, extra func(sample) int) {
		fn := func(x sample) float64 { return float64(x.score + extra(x)) }
		r, n, hit, net := metricsAt(s, fn, 20, 0.10)
		fmt.Printf("%-22s %+.3f %7d %6.1f%% %+8.3f%%\n", name, r, n, hit, net)
	}
	prAll("baseline", zero)
	prAll("+散戶背離", retailDiv)
	prAll("+溢價逆勢", prem)
	prAll("+現貨CVD", spotF)
	prAll("+現貨背離", divF)
}

func smoothSatF(x, mx, half float64) float64 {
	if half <= 0 {
		return 0
	}
	return mx * x / (math.Abs(x) + half)
}
func iround(f float64) int { return int(math.Round(f)) }

// liquidity (1) shows whether thin coins predict worse, and (2) tests a
// continuous damping factor min(1, vol24/ref) applied to the score.
func liquidity(s []sample, fee float64) {
	fmt.Println("\n--- 流動性診斷 (±20 訊號, 依 24h 成交量分桶) ---")
	fmt.Printf("%-12s %7s %7s %9s\n", "24h量", "訊號", "命中", "淨期望")
	type bk struct {
		lo, hi float64
		name   string
	}
	for _, b := range []bk{{0, 1e8, "<100M"}, {1e8, 5e8, "100-500M"}, {5e8, 2e9, "500M-2B"}, {2e9, 1e18, ">2B"}} {
		var n, wins int
		var g float64
		for _, x := range s {
			if (x.score >= 20 || x.score <= -20) && x.vol24 >= b.lo && x.vol24 < b.hi {
				n++
				sgn := 1.0
				if x.score < 0 {
					sgn = -1.0
				}
				r := x.fwd * sgn
				g += r
				if r > 0 {
					wins++
				}
			}
		}
		if n > 0 {
			fmt.Printf("%-12s %7d %6.1f%% %+8.3f%%\n", b.name, n, float64(wins)/float64(n)*100, g/float64(n)-fee)
		}
	}
	fmt.Println("(分數已內建 min(1, vol/100M) 流動性抑制)")
}

// variants re-scores every sample under candidate tweaks using the SAME live
// ScoreDetail, so we can see whether a change is worth applying before touching
// production. Compared at the ±20 entry threshold.
func variants(s []sample, fee float64) {
	fmt.Println("\n--- 變體比較 (門檻 ±20, 全部走線上 ScoreDetail) ---")
	fmt.Printf("%-26s %7s %7s %7s %9s\n", "設定", "r", "訊號", "命中", "淨期望")

	base := scorer.DefaultDetailWeights()
	wA := base
	wA.OIHalf = 1.5 // de-sensitise OI
	wB := base
	wB.Mom24hHalf = 1.0 // momentum in volatility z-units
	wAB := wA
	wAB.Mom24hHalf = 1.0

	score := func(w scorer.DetailWeights, normMom bool) func(sample) float64 {
		return func(x sample) float64 {
			in := x.in
			if normMom {
				in.Mom24h = x.normMom24
			}
			return float64(scorer.ScoreDetail(in, w).Total)
		}
	}
	print := func(name string, fn func(sample) float64) {
		r, n, hit, net := metricsAt(s, fn, 20, fee)
		fmt.Printf("%-26s %+.3f %7d %6.1f%% %+8.3f%%\n", name, r, n, hit, net)
	}
	print("目前 baseline", score(base, false))
	print("A: OIHalf 0.3→1.5", score(wA, false))
	print("B: 動能波動率正規化", score(wB, true))
	print("A+B", score(wAB, true))
}

func report(s []sample, horizon int, fee float64) {
	if len(s) == 0 {
		fmt.Println("\nno samples — OI/long-short history may be unavailable")
		return
	}
	fmt.Printf("\n=================  BACKTEST  (horizon=%dh, fee=%.2f%%, n=%d)  =================\n", horizon, fee, len(s))

	// overall: does total score predict forward return?
	fmt.Printf("\n總分 vs %dh 後報酬  相關係數 r = %+.3f  (>0 = 分數有預測力)\n", horizon, corr(s, func(x sample) float64 { return float64(x.score) }))

	// threshold sweep
	fmt.Println("\n--- 門檻掃描 (signed = 依分數方向的報酬, 已扣手續費) ---")
	fmt.Printf("%-6s %7s %7s %9s %9s\n", "門檻", "訊號數", "命中率", "毛期望", "淨期望")
	for _, th := range []int{8, 12, 16, 20, 24, 28, 32} {
		var n, wins int
		var gross float64
		for _, x := range s {
			if x.score >= th || x.score <= -th {
				n++
				sgn := 1.0
				if x.score < 0 {
					sgn = -1.0
				}
				r := x.fwd * sgn
				gross += r
				if r > 0 {
					wins++
				}
			}
		}
		if n == 0 {
			continue
		}
		avg := gross / float64(n)
		fmt.Printf("±%-5d %7d %6.1f%% %+8.3f%% %+8.3f%%\n", th, n, float64(wins)/float64(n)*100, avg, avg-fee)
	}

	// score buckets — must be monotonic if the score ranks well
	fmt.Println("\n--- |分數| 分桶 (檢查單調性: 分數越高報酬該越高) ---")
	fmt.Printf("%-10s %7s %7s %9s\n", "區間", "樣本", "命中率", "平均報酬")
	bounds := [][2]int{{0, 8}, {8, 16}, {16, 24}, {24, 32}, {32, 999}}
	for _, b := range bounds {
		var n, wins int
		var sum float64
		for _, x := range s {
			a := x.score
			if a < 0 {
				a = -a
			}
			if a >= b[0] && a < b[1] {
				n++
				sgn := 1.0
				if x.score < 0 {
					sgn = -1.0
				}
				r := x.fwd * sgn
				sum += r
				if r > 0 {
					wins++
				}
			}
		}
		if n == 0 {
			continue
		}
		fmt.Printf("%-10s %7d %6.1f%% %+8.3f%%\n", fmt.Sprintf("%d-%d", b[0], b[1]), n, float64(wins)/float64(n)*100, sum/float64(n))
	}

	// per-factor predictiveness
	fmt.Println("\n--- 各因子預測力 (factor 分數 vs 原始報酬 的相關係數) ---")
	fmt.Println("    r>0 = 該因子方向正確, 值越大越該加權; r≈0 或 <0 = 雜訊/反指標, 該降權")
	labels := []string{"市場結構", "價格結構", "動能 1H", "動能 24H", "資金費率", "多空比", "相對強弱"}
	for _, lab := range labels {
		l := lab
		fmt.Printf("  %-8s r = %+.3f\n", l, corr(s, func(x sample) float64 { return float64(x.factors[l]) }))
	}
	fmt.Println("\n注意: CVD 改用 K 線 taker-buy 量 (與線上一致), 12h 視窗。")

	// what-if: drop the two consistently negative factors
	fmt.Println("\n--- what-if: 移除「動能 1H」與「相對強弱」兩個反指標 ---")
	adj := func(x sample) float64 {
		return float64(x.score - x.factors["動能 1H"] - x.factors["相對強弱"])
	}
	br, bn, bhit, bnet := metricsAt(s, func(x sample) float64 { return float64(x.score) }, 20, fee)
	vr, vn, vhit, vnet := metricsAt(s, adj, 20, fee)
	fmt.Printf("  目前   : r=%+.3f  ±20訊號=%d  命中=%.1f%%  淨期望=%+.3f%%\n", br, bn, bhit, bnet)
	fmt.Printf("  移除後 : r=%+.3f  ±20訊號=%d  命中=%.1f%%  淨期望=%+.3f%%\n", vr, vn, vhit, vnet)
}

// metricsAt returns correlation plus ±threshold signal stats for a score fn.
func metricsAt(s []sample, fn func(sample) float64, th int, fee float64) (r float64, n int, hit, net float64) {
	r = corr(s, fn)
	var wins int
	var gross float64
	for _, x := range s {
		sc := fn(x)
		if sc >= float64(th) || sc <= -float64(th) {
			n++
			sgn := 1.0
			if sc < 0 {
				sgn = -1.0
			}
			rr := x.fwd * sgn
			gross += rr
			if rr > 0 {
				wins++
			}
		}
	}
	if n > 0 {
		hit = float64(wins) / float64(n) * 100
		net = gross/float64(n) - fee
	}
	return
}

// corr: Pearson correlation between f(sample) and raw forward return.
func corr(s []sample, f func(sample) float64) float64 {
	var n, sx, sy, sxy, sx2, sy2 float64
	for _, x := range s {
		xv, yv := f(x), x.fwd
		n++
		sx += xv
		sy += yv
		sxy += xv * yv
		sx2 += xv * xv
		sy2 += yv * yv
	}
	d := math.Sqrt((n*sx2 - sx*sx) * (n*sy2 - sy*sy))
	if d == 0 {
		return 0
	}
	return (n*sxy - sx*sy) / d
}
