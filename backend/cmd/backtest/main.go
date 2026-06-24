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
	"math/rand"
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
	// explosive-move (pump-ahead) signals + outcome
	fwdMaxUp   float64 // max upward move over the forward horizon, %
	fwdMaxDn   float64 // max drawdown over the forward horizon, % (positive)
	squeeze    float64 // prior range / recent range (>1 = compressed/coiled)
	volSpike   float64 // recent 3h vol / 48h baseline
	oiAccum    float64 // 12h OI change %
	whaleZ     float64 // avg trade size z-score
	whaleRatio float64 // recent avg trade size / baseline (radar-style)
	fundLvl    float64 // |funding| %, leverage extremity
	accel3     float64 // last 3h price change % (radar nudge)
	cvd6       float64 // last 6h taker-buy CVD %
	chg24      float64 // 24h change % (for earliness weight)
	ts         int64   // bar open time, for train/test split
	atrPct     float64 // ATR(14) as % of price
	atrExp     float64 // ATR(6) / ATR(28) — volatility expansion
	htf12      float64 // 12h price change % (higher-timeframe trend)
	breakoutPos float64 // close position within last-24h range (0..1)
	// new untested indicators
	oiAccel  float64 // OI 1h-change acceleration (2nd diff of OI%, +ve = building faster)
	cvdSlope float64 // recent 6h CVD minus prior 6h CVD (buy-pressure slope)
	oiVol    float64 // OI notional / 24h turnover (leverage-density proxy)
}

// simExit walks the forward bars to find which of TP/SL is touched first, with
// levels set at entry ± mult×R (R = recent swing range). Returns the trade
// return % and whether it won. Ambiguous bars (range spans both) count as SL.
func simExit(kl []exchange.Candle, i, h int, entry, R float64, pump bool, tpMult, slMult float64) (float64, bool) {
	if entry <= 0 || R <= 0 {
		return 0, false
	}
	var tp, sl float64
	if pump {
		tp, sl = entry+tpMult*R, entry-slMult*R
	} else {
		tp, sl = entry-tpMult*R, entry+slMult*R
	}
	tpRet := tpMult * R / entry * 100
	slRet := -slMult * R / entry * 100
	end := i + h
	if end >= len(kl) {
		end = len(kl) - 1
	}
	for j := i + 1; j <= end; j++ {
		if pump {
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
	r := (kl[end].Close - entry) / entry * 100 // time exit at horizon
	if !pump {
		r = -r
	}
	return r, r > 0
}

type exitCfg struct {
	name   string
	tp, sl float64
}

var exitCfgs = []exitCfg{
	{"現用 TP1.382/SL1.0", 1.382, 1.0},
	{"TP1.0/SL1.0", 1.0, 1.0},
	{"TP1.0/SL0.618", 1.0, 0.618},
	{"TP1.0/SL0.5", 1.0, 0.5},
	{"TP0.75/SL0.5", 0.75, 0.5},
	{"TP0.618/SL0.5", 0.618, 0.5},
	{"TP0.5/SL0.5", 0.5, 0.5},
	{"TP0.5/SL0.382", 0.5, 0.382},
	{"TP1.382/SL0.618", 1.382, 0.618},
	{"TP2.0/SL1.0", 2.0, 1.0},
}

type exitAcc struct {
	n, wins int
	sum     float64
}

// simBreakout: don't predict direction — wait for the price to break the recent
// range within `window` bars, then enter in the break direction and apply the
// same TP/SL. Returns (return%, win, traded).
func simBreakout(kl []exchange.Candle, i, window, h int, R, tp, sl float64) (float64, bool, bool) {
	n := len(kl)
	hi, lo := kl[i].High, kl[i].Low
	for j := i - 11; j <= i; j++ {
		if kl[j].High > hi {
			hi = kl[j].High
		}
		if kl[j].Low < lo {
			lo = kl[j].Low
		}
	}
	for j := i + 1; j <= i+window && j < n; j++ {
		if kl[j].High >= hi { // up-break -> long, entry at the break level
			r, w := simExit(kl, j, h, hi, R, true, tp, sl)
			return r, w, true
		}
		if kl[j].Low <= lo { // down-break -> short
			r, w := simExit(kl, j, h, lo, R, false, tp, sl)
			return r, w, true
		}
	}
	return 0, false, false
}

// atrAvg returns the average true range over [end-n+1, end].
func atrAvg(kl []exchange.Candle, end, n int) float64 {
	lo := end - n + 1
	if lo < 1 {
		lo = 1
	}
	var sum float64
	c := 0
	for j := lo; j <= end; j++ {
		tr := kl[j].High - kl[j].Low
		if d := math.Abs(kl[j].High - kl[j-1].Close); d > tr {
			tr = d
		}
		if d := math.Abs(kl[j].Low - kl[j-1].Close); d > tr {
			tr = d
		}
		sum += tr
		c++
	}
	if c == 0 {
		return 0
	}
	return sum / float64(c)
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
	// BTC regime maps (+1 bull / -1 bear) keyed by hour bucket
	ema := make([]float64, len(btcK))
	kf := 2.0 / 51.0
	for i := range btcK {
		if i == 0 {
			ema[i] = btcK[i].Close
		} else {
			ema[i] = btcK[i].Close*kf + ema[i-1]*(1-kf)
		}
	}
	sgnf := func(x float64) int {
		if x > 0 {
			return 1
		}
		if x < 0 {
			return -1
		}
		return 0
	}
	reg24, reg48, regEma := map[int64]int{}, map[int64]int{}, map[int64]int{}
	for i := 48; i < len(btcK); i++ {
		hb := btcK[i].Ts / hourMs
		reg24[hb] = sgnf(pct(btcK[i-24].Close, btcK[i].Close))
		reg48[hb] = sgnf(pct(btcK[i-48].Close, btcK[i].Close))
		regEma[hb] = sgnf(btcK[i].Close - ema[i])
	}
	// Deribit DVOL (BTC forward implied-vol regime) over the sample window
	dvol := map[int64]float64{}
	if len(btcK) > 0 {
		if pts, err := ex.DeribitDVOL("BTC", btcK[0].Ts, btcK[len(btcK)-1].Ts+hourMs, 3600); err != nil {
			fmt.Println("DVOL fetch failed (skipping that test):", err)
		} else {
			for _, p := range pts {
				dvol[p.Ts/hourMs] = p.Value
			}
		}
	}

	var samples []sample
	exitGrid := make([]exitAcc, len(exitCfgs))
	var predAcc, breakAcc exitAcc // 預測方向 vs 突破追進
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

			// ---- explosive-move signals + forward max upside ----
			fwdMaxUp := 0.0
			hh := kl[i].Close
			for j := i + 1; j <= i+*horizon && j < len(kl); j++ {
				if kl[j].High > hh {
					hh = kl[j].High
				}
			}
			fwdMaxUp = pct(kl[i].Close, hh)
			ll := kl[i].Close
			for j := i + 1; j <= i+*horizon && j < len(kl); j++ {
				if kl[j].Low < ll {
					ll = kl[j].Low
				}
			}
			fwdMaxDn := -pct(kl[i].Close, ll) // positive = drawdown magnitude

			squeeze := 1.0
			if i >= 48 {
				rh, rl := kl[i].High, kl[i].Low
				for j := i - 7; j <= i; j++ {
					if kl[j].High > rh {
						rh = kl[j].High
					}
					if kl[j].Low < rl {
						rl = kl[j].Low
					}
				}
				ph, pl := kl[i-8].High, kl[i-8].Low
				for j := i - 47; j <= i-8; j++ {
					if kl[j].High > ph {
						ph = kl[j].High
					}
					if kl[j].Low < pl {
						pl = kl[j].Low
					}
				}
				recR := rh - rl
				priR := ph - pl
				if recR > 0 {
					squeeze = priR / recR // >1 = recent range tighter than history
				}
			}
			var v3, v48 float64
			for j := i - 2; j <= i; j++ {
				v3 += kl[j].Volume
			}
			for j := i - 47; j <= i; j++ {
				v48 += kl[j].Volume
			}
			volSpike := 0.0
			if v48 > 0 {
				volSpike = (v3 / 3) / (v48 / 48)
			}
			oiAccum := 0.0
			if v0, ok := oiMap[hb-12]; ok && v0 > 0 {
				oiAccum = pct(v0, oiNow)
			}
			// radar-style whale ratio: recent 3h avg trade size / 48h baseline
			var rsz, bsz float64
			var rcz, bcz int
			for j := i - 2; j <= i; j++ {
				if kl[j].Trades > 0 {
					rsz += kl[j].QuoteVol / kl[j].Trades
					rcz++
				}
			}
			for j := i - 47; j <= i; j++ {
				if kl[j].Trades > 0 {
					bsz += kl[j].QuoteVol / kl[j].Trades
					bcz++
				}
			}
			whaleRatio := 1.0
			if rcz > 0 && bcz > 0 && bsz > 0 {
				whaleRatio = (rsz / float64(rcz)) / (bsz / float64(bcz))
			}
			accel3 := pct(kl[i-3].Close, kl[i].Close)
			cvd6 := indicator.CVDFromKlines(kl[:i+1], 6)
			atr14 := atrAvg(kl, i, 14)
			atrPct := 0.0
			if kl[i].Close > 0 {
				atrPct = atr14 / kl[i].Close * 100
			}
			atrExp := 1.0
			if b := atrAvg(kl, i, 28); b > 0 {
				atrExp = atrAvg(kl, i, 6) / b
			}
			htf12 := pct(kl[i-12].Close, kl[i].Close)
			// OI acceleration: is OI building FASTER than the prior hour? (2nd diff)
			oiAccel := 0.0
			if p2, ok := oiMap[hb-2]; ok && p2 > 0 && oiPrev > 0 {
				oiAccel = pct(oiPrev, oiNow) - pct(p2, oiPrev)
			}
			// CVD slope: recent 6h taker-buy CVD vs the prior 6h (buying accelerating?)
			cvdSlope := 0.0
			if i >= 11 {
				cvdSlope = cvd6 - indicator.CVDFromKlines(kl[:i-5], 6)
			}
			// leverage-density proxy: OI notional relative to 24h turnover
			oiVol := 0.0
			if vol24 > 0 {
				oiVol = oiNow / vol24
			}
			bhi, blo := kl[i].High, kl[i].Low
			for j := i - 23; j <= i; j++ {
				if kl[j].High > bhi {
					bhi = kl[j].High
				}
				if kl[j].Low < blo {
					blo = kl[j].Low
				}
			}
			breakoutPos := 0.5
			if bhi > blo {
				breakoutPos = (kl[i].Close - blo) / (bhi - blo)
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
				fwdMaxUp:   fwdMaxUp,
				fwdMaxDn:   fwdMaxDn,
				squeeze:    squeeze,
				volSpike:   volSpike,
				oiAccum:    oiAccum,
				whaleZ:     avgSizeZ(kl, i, 48),
				whaleRatio: whaleRatio,
				fundLvl:    math.Abs(in.FundingRate * 100),
				accel3:     accel3,
				cvd6:       cvd6,
				chg24:      in.Mom24h,
				ts:         kl[i].Ts,
				atrPct:      atrPct,
				atrExp:      atrExp,
				htf12:       htf12,
				breakoutPos: breakoutPos,
				oiAccel:     oiAccel,
				cvdSlope:    cvdSlope,
				oiVol:       oiVol,
			})
			used++

			// exit-strategy grid: radar-grade signals (gate lowered to 45 for a
			// more stable estimate of the TP/SL geometry; it generalises to >=55)
			last := samples[len(samples)-1]
			if radarScore(defaultRW, last) >= 45 {
				pump := last.oiAccum >= 0
				if math.Abs(last.oiAccum) < 1 {
					pump = last.cvd6 >= 0
				}
				shi, slo := kl[i].High, kl[i].Low
				for j := i - 11; j <= i; j++ {
					if kl[j].High > shi {
						shi = kl[j].High
					}
					if kl[j].Low < slo {
						slo = kl[j].Low
					}
				}
				R := shi - slo
				for ci, cf := range exitCfgs {
					ret, win := simExit(kl, i, *horizon, kl[i].Close, R, pump, cf.tp, cf.sl)
					exitGrid[ci].n++
					exitGrid[ci].sum += ret
					if win {
						exitGrid[ci].wins++
					}
				}
				// entry-method comparison (both with TP0.618/SL0.5)
				pr, pw := simExit(kl, i, *horizon, kl[i].Close, R, pump, 0.618, 0.5)
				predAcc.n++
				predAcc.sum += pr
				if pw {
					predAcc.wins++
				}
				if br, bw, traded := simBreakout(kl, i, 8, *horizon, R, 0.618, 0.5); traded {
					breakAcc.n++
					breakAcc.sum += br
					if bw {
						breakAcc.wins++
					}
				}
			}
		}
		fmt.Printf("  %-6s %d bars, %d samples\n", coin, len(kl), used)
	}

	report(samples, *horizon, *feeRT)
	reportExit(exitGrid)
	fmt.Println("\n=== 進場法比較 (雷達訊號上, 同 TP0.618/SL0.5) ===")
	fmt.Printf("%-22s %7s %8s %10s\n", "進場法", "交易數", "勝率", "每筆期望")
	pe := func(name string, a exitAcc) {
		if a.n == 0 {
			return
		}
		fmt.Printf("%-22s %7d %7.1f%% %+9.3f%%\n", name, a.n, float64(a.wins)/float64(a.n)*100, a.sum/float64(a.n))
	}
	pe("預測方向(現用, 市價)", predAcc)
	pe("突破追進(等突破才進)", breakAcc)
	fmt.Println("突破法交易數較少(沒突破就不進);看每筆期望與勝率誰較優")
	variants(samples, *feeRT)
	liquidity(samples, *feeRT)
	candidates(samples)
	explosive(samples, *horizon)
	optimizeRadar(samples)
	directionTest(samples)
	optimizeScorer(samples, *feeRT)
	regimeTest(samples, map[string]map[int64]int{"BTC 24h趨勢": reg24, "BTC 48h趨勢": reg48, "BTC vs EMA50": regEma}, *feeRT)
	overlayTest(samples, *feeRT)
	qualityTest(samples, *feeRT)
	referenceTest(samples, *feeRT, dvol)
	oosTest(samples, *feeRT)
}

// oosTest validates the two strongest reference findings (OI contraction +
// extreme funding) out-of-sample: thresholds fit on the earlier half, tested
// on the later half, so an in-sample mirage can't pass.
func oosTest(s []sample, fee float64) {
	w := scorer.DefaultDetailWeights()
	type sig struct {
		signed, oiChg, fundLvl float64
		ts                     int64
	}
	var sigs []sig
	for _, x := range s {
		sc := scorer.ScoreTotal(x.in, w)
		if absI(sc) < 20 {
			continue
		}
		signed := x.fwd
		if sc < 0 {
			signed = -x.fwd
		}
		sigs = append(sigs, sig{signed, x.in.OIChg1h, x.fundLvl, x.ts})
	}
	sort.Slice(sigs, func(i, j int) bool { return sigs[i].ts < sigs[j].ts })
	mid := len(sigs) / 2
	train, test := sigs[:mid], sigs[mid:]
	fc := make([]float64, len(train))
	for i, g := range train {
		fc[i] = g.fundLvl
	}
	sort.Float64s(fc)
	fcut := 0.0
	if len(fc) > 0 {
		fcut = fc[len(fc)/3] // bottom-third |funding| boundary (mild funding)
	}

	type a struct {
		n, w int
		sum  float64
	}
	stat := func(set []sig, keep func(sig) bool) a {
		var x a
		for _, g := range set {
			if !keep(g) {
				continue
			}
			x.n++
			x.sum += g.signed
			if g.signed > 0 {
				x.w++
			}
		}
		return x
	}
	pr := func(label string, x a, total int) {
		if x.n == 0 {
			fmt.Printf("  %-20s 無\n", label)
			return
		}
		fmt.Printf("  %-20s n=%-5d (留%3.0f%%) 勝率%.1f%%  淨期望%+.3f%%\n",
			label, x.n, float64(x.n)/float64(total)*100,
			float64(x.w)/float64(x.n)*100, x.sum/float64(x.n)-fee)
	}
	runOn := func(name string, set []sig) {
		tot := len(set)
		fmt.Println("—", name, "—")
		pr("基準", stat(set, func(g sig) bool { return true }), tot)
		pr("OI收縮(OI<0)", stat(set, func(g sig) bool { return g.oiChg < 0 }), tot)
		pr("非溫和費率(|f|≥門檻)", stat(set, func(g sig) bool { return g.fundLvl >= fcut }), tot)
		pr("兩者皆是", stat(set, func(g sig) bool { return g.oiChg < 0 && g.fundLvl >= fcut }), tot)
	}

	fmt.Printf("\n=== OI象限 + 費率極端 樣本外驗證 (費率門檻=前半下三分位 %.4f) ===\n", fcut)
	runOn("前半(樣本內)", train)
	runOn("後半(樣本外)", test)
	fmt.Println("判讀: 後半各 gate 淨期望 > 後半基準、且差距像前半 → 真有效, 可碰評分器")
}

// referenceTest evaluates the candidate "reference / monitoring" indicators
// against the ±20 directional signals: OI×price quadrant, CVD–price divergence,
// funding extremity, range compression (squeeze), ATR%, and Deribit DVOL regime.
func referenceTest(s []sample, fee float64, dvol map[int64]float64) {
	w := scorer.DefaultDetailWeights()
	type sig struct {
		dir                                  int
		signed                               float64
		hb                                   int64
		oiChg, cvd, htf12, fundLvl, squeeze  float64
		atrPct, dvol                         float64
		hasDvol                              bool
	}
	var sigs []sig
	for _, x := range s {
		sc := scorer.ScoreTotal(x.in, w)
		if absI(sc) < 20 {
			continue
		}
		dir, signed := 1, x.fwd
		if sc < 0 {
			dir, signed = -1, -x.fwd
		}
		hb := x.ts / hourMs
		dv, ok := dvol[hb]
		sigs = append(sigs, sig{dir, signed, hb, x.in.OIChg1h, x.in.CVDRatio,
			x.htf12, x.fundLvl, x.squeeze, x.atrPct, dv, ok})
	}

	type a struct {
		n, w int
		sum  float64
	}
	add := func(x *a, v float64) {
		x.n++
		x.sum += v
		if v > 0 {
			x.w++
		}
	}
	pr := func(label string, x a) {
		if x.n == 0 {
			fmt.Printf("  %-18s 無\n", label)
			return
		}
		fmt.Printf("  %-18s n=%-5d 勝率%.1f%%  淨期望%+.3f%%\n", label, x.n,
			float64(x.w)/float64(x.n)*100, x.sum/float64(x.n)-fee)
	}
	tercile := func(get func(sig) float64, only func(sig) bool) (lo, hi float64) {
		var c []float64
		for _, g := range sigs {
			if only == nil || only(g) {
				c = append(c, get(g))
			}
		}
		sort.Float64s(c)
		if len(c) == 0 {
			return 0, 0
		}
		return c[len(c)/3], c[2*len(c)/3]
	}

	fmt.Println("\n=== 參考指標逐一測試 (對 ±20 方向訊號) ===")
	var base a
	for _, g := range sigs {
		add(&base, g.signed)
	}
	pr("全部訊號(基準)", base)

	// 1. OI×price quadrant: position backed by rising OI (new money) vs falling OI (unwind)
	fmt.Println("[OI象限] 動作由新倉(OI增) 還是 平倉(OI減) 推動")
	var oiUp, oiDn a
	for _, g := range sigs {
		if g.oiChg >= 0 {
			add(&oiUp, g.signed)
		} else {
			add(&oiDn, g.signed)
		}
	}
	pr("OI增(新倉撐)", oiUp)
	pr("OI減(平倉撐)", oiDn)

	// 2. CVD–price divergence: does 12h taker-buy CVD agree with the 12h price move?
	fmt.Println("[CVD背離] CVD 是否與 12h 價格同向")
	var cvAgree, cvDiv a
	for _, g := range sigs {
		if (g.cvd > 0) == (g.htf12 > 0) {
			add(&cvAgree, g.signed)
		} else {
			add(&cvDiv, g.signed)
		}
	}
	pr("CVD同向(無背離)", cvAgree)
	pr("CVD背離", cvDiv)

	// 3. funding extremity terciles
	loF, hiF := tercile(func(g sig) float64 { return g.fundLvl }, nil)
	fmt.Printf("[資金費率極端] |funding| 分檔 (低<%.4f 高>%.4f)\n", loF, hiF)
	var fLo, fMid, fHi a
	for _, g := range sigs {
		switch {
		case g.fundLvl <= loF:
			add(&fLo, g.signed)
		case g.fundLvl >= hiF:
			add(&fHi, g.signed)
		default:
			add(&fMid, g.signed)
		}
	}
	pr("費率溫和", fLo)
	pr("費率中", fMid)
	pr("費率極端", fHi)

	// 4. range compression (squeeze): >1 = recent range tighter than history
	loS, hiS := tercile(func(g sig) float64 { return g.squeeze }, nil)
	fmt.Printf("[區間壓縮] squeeze 分檔 (鬆<%.2f 緊>%.2f)\n", loS, hiS)
	var sLoo, sMid, sTight a
	for _, g := range sigs {
		switch {
		case g.squeeze <= loS:
			add(&sLoo, g.signed)
		case g.squeeze >= hiS:
			add(&sTight, g.signed)
		default:
			add(&sMid, g.signed)
		}
	}
	pr("區間鬆(已擴張)", sLoo)
	pr("中", sMid)
	pr("區間緊(蓄勢)", sTight)

	// 5. ATR% terciles (reference: position sizing, not a filter)
	loV, hiV := tercile(func(g sig) float64 { return g.atrPct }, nil)
	fmt.Printf("[ATR%%] 分檔 (低<%.2f%% 高>%.2f%%)\n", loV, hiV)
	var vLo, vMid, vHi a
	for _, g := range sigs {
		switch {
		case g.atrPct <= loV:
			add(&vLo, g.signed)
		case g.atrPct >= hiV:
			add(&vHi, g.signed)
		default:
			add(&vMid, g.signed)
		}
	}
	pr("低波動", vLo)
	pr("中", vMid)
	pr("高波動", vHi)

	// 6. Deribit DVOL regime (BTC forward implied vol; market-wide)
	hasDvol := false
	for _, g := range sigs {
		if g.hasDvol {
			hasDvol = true
			break
		}
	}
	if !hasDvol {
		fmt.Println("[DVOL] 無資料(Deribit 抓取失敗,略過)")
	} else {
		loD, hiD := tercile(func(g sig) float64 { return g.dvol }, func(g sig) bool { return g.hasDvol })
		fmt.Printf("[DVOL regime] BTC 隱含波動 分檔 (低<%.1f 高>%.1f)\n", loD, hiD)
		var dLo, dMid, dHi a
		for _, g := range sigs {
			if !g.hasDvol {
				continue
			}
			switch {
			case g.dvol <= loD:
				add(&dLo, g.signed)
			case g.dvol >= hiD:
				add(&dHi, g.signed)
			default:
				add(&dMid, g.signed)
			}
		}
		pr("低IV(平靜)", dLo)
		pr("中IV", dMid)
		pr("高IV(恐慌)", dHi)
	}
	fmt.Println("判讀: 各組與基準差越大 = 該指標越能分辨訊號好壞")
}

// qualityTest validates the combined "quality" filter (avoid high leverage
// density + avoid the NY block) on the ±20 signals, with an out-of-sample split.
func qualityTest(s []sample, fee float64) {
	w := scorer.DefaultDetailWeights()
	type sig struct {
		dir    int
		signed float64
		hb     int64
		oiVol  float64
		ts     int64
	}
	var sigs []sig
	for _, x := range s {
		sc := scorer.ScoreTotal(x.in, w)
		if absI(sc) < 20 {
			continue
		}
		dir, signed := 1, x.fwd
		if sc < 0 {
			dir, signed = -1, -x.fwd
		}
		sigs = append(sigs, sig{dir, signed, x.ts / hourMs, x.oiVol, x.ts})
	}
	hiTercile := func(set []sig) float64 {
		c := make([]float64, len(set))
		for i, g := range set {
			c[i] = g.oiVol
		}
		sort.Float64s(c)
		if len(c) == 0 {
			return math.MaxFloat64
		}
		return c[2*len(c)/3]
	}
	isNY := func(hb int64) bool { h := int(hb % 24); return h >= 12 && h < 18 }

	type a struct {
		n, w int
		sum  float64
	}
	stat := func(set []sig, keep func(sig) bool) a {
		var x a
		for _, g := range set {
			if !keep(g) {
				continue
			}
			x.n++
			x.sum += g.signed
			if g.signed > 0 {
				x.w++
			}
		}
		return x
	}
	pr := func(label string, x a, total int) {
		if x.n == 0 {
			fmt.Printf("  %-22s 無\n", label)
			return
		}
		fmt.Printf("  %-22s n=%-5d (留%3.0f%%) 勝率%.1f%%  淨期望%+.3f%%\n",
			label, x.n, float64(x.n)/float64(total)*100,
			float64(x.w)/float64(x.n)*100, x.sum/float64(x.n)-fee)
	}

	fmt.Println("\n=== 品質過濾合併驗證 (避開 高槓桿密度 + 紐約盤 12-18 UTC) ===")
	cut := hiTercile(sigs)
	tot := len(sigs)
	pr("全部(基準)", stat(sigs, func(g sig) bool { return true }), tot)
	pr("只避高槓桿密度", stat(sigs, func(g sig) bool { return g.oiVol < cut }), tot)
	pr("只避紐約盤", stat(sigs, func(g sig) bool { return !isNY(g.hb) }), tot)
	pr("合併(避兩者)", stat(sigs, func(g sig) bool { return g.oiVol < cut && !isNY(g.hb) }), tot)

	sort.Slice(sigs, func(i, j int) bool { return sigs[i].ts < sigs[j].ts })
	mid := len(sigs) / 2
	train, test := sigs[:mid], sigs[mid:]
	cutTr := hiTercile(train)
	fmt.Println("— 樣本外(前半擬合門檻, 後半測試) —")
	pr("後半 全部(基準)", stat(test, func(g sig) bool { return true }), len(test))
	pr("後半 合併過濾", stat(test, func(g sig) bool { return g.oiVol < cutTr && !isNY(g.hb) }), len(test))
	fmt.Println("判讀: 合併淨期望 > 基準、且樣本外仍成立 → 採用")
}

// overlayTest evaluates candidate overlays (filters) and new indicators against
// the ±20 directional signals: market breadth, higher-timeframe trend,
// volatility regime, time-of-day, OI acceleration, CVD slope, leverage density.
func overlayTest(s []sample, fee float64) {
	w := scorer.DefaultDetailWeights()
	// market breadth: net long-minus-short count across the universe per hour
	brNet := map[int64]int{}
	brTot := map[int64]int{}
	for _, x := range s {
		hb := x.ts / hourMs
		brTot[hb]++
		switch sc := scorer.ScoreTotal(x.in, w); {
		case sc > 0:
			brNet[hb]++
		case sc < 0:
			brNet[hb]--
		}
	}
	breadth := func(hb int64) float64 {
		if brTot[hb] == 0 {
			return 0
		}
		return float64(brNet[hb]) / float64(brTot[hb])
	}

	type sig struct {
		dir                             int
		signed                          float64
		hb                              int64
		atrPct, htf12, oiAccel, cvdSlope, oiVol float64
	}
	var sigs []sig
	for _, x := range s {
		sc := scorer.ScoreTotal(x.in, w)
		if absI(sc) < 20 {
			continue
		}
		dir, signed := 1, x.fwd
		if sc < 0 {
			dir, signed = -1, -x.fwd
		}
		sigs = append(sigs, sig{dir, signed, x.ts / hourMs, x.atrPct, x.htf12, x.oiAccel, x.cvdSlope, x.oiVol})
	}

	type a struct {
		n, w int
		sum  float64
	}
	add := func(x *a, v float64) {
		x.n++
		x.sum += v
		if v > 0 {
			x.w++
		}
	}
	pr := func(label string, x a) {
		if x.n == 0 {
			fmt.Printf("  %-18s 無\n", label)
			return
		}
		fmt.Printf("  %-18s n=%-5d 勝率%.1f%%  淨期望%+.3f%%\n", label, x.n,
			float64(x.w)/float64(x.n)*100, x.sum/float64(x.n)-fee)
	}
	tercile := func(get func(sig) float64) (lo, hi float64) {
		c := make([]float64, len(sigs))
		for i, g := range sigs {
			c[i] = get(g)
		}
		sort.Float64s(c)
		if len(c) == 0 {
			return 0, 0
		}
		return c[len(c)/3], c[2*len(c)/3]
	}

	fmt.Println("\n=== Overlay / 新指標 測試 (對 ±20 方向訊號) ===")
	var base a
	for _, g := range sigs {
		add(&base, g.signed)
	}
	pr("全部訊號(基準)", base)

	// 1. market breadth filter
	fmt.Println("[市場廣度] 訊號方向 vs 全市場淨多空")
	var brAl, brCt a
	for _, g := range sigs {
		b := breadth(g.hb)
		if (b > 0 && g.dir > 0) || (b < 0 && g.dir < 0) {
			add(&brAl, g.signed)
		} else if (b > 0 && g.dir < 0) || (b < 0 && g.dir > 0) {
			add(&brCt, g.signed)
		}
	}
	pr("順廣度(保留)", brAl)
	pr("逆廣度(濾掉)", brCt)

	// 2. higher-timeframe (12h) trend confirmation
	fmt.Println("[多時間框架] 訊號方向 vs 12h 趨勢")
	var hAl, hCt a
	for _, g := range sigs {
		if (g.htf12 > 0 && g.dir > 0) || (g.htf12 < 0 && g.dir < 0) {
			add(&hAl, g.signed)
		} else {
			add(&hCt, g.signed)
		}
	}
	pr("順12h(保留)", hAl)
	pr("逆12h(濾掉)", hCt)

	// 3. volatility regime (ATR% terciles)
	loA, hiA := tercile(func(g sig) float64 { return g.atrPct })
	fmt.Printf("[波動率regime] 依 ATR%% 分檔 (低<%.2f%% 高>%.2f%%)\n", loA, hiA)
	var vLo, vMid, vHi a
	for _, g := range sigs {
		switch {
		case g.atrPct <= loA:
			add(&vLo, g.signed)
		case g.atrPct >= hiA:
			add(&vHi, g.signed)
		default:
			add(&vMid, g.signed)
		}
	}
	pr("低波動", vLo)
	pr("中波動", vMid)
	pr("高波動", vHi)

	// 4. time-of-day (UTC 6h blocks)
	fmt.Println("[時段] 依進場 UTC 時段")
	var blk [4]a
	for _, g := range sigs {
		add(&blk[int(g.hb%24)/6], g.signed)
	}
	for i, nm := range []string{"00-06(亞洲)", "06-12(倫敦)", "12-18(紐約)", "18-24(美盤晚)"} {
		pr(nm, blk[i])
	}

	// 5. OI acceleration (conviction): is OI building faster?
	loAc, hiAc := tercile(func(g sig) float64 { return g.oiAccel })
	fmt.Printf("[OI加速度] 分檔 (減速<%.2f 加速>%.2f)\n", loAc, hiAc)
	var acHi, acMid, acLo a
	for _, g := range sigs {
		switch {
		case g.oiAccel >= hiAc:
			add(&acHi, g.signed)
		case g.oiAccel <= loAc:
			add(&acLo, g.signed)
		default:
			add(&acMid, g.signed)
		}
	}
	pr("OI加速(上1/3)", acHi)
	pr("OI中性", acMid)
	pr("OI減速(下1/3)", acLo)

	// 6. CVD slope: buying/selling pressure accelerating toward the signal?
	fmt.Println("[CVD斜率] 主動買賣壓是否朝訊號方向加速")
	var cAl, cCt a
	for _, g := range sigs {
		if (g.cvdSlope > 0 && g.dir > 0) || (g.cvdSlope < 0 && g.dir < 0) {
			add(&cAl, g.signed)
		} else {
			add(&cCt, g.signed)
		}
	}
	pr("CVD順勢(保留)", cAl)
	pr("CVD逆勢(濾掉)", cCt)

	// 7. leverage density (OI / turnover) terciles
	loV, hiV := tercile(func(g sig) float64 { return g.oiVol })
	fmt.Printf("[槓桿密度] OI/成交量 分檔 (低<%.2f 高>%.2f)\n", loV, hiV)
	var dLo, dMid, dHi a
	for _, g := range sigs {
		switch {
		case g.oiVol <= loV:
			add(&dLo, g.signed)
		case g.oiVol >= hiV:
			add(&dHi, g.signed)
		default:
			add(&dMid, g.signed)
		}
	}
	pr("低槓桿密度", dLo)
	pr("中", dMid)
	pr("高槓桿密度", dHi)

	fmt.Println("判讀: 保留組淨期望 > 基準、且濾掉組明顯較差 → 該 overlay 有效")
}

// regimeTest checks whether filtering the ±20 directional signals by BTC's
// trend (only take signals aligned with BTC) improves expectancy.
func regimeTest(s []sample, regimes map[string]map[int64]int, fee float64) {
	type sig struct {
		dir    int
		signed float64
		hb     int64
	}
	var sigs []sig
	w := scorer.DefaultDetailWeights()
	for _, x := range s {
		sc := scorer.ScoreTotal(x.in, w)
		if absI(sc) < 20 {
			continue
		}
		dir := 1
		signed := x.fwd
		if sc < 0 {
			dir, signed = -1, -x.fwd
		}
		sigs = append(sigs, sig{dir, signed, x.ts / hourMs})
	}
	type a struct {
		n, w int
		sum  float64
	}
	add := func(x *a, v float64) {
		x.n++
		x.sum += v
		if v > 0 {
			x.w++
		}
	}
	pr := func(label string, x a) {
		if x.n == 0 {
			fmt.Printf("  %-14s 無\n", label)
			return
		}
		fmt.Printf("  %-14s n=%-5d 勝率%.1f%%  淨期望%+.3f%%\n", label, x.n,
			float64(x.w)/float64(x.n)*100, x.sum/float64(x.n)-fee)
	}

	fmt.Println("\n=== BTC Regime 過濾 (對 ±20 方向訊號; 只取順 BTC 趨勢的) ===")
	var base a
	for _, g := range sigs {
		add(&base, g.signed)
	}
	pr("全部訊號", base)
	for _, name := range []string{"BTC 24h趨勢", "BTC 48h趨勢", "BTC vs EMA50"} {
		reg := regimes[name]
		var al, ct a
		for _, g := range sigs {
			switch reg[g.hb] {
			case g.dir:
				add(&al, g.signed)
			case -g.dir:
				add(&ct, g.signed)
			}
		}
		fmt.Println("[" + name + "]")
		pr("  順勢(保留)", al)
		pr("  逆勢(濾掉)", ct)
	}
	fmt.Println("順勢淨期望 > 全部、逆勢 < 全部 → 過濾有效")
}

// reportExit ranks TP/SL configs by expectancy on the radar's actual trades.
func reportExit(g []exitAcc) {
	type row struct {
		name              string
		n, wins           int
		winRate, exp, tot float64
	}
	var rows []row
	for ci, a := range g {
		if a.n == 0 {
			continue
		}
		rows = append(rows, row{exitCfgs[ci].name, a.n, a.wins,
			float64(a.wins) / float64(a.n) * 100, a.sum / float64(a.n), a.sum})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].exp > rows[j].exp })
	fmt.Println("\n=== TP/SL 出場最佳化 (雷達實際會交易的訊號, 走訪前向K線先觸發者; R=近12h區間) ===")
	fmt.Printf("%-20s %6s %8s %10s %10s\n", "設定(R倍數)", "交易數", "勝率", "每筆期望", "累計")
	for _, r := range rows {
		fmt.Printf("%-20s %6d %7.1f%% %+9.3f%% %+9.1f%%\n", r.name, r.n, r.winRate, r.exp, r.tot)
	}
	fmt.Println("依「每筆期望」排序;同樣本內回測,實盤以模擬追蹤為準")
}

// directionTest isolates the radar's weak link — the pump/dump CALL. Among the
// coins flagged as "about to move big" (top 20% by a direction-agnostic
// magnitude score), it compares direction rules by directional hit-rate and the
// net return of following the call. fwd is the signed close-to-close return.
func directionTest(s []sample) {
	mag := func(x sample) float64 {
		return clampF((x.volSpike-1)*18, 0, 40) + clampF(math.Abs(x.oiAccum)*1.8, 0, 30) + clampF((x.whaleRatio-1)*1, 0, 12)
	}
	v := make([]sample, len(s))
	copy(v, s)
	sort.Slice(v, func(i, j int) bool { return mag(v[i]) > mag(v[j]) })
	top := v[:len(v)/5] // big-move-coming subset

	vote := func(b ...bool) bool {
		n := 0
		for _, x := range b {
			if x {
				n++
			} else {
				n--
			}
		}
		return n >= 0
	}
	rules := []struct {
		name string
		pump func(sample) bool
	}{
		{"現用: 3h動能", func(x sample) bool { return x.accel3 >= 0 }},
		{"CVD 方向", func(x sample) bool { return x.cvd6 >= 0 }},
		{"OI 堆積方向", func(x sample) bool { return x.oiAccum >= 0 }},
		{"HTF 12h趨勢", func(x sample) bool { return x.htf12 >= 0 }},
		{"HTF 24h趨勢", func(x sample) bool { return x.chg24 >= 0 }},
		{"突破位置(>0.5)", func(x sample) bool { return x.breakoutPos >= 0.5 }},
		{"動能+CVD+OI 投票", func(x sample) bool { return vote(x.accel3 >= 0, x.cvd6 >= 0, x.oiAccum >= 0) }},
		{"動能+HTF12+突破 投票", func(x sample) bool { return vote(x.accel3 >= 0, x.htf12 >= 0, x.breakoutPos >= 0.5) }},
		{"★採用: OI方向+CVD補", func(x sample) bool {
			if math.Abs(x.oiAccum) < 1 {
				return x.cvd6 >= 0
			}
			return x.oiAccum >= 0
		}},
	}
	fmt.Println("\n=== 方向命中率檢驗 (取前20%「即將大動」的幣, 比較分多空的規則) ===")
	fmt.Printf("%-22s %8s %11s\n", "方向規則", "命中率", "跟單淨報酬")
	for _, r := range rules {
		hit, n := 0, 0
		var net float64
		for _, x := range top {
			p := r.pump(x)
			n++
			if (x.fwd > 0) == p {
				hit++
			}
			sgn := 1.0
			if !p {
				sgn = -1.0
			}
			net += x.fwd * sgn
		}
		fmt.Printf("%-22s %7.1f%% %10.3f%%\n", r.name, float64(hit)/float64(n)*100, net/float64(n))
	}
	fmt.Println("命中率>50% 且 跟單淨報酬>0 才算有方向 edge")
}

// optimizeScorer random-searches the directional ScoreDetail weights to maximise
// the net expectancy of the top 15% strongest signals, with a time-based
// train/test split. Answers "can the OI dashboard scorer be tuned further?".
func optimizeScorer(s []sample, fee float64) {
	if len(s) < 400 {
		return
	}
	tss := make([]int64, len(s))
	for i, x := range s {
		tss[i] = x.ts
	}
	sort.Slice(tss, func(i, j int) bool { return tss[i] < tss[j] })
	cut := tss[int(float64(len(tss))*0.7)]
	var train, test []sample
	for _, x := range s {
		if x.ts <= cut {
			train = append(train, x)
		} else {
			test = append(test, x)
		}
	}

	// objective: net signed expectancy of the top 15% by |score|
	const frac = 0.15
	obj := func(w scorer.DetailWeights, subset []sample) float64 {
		type sc struct {
			a   int
			fwd float64
		}
		arr := make([]sc, len(subset))
		for i, x := range subset {
			arr[i] = sc{scorer.ScoreTotal(x.in, w), x.fwd}
		}
		sort.Slice(arr, func(i, j int) bool { return absI(arr[i].a) > absI(arr[j].a) })
		k := int(float64(len(arr)) * frac)
		if k < 1 {
			k = 1
		}
		var sum float64
		for _, e := range arr[:k] {
			sgn := 1.0
			if e.a < 0 {
				sgn = -1.0
			}
			sum += e.fwd * sgn
		}
		return sum/float64(k) - fee
	}

	def := scorer.DefaultDetailWeights()
	rng := rand.New(rand.NewSource(7))
	pick := func(lo, hi float64) float64 { return lo + rng.Float64()*(hi-lo) }
	best := def
	bestTrain := obj(def, train)
	for t := 0; t < 1200; t++ {
		w := def // keep disabled factors (Mom1h, Rel) and threshold/divisor
		w.OIMax, w.OIHalf = pick(5, 25), pick(0.3, 4)
		w.CVDMax, w.CVDHalf = pick(5, 25), pick(3, 15)
		w.StructPts = pick(3, 15)
		w.Mom24hMax, w.Mom24hHalf = pick(3, 20), pick(0.5, 5)
		w.FundingMax, w.FundingHalf = pick(3, 20), pick(0.02, 0.2)
		w.CrowdMax, w.CrowdHalf = pick(3, 15), pick(5, 30)
		w.MinVol24h = pick(20e6, 300e6)
		if o := obj(w, train); o > bestTrain {
			bestTrain, best = o, w
		}
	}

	fmt.Println("\n=== OI 儀表板 評分器權重最佳化 (目標: top 15% 訊號淨期望; 訓練70/測試30) ===")
	fmt.Printf("%-10s %13s %13s\n", "設定", "訓練淨期望", "測試淨期望")
	fmt.Printf("%-10s %12.3f%% %12.3f%%\n", "目前權重", obj(def, train), obj(def, test))
	fmt.Printf("%-10s %12.3f%% %12.3f%%\n", "最佳搜尋", bestTrain, obj(best, test))
	fmt.Printf("最佳權重: OI(%.0f/%.1f) CVD(%.0f/%.0f) 結構%.0f 動能24(%.0f/%.1f) 費率(%.0f/%.2f) 多空(%.0f/%.0f) 流動性%.0fM\n",
		best.OIMax, best.OIHalf, best.CVDMax, best.CVDHalf, best.StructPts,
		best.Mom24hMax, best.Mom24hHalf, best.FundingMax, best.FundingHalf,
		best.CrowdMax, best.CrowdHalf, best.MinVol24h/1e6)
	fmt.Println("若『測試』欄沒比目前權重好 → 過擬合，不採用。")
}

// explosive validates pump-ahead signals: does ranking by a signal select coins
// with bigger forward MAX upside (and a higher "pop" rate) than average?
func explosive(s []sample, horizon int) {
	const pop = 10.0 // a "pump" = forward max upside >= 10%
	var sumUp float64
	pops := 0
	for _, x := range s {
		sumUp += x.fwdMaxUp
		if x.fwdMaxUp >= pop {
			pops++
		}
	}
	base := float64(pops) / float64(len(s)) * 100
	fmt.Printf("\n=== 暴噴驗證 (forward %dh 最大漲幅; pop = 漲幅 >= %.0f%%) ===\n", horizon, pop)
	fmt.Printf("整體基準: n=%d  pop率=%.1f%%  平均最大漲幅=%.2f%%\n", len(s), base, sumUp/float64(len(s)))
	fmt.Printf("\n%-18s %8s %9s %7s %8s\n", "訊號(取前20%)", "pop率", "平均漲幅", "lift", "r")

	corrUp := func(fn func(sample) float64) float64 {
		var n, sx, sy, sxy, sx2, sy2 float64
		for _, x := range s {
			xv, yv := fn(x), x.fwdMaxUp
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
	show := func(name string, fn func(sample) float64) {
		v := make([]sample, len(s))
		copy(v, s)
		sort.Slice(v, func(i, j int) bool { return fn(v[i]) > fn(v[j]) })
		top := v[:len(v)/5]
		var tu float64
		tp := 0
		for _, x := range top {
			tu += x.fwdMaxUp
			if x.fwdMaxUp >= pop {
				tp++
			}
		}
		pr := float64(tp) / float64(len(top)) * 100
		fmt.Printf("%-18s %7.1f%% %8.2f%% %6.2fx %+.3f\n", name, pr, tu/float64(len(top)), pr/base, corrUp(fn))
	}
	show("波動壓縮 squeeze", func(x sample) float64 { return x.squeeze })
	show("成交量突增", func(x sample) float64 { return x.volSpike })
	show("OI 堆積(12h)", func(x sample) float64 { return x.oiAccum })
	show("鯨魚單量 z", func(x sample) float64 { return x.whaleZ })
	show("費率極端", func(x sample) float64 { return x.fundLvl })
	show("ATR 擴張(近6/基準28)", func(x sample) float64 { return x.atrExp })
	show("ATR 水位%", func(x sample) float64 { return x.atrPct })
	show("★雷達綜合分數", func(x sample) float64 { return radarScore(defaultRW, x) })
	fmt.Println("lift > 1 = 該訊號選到的幣比平均更會噴；r = 訊號 vs 最大漲幅 相關性")

	// symmetry check: a real *pump* signal should give more upside than downside;
	// a pure volatility signal (like ATR level) gives symmetric up/down.
	fmt.Println("\n--- 對稱性檢驗 (取前20%該訊號 → 平均最大漲 vs 平均最大跌) ---")
	sym := func(name string, fn func(sample) float64) {
		v := make([]sample, len(s))
		copy(v, s)
		sort.Slice(v, func(i, j int) bool { return fn(v[i]) > fn(v[j]) })
		top := v[:len(v)/5]
		var up, dn float64
		for _, x := range top {
			up += x.fwdMaxUp
			dn += x.fwdMaxDn
		}
		n := float64(len(top))
		fmt.Printf("  %-16s 最大漲 %+.2f%% / 最大跌 -%.2f%%  (漲跌比 %.2f)\n", name, up/n, dn/n, (up/n)/(dn/n))
	}
	sym("ATR 水位%", func(x sample) float64 { return x.atrPct })
	sym("OI 堆積", func(x sample) float64 { return x.oiAccum })
	sym("★雷達綜合分數", func(x sample) float64 { return radarScore(defaultRW, x) })
	fmt.Println("漲跌比≈1 = 純波動(沒方向edge);>1 = 真的偏上行")
}

// rweights mirrors the radar's earlyScore tunables.
type rweights struct{ vol, oi, whale, accel, cvd, earlyDiv float64 }

var defaultRW = rweights{vol: 6, oi: 1.8, whale: 1.0, accel: 1.0, cvd: 0.4, earlyDiv: 68}

// radarScore replicates the live radar earlyScore so we can validate & tune it.
func radarScore(w rweights, s sample) float64 {
	vp := clampF((s.volSpike-1)*w.vol, 0, 40)
	oi := clampF(math.Abs(s.oiAccum)*w.oi, 0, 30)
	wh := clampF((s.whaleRatio-1)*w.whale, 0, 12)
	ac := clampF(math.Abs(s.accel3)*w.accel, 0, 12)
	cv := 0.0
	if (s.accel3 >= 0) == (s.cvd6 >= 0) {
		cv = clampF(math.Abs(s.cvd6)*w.cvd, 0, 8)
	}
	earliness := clampF(1-math.Abs(s.chg24)/w.earlyDiv, 0.3, 1)
	return (vp + oi + wh + ac + cv) * earliness
}

func absI(i int) int {
	if i < 0 {
		return -i
	}
	return i
}

func clampF(x, lo, hi float64) float64 {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

// topAvgUp = average forward max-upside of the top `frac` of subset ranked by
// score fn — i.e. "if I act on the radar's top picks, how much do they pop".
func topAvgUp(s []sample, fn func(sample) float64, frac float64) float64 {
	if len(s) == 0 {
		return 0
	}
	v := make([]sample, len(s))
	copy(v, s)
	sort.Slice(v, func(i, j int) bool { return fn(v[i]) > fn(v[j]) })
	k := int(float64(len(v)) * frac)
	if k < 1 {
		k = 1
	}
	var sum float64
	for _, x := range v[:k] {
		sum += x.fwdMaxUp
	}
	return sum / float64(k)
}

// optimizeRadar random-searches the radar weights to maximise the forward
// max-upside of its top picks, with a time-based train/test split so we can see
// whether a tuned config holds out of sample.
func optimizeRadar(s []sample) {
	if len(s) < 400 {
		return
	}
	// time split: train = older 70%, test = newer 30%
	tss := make([]int64, len(s))
	for i, x := range s {
		tss[i] = x.ts
	}
	sort.Slice(tss, func(i, j int) bool { return tss[i] < tss[j] })
	cut := tss[int(float64(len(tss))*0.7)]
	var train, test []sample
	for _, x := range s {
		if x.ts <= cut {
			train = append(train, x)
		} else {
			test = append(test, x)
		}
	}

	const frac = 0.05
	rng := rand.New(rand.NewSource(42))
	pick := func(lo, hi float64) float64 { return lo + rng.Float64()*(hi-lo) }
	best := defaultRW
	bestTrain := topAvgUp(train, func(x sample) float64 { return radarScore(best, x) }, frac)
	for t := 0; t < 1500; t++ {
		w := rweights{
			vol:      pick(5, 30),
			oi:       pick(0.3, 4),
			whale:    pick(0, 25),
			accel:    pick(0, 6),
			cvd:      pick(0, 0.5),
			earlyDiv: pick(20, 90),
		}
		o := topAvgUp(train, func(x sample) float64 { return radarScore(w, x) }, frac)
		if o > bestTrain {
			bestTrain, best = o, w
		}
	}

	rsTest := func(w rweights) float64 {
		return topAvgUp(test, func(x sample) float64 { return radarScore(w, x) }, frac)
	}
	baseTrain := topAvgUp(train, func(x sample) float64 { return radarScore(defaultRW, x) }, frac)
	fmt.Println("\n=== 雷達權重最佳化 (目標: top 5% 點火幣的 forward 最大漲幅; 訓練70/測試30 時間切分) ===")
	fmt.Printf("%-10s %14s %14s\n", "設定", "訓練 top5%漲幅", "測試 top5%漲幅")
	fmt.Printf("%-10s %13.2f%% %13.2f%%\n", "目前權重", baseTrain, rsTest(defaultRW))
	fmt.Printf("%-10s %13.2f%% %13.2f%%\n", "最佳搜尋", bestTrain, rsTest(best))
	fmt.Printf("最佳權重: vol=%.1f oi=%.2f whale=%.1f accel=%.1f cvd=%.2f earlyDiv=%.0f\n",
		best.vol, best.oi, best.whale, best.accel, best.cvd, best.earlyDiv)
	fmt.Println("若『測試』欄沒比目前權重好 → 屬過擬合，不該採用。")
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
		{"ATR 動能正規化(動能24÷ATR)", func(x sample) float64 {
			if x.atrPct <= 0 {
				return 0
			}
			return x.in.Mom24h / x.atrPct
		}, all},
		{"ATR 水位%(方向性?)", func(x sample) float64 { return x.atrPct }, all},
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
