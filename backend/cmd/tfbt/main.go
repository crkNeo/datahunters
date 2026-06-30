// Command tfbt compares the breakout-radar / gamble-book detector across kline
// timeframes (5m / 15m / 30m / 1h) to see whether running it faster helps after
// fees. Same detector shape (same BAR counts), just faster bars.
//
// ⚠️ Binance OI history is capped at 500 points PER PERIOD, so shorter
// timeframes have far less history (5m≈41h, 15m≈5d, 30m≈10d, 1h≈20d) and
// smaller samples — read shorter-TF results with caution.
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"datahunter/internal/exchange"
	"datahunter/internal/indicator"

	_ "modernc.org/sqlite"
)

var defaultCoins = []string{
	"BTC", "ETH", "SOL", "BNB", "XRP", "ADA", "AVAX", "SUI", "LTC", "DOT", "TRX",
	"NEAR", "APT", "ATOM", "TON", "ICP", "FIL", "SEI", "TIA", "BCH", "ARB", "OP",
	"LINK", "UNI", "AAVE", "ENA", "JUP", "INJ", "DOGE", "WIF", "TRUMP", "WLD", "FET", "ORDI",
}

func clampf(x, lo, hi float64) float64 {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

func pctf(a, b float64) float64 {
	if a == 0 {
		return 0
	}
	return (b - a) / a * 100
}

// ign mirrors cache.earlyScore (same weights as the live radar).
func ign(volSpike, oiAccum, whale, accel, cvd, earliness float64) int {
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

// simExit walks forward bars: first TP/SL touch wins, else time-exit at horizon.
func simExit(kl []exchange.Candle, i, h int, entry, R float64, pump bool) (float64, bool) {
	const tpM, slM = 0.618, 0.5
	if entry <= 0 || R <= 0 {
		return 0, false
	}
	var tp, sl float64
	if pump {
		tp, sl = entry+tpM*R, entry-slM*R
	} else {
		tp, sl = entry-tpM*R, entry+slM*R
	}
	tpRet, slRet := tpM*R/entry*100, -slM*R/entry*100
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
	r := (kl[end].Close - entry) / entry * 100
	if !pump {
		r = -r
	}
	return r, r > 0
}

type stat struct {
	n, wins int
	gross   float64
	absMove float64 // avg |TP distance| as % (move size proxy)
	fwdSum  float64 // uncapped signed forward return over horizon (真.實得)
	mfeSum  float64 // max favorable excursion over horizon (最大有利)
	elevSum float64 // |24-bar change| at entry (進場時已漲幅 = 越小越早)
	spanMs  int64
}

func stepMs(interval string) int64 {
	switch interval {
	case "5m":
		return 5 * 60000
	case "15m":
		return 15 * 60000
	case "30m":
		return 30 * 60000
	case "1h":
		return 3600000
	}
	return 3600000
}

func run(ex *exchange.Client, interval string, coins []string, fee, gate float64, horizon int) stat {
	step := stepMs(interval)
	var st stat
	for _, coin := range coins {
		sym := coin + "USDT"
		kl, err := ex.BinanceKlines(sym, interval, 1000)
		if err != nil || len(kl) < 60 {
			continue
		}
		oi, _ := ex.BinanceOIHist(sym, interval, 500)
		if len(oi) < 30 {
			continue
		}
		oiMap := map[int64]float64{}
		for _, p := range oi {
			oiMap[p.Ts/step] = p.SumOIValue
		}
		if st.spanMs == 0 {
			st.spanMs = int64(len(oi)) * step
		}
		for i := 48; i < len(kl)-horizon; i++ {
			hb := kl[i].Ts / step
			oiNow, ok := oiMap[hb]
			oiPast, ok2 := oiMap[hb-12]
			if !ok || !ok2 || oiPast == 0 {
				continue
			}
			oiAccum := pctf(oiPast, oiNow)
			// volume spike: last 3 vs 48-bar baseline
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
			// whale: recent vs baseline avg trade size
			var rs, bs float64
			var rc, bc int
			for j := i - 2; j <= i; j++ {
				if kl[j].Trades > 0 {
					rs += kl[j].QuoteVol / kl[j].Trades
					rc++
				}
			}
			for j := i - 47; j <= i; j++ {
				if kl[j].Trades > 0 {
					bs += kl[j].QuoteVol / kl[j].Trades
					bc++
				}
			}
			whale := 1.0
			if rc > 0 && bc > 0 && bs > 0 {
				whale = (rs / float64(rc)) / (bs / float64(bc))
			}
			accel := pctf(kl[i-3].Close, kl[i].Close)
			cvd := indicator.CVDFromKlines(kl[:i+1], 6)
			earliness := clampf(1-math.Abs(pctf(kl[i-24].Close, kl[i].Close))/68, 0.3, 1)
			score := ign(volSpike, oiAccum, whale, accel, cvd, earliness)
			if float64(score) < gate {
				continue
			}
			pump := oiAccum >= 0
			if math.Abs(oiAccum) < 1 {
				pump = cvd >= 0
			}
			hi, lo := kl[i].High, kl[i].Low
			for j := i - 11; j <= i; j++ {
				if kl[j].High > hi {
					hi = kl[j].High
				}
				if kl[j].Low < lo {
					lo = kl[j].Low
				}
			}
			R := hi - lo
			ret, win := simExit(kl, i, horizon, kl[i].Close, R, pump)
			st.n++
			st.gross += ret
			if win {
				st.wins++
			}
			if kl[i].Close > 0 {
				st.absMove += 0.618 * R / kl[i].Close * 100
			}
			// uncapped capture, max favorable excursion, entry elevation
			end := i + horizon
			if end >= len(kl) {
				end = len(kl) - 1
			}
			rawFwd := pctf(kl[i].Close, kl[end].Close)
			if !pump {
				rawFwd = -rawFwd
			}
			mfe := 0.0
			for j := i + 1; j <= end; j++ {
				var ex float64
				if pump {
					ex = (kl[j].High - kl[i].Close) / kl[i].Close * 100
				} else {
					ex = (kl[i].Close - kl[j].Low) / kl[i].Close * 100
				}
				if ex > mfe {
					mfe = ex
				}
			}
			st.fwdSum += rawFwd
			st.mfeSum += mfe
			st.elevSum += math.Abs(pctf(kl[i-24].Close, kl[i].Close))
		}
	}
	return st
}

func main() {
	intervals := flag.String("intervals", "5m,15m,30m,1h", "comma-separated timeframes")
	coinsCSV := flag.String("coins", strings.Join(defaultCoins, ","), "coins")
	fee := flag.Float64("fee", 0.10, "round-trip fee+slippage %")
	gate := flag.Float64("gate", 45, "ignition gate (45 = gamble, 55 = disciplined)")
	horizon := flag.Int("bars", 24, "forward hold horizon in bars")
	sweep := flag.Bool("sweep", false, "sweep the ignition gate on one interval")
	sweepIv := flag.String("sweepiv", "1h", "interval to sweep the gate on")
	early := flag.Bool("early", false, "test 'quiet OI accumulation' as an earlier (leading) signal")
	fib := flag.Bool("fib", false, "test fib 0.142-0.236 pullback entry vs market entry (gamble signals)")
	exit := flag.Bool("exit", false, "optimise TP/SL + trailing-stop exits on gamble signals")
	disc := flag.Bool("disc", false, "optimise disciplined-book entry (fresh-cross) with overlays")
	mtf := flag.Bool("mtf", false, "test 15m/30m confirmation on top of the 1h signal")
	lead := flag.Bool("lead", false, "detect on 15m/30m (earlier) but trade at 1h scale; vs 1h detect")
	bb := flag.Bool("bb", false, "test Bollinger mid-band (SMA20) filter on entries")
	session := flag.Bool("session", false, "break down ignition-signal performance by UTC session")
	ema := flag.Bool("ema", false, "test EMA trend filters on entry (gamble/disciplined)")
	funding := flag.Bool("funding", false, "test funding-rate filter on entries (OOS train/test)")
	fundlive := flag.Bool("fundlive", false, "test funding-rate filter on REAL recorded paper trades (SQLite)")
	outcomes := flag.Bool("outcomes", false, "break down REAL recorded trades by outcome (SQLite, no network)")
	earlyexit := flag.Bool("earlyexit", false, "reconstruct REAL trades' price paths and test break-even / time-stop exits")
	oicvd := flag.Bool("oicvd", false, "among score>=gate & OI-positive entries, split win-rate by CVD sign (OOS)")
	matrix := flag.Bool("matrix", false, "price/OI/CVD matrix: when price+CVD agree, does OI↑(new) beat OI↓(closing)?")
	recent := flag.Bool("recent", false, "dump recent closed gamble trades with OI/CVD alignment + hold time (SQLite)")
	extend := flag.Bool("extend", false, "split REAL gamble trades by OI-spike magnitude at entry (over-extension test)")
	premium := flag.Bool("premium", false, "stack validated filters (aligned + funding-fuel) into a premium subset (SQLite)")
	premiumbt := flag.Bool("premiumbt", false, "KLINE backtest of the aligned + funding-fuel premium stack (OOS)")
	flag.Parse()
	coins := strings.Split(*coinsCSV, ",")
	ex := exchange.NewClient()

	if *early {
		earlyAnalysis(ex, coins, *fee)
		return
	}

	if *fib {
		fibAnalysis(ex, coins, *fee, *gate)
		return
	}

	if *exit {
		exitAnalysis(ex, coins, *fee, *gate)
		return
	}

	if *disc {
		discAnalysis(ex, coins, *fee, *gate)
		return
	}

	if *mtf {
		mtfAnalysis(ex, coins, *fee, *gate)
		return
	}

	if *lead {
		leadAnalysis(ex, coins, *fee, *gate)
		return
	}

	if *bb {
		bbAnalysis(ex, coins, *fee, *gate)
		return
	}

	if *session {
		sessionAnalysis(ex, coins, *fee, *gate)
		return
	}

	if *ema {
		emaAnalysis(ex, coins, *fee, *gate)
		return
	}

	if *funding {
		fundingAnalysis(ex, coins, *fee, *gate, *horizon)
		return
	}

	if *fundlive {
		fundLiveAnalysis(ex)
		return
	}

	if *outcomes {
		outcomesAnalysis()
		return
	}

	if *earlyexit {
		earlyExitAnalysis(ex)
		return
	}

	if *oicvd {
		oiCvdAnalysis(ex, coins, *fee, *gate, *horizon)
		return
	}

	if *matrix {
		matrixAnalysis(ex, coins, *fee, *horizon)
		return
	}

	if *recent {
		recentAnalysis()
		return
	}

	if *extend {
		extendAnalysis()
		return
	}

	if *premium {
		premiumAnalysis()
		return
	}

	if *premiumbt {
		premiumBtAnalysis(ex, coins, *fee, *gate, *horizon)
		return
	}

	if *sweep {
		fmt.Printf("=== 門檻掃描 (%s, 持倉 %d 根, 費 %.2f%%) ===\n", *sweepIv, *horizon, *fee)
		fmt.Printf("%-5s %7s %7s %9s %10s %10s %10s\n",
			"門檻", "訊號數", "勝率", "淨期望", "實得(無停利)", "最大有利", "進場時漲幅")
		for _, g := range []float64{30, 35, 40, 45, 50, 55, 60} {
			st := run(ex, *sweepIv, coins, *fee, g, *horizon)
			if st.n == 0 {
				fmt.Printf("%-5.0f  無訊號\n", g)
				continue
			}
			nn := float64(st.n)
			fmt.Printf("%-5.0f %7d %6.1f%% %+8.3f%% %+9.2f%% %9.2f%% %9.2f%%\n",
				g, st.n, float64(st.wins)/nn*100, st.gross/nn-*fee,
				st.fwdSum/nn, st.mfeSum/nn, st.elevSum/nn)
		}
		fmt.Println("判讀: 門檻↓ 若『進場時漲幅』變小(進得更早)且勝率/淨期望沒崩 → 可降門檻;若勝率崩 → 門檻在保護你")
		return
	}

	fmt.Printf("=== 時間框架對比 (點火門檻 %.0f, 持倉 %d 根, 費 %.2f%%, TP0.618/SL0.5) ===\n", *gate, *horizon, *fee)
	fmt.Printf("%-6s %8s %8s %10s %10s %10s %8s\n", "框架", "訊號數", "勝率", "毛期望", "淨期望", "平均TP幅", "資料跨度")
	for _, iv := range strings.Split(*intervals, ",") {
		iv = strings.TrimSpace(iv)
		st := run(ex, iv, coins, *fee, *gate, *horizon)
		if st.n == 0 {
			fmt.Printf("%-6s  無資料\n", iv)
			continue
		}
		win := float64(st.wins) / float64(st.n) * 100
		gross := st.gross / float64(st.n)
		span := time.Duration(st.spanMs) * time.Millisecond
		fmt.Printf("%-6s %8d %7.1f%% %+9.3f%% %+9.3f%% %9.2f%% %8s\n",
			iv, st.n, win, gross, gross-*fee, st.absMove/float64(st.n), fmtSpan(span))
	}
	fmt.Println("判讀: 淨期望(扣費後)為正才有意義;平均TP幅越小,費用佔比越重")
}

// earlyAnalysis tests whether entering during "quiet OI accumulation" (OI rising
// while price is still flat) beats the current "already-moving" entry — i.e.
// whether we can detect the move 1-2 candles earlier and still call direction.
// Both require OI to be accumulating; the split is price already moved or not.
func earlyAnalysis(ex *exchange.Client, coins []string, fee float64) {
	const step = int64(3600000)
	const H = 12 // forward hold (bars)
	type grp struct {
		n, wins        int
		fwd, mfe, elev float64
	}
	add := func(g *grp, dirUp bool, kl []exchange.Candle, i, end int) {
		fwd := pctf(kl[i].Close, kl[end].Close)
		if !dirUp {
			fwd = -fwd
		}
		mfe := 0.0
		for j := i + 1; j <= end; j++ {
			var ex float64
			if dirUp {
				ex = (kl[j].High - kl[i].Close) / kl[i].Close * 100
			} else {
				ex = (kl[i].Close - kl[j].Low) / kl[i].Close * 100
			}
			if ex > mfe {
				mfe = ex
			}
		}
		g.n++
		g.fwd += fwd
		g.mfe += mfe
		g.elev += math.Abs(pctf(kl[i-12].Close, kl[i].Close))
		if fwd > 0 {
			g.wins++
		}
	}
	var quiet, moved grp
	for _, coin := range coins {
		kl, err := ex.BinanceKlines(coin+"USDT", "1h", 1000)
		if err != nil || len(kl) < 40 {
			continue
		}
		oi, _ := ex.BinanceOIHist(coin+"USDT", "1h", 500)
		if len(oi) < 30 {
			continue
		}
		oiMap := map[int64]float64{}
		for _, p := range oi {
			oiMap[p.Ts/step] = p.SumOIValue
		}
		for i := 24; i < len(kl)-H; i++ {
			hb := kl[i].Ts / step
			now, ok := oiMap[hb]
			past, ok2 := oiMap[hb-12]
			if !ok || !ok2 || past == 0 {
				continue
			}
			oiAccum := pctf(past, now)
			if oiAccum < 2 { // require OI to be accumulating (the lead)
				continue
			}
			chg12 := pctf(kl[i-12].Close, kl[i].Close)
			cvd := indicator.CVDFromKlines(kl[:i+1], 6)
			dirUp := cvd >= 0 // best directional guess this early
			end := i + H
			if math.Abs(chg12) < 2 { // price still flat = early "quiet accumulation"
				add(&quiet, dirUp, kl, i, end)
			} else { // price already moving = current radar style
				add(&moved, dirUp, kl, i, end)
			}
		}
	}
	fmt.Println("=== 領先訊號測試:靜默累積 vs 已發動 (1h, OI 12根累積≥2%, 方向用CVD, 持倉12根) ===")
	fmt.Printf("%-16s %7s %7s %10s %10s %12s\n", "類型", "訊號數", "勝率", "後續實得", "最大有利", "進場時已漲幅")
	pr := func(name string, g grp) {
		if g.n == 0 {
			fmt.Printf("%-16s  無\n", name)
			return
		}
		nn := float64(g.n)
		fmt.Printf("%-16s %7d %6.1f%% %+9.2f%% %9.2f%% %11.2f%%\n",
			name, g.n, float64(g.wins)/nn*100, g.fwd/nn, g.mfe/nn, g.elev/nn)
	}
	pr("靜默累積(早)", quiet)
	pr("已發動(現用)", moved)
	fmt.Println("判讀: 若『靜默累積』勝率≈50% → 太早猜不到方向(早=賭);若 >55% 且實得不差 → 有領先edge,可做『早期觀察』層")
}

// fibAnalysis tests the user's entry idea on gamble signals (1h, ignition≥gate):
// fib1 = signal bar extreme (波1 頂/底), fib0 = start of the leg (起漲點 or 前一根);
// wait for a SHALLOW pullback into 0.142–0.236 of the leg, then enter. Compared
// against the current market-entry. Both exit with TP 0.618R / SL 0.5R.
// simExitPx exits at the first TP/SL touch (explicit prices), else time-exits.
func simExitPx(kl []exchange.Candle, from, h int, entry, tp, sl float64, pump bool) (float64, bool) {
	end := from + h
	if end >= len(kl) {
		end = len(kl) - 1
	}
	for j := from + 1; j <= end; j++ {
		if pump {
			if kl[j].Low <= sl {
				return (sl - entry) / entry * 100, false
			}
			if kl[j].High >= tp {
				return (tp - entry) / entry * 100, true
			}
		} else {
			if kl[j].High >= sl {
				return (entry - sl) / entry * 100, false
			}
			if kl[j].Low <= tp {
				return (entry - tp) / entry * 100, true
			}
		}
	}
	r := (kl[end].Close - entry) / entry * 100
	if !pump {
		r = -r
	}
	return r, r > 0
}

// fibAnalysis sweeps the pullback-entry DEPTH on gamble signals (1h, ignition≥gate).
//   fib0 = 起漲點 (6-bar swing); fib1 = 波1 (signal high/low); L(x)=fib0+x*(fib1-fib0).
//   For each retracement p, entry = L(1-p) (price retraces p of the leg), then ride
//   the continuation. TP = extension L(1.236), SL = origin L(0).
//   p small = shallow pullback; p large = deep (near origin). Covers both readings
//   of "回採 0.142-0.236". Compared vs market-entry-at-signal with the same TP/SL.
func fibAnalysis(ex *exchange.Client, coins []string, fee, gate float64) {
	const step = int64(3600000)
	const waitPull = 12
	const holdH = 24
	const tp1x, slx = 1.236, 0.0
	retr := []float64{0.146, 0.236, 0.382, 0.5, 0.618, 0.786, 0.854}

	type acc struct {
		fills, win, miss int
		sum              float64
	}
	accs := make([]acc, len(retr))
	var base struct {
		n, win int
		sum    float64
	}

	for _, coin := range coins {
		kl, err := ex.BinanceKlines(coin+"USDT", "1h", 1000)
		if err != nil || len(kl) < 60 {
			continue
		}
		oi, _ := ex.BinanceOIHist(coin+"USDT", "1h", 500)
		if len(oi) < 30 {
			continue
		}
		oiMap := map[int64]float64{}
		for _, p := range oi {
			oiMap[p.Ts/step] = p.SumOIValue
		}
		for i := 48; i < len(kl)-holdH-waitPull; i++ {
			hb := kl[i].Ts / step
			now, ok := oiMap[hb]
			past, ok2 := oiMap[hb-12]
			if !ok || !ok2 || past == 0 {
				continue
			}
			oiAccum := pctf(past, now)
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
			accel := pctf(kl[i-3].Close, kl[i].Close)
			cvd := indicator.CVDFromKlines(kl[:i+1], 6)
			earliness := clampf(1-math.Abs(pctf(kl[i-24].Close, kl[i].Close))/68, 0.3, 1)
			if float64(ign(volSpike, oiAccum, 1, accel, cvd, earliness)) < gate {
				continue
			}
			pump := oiAccum >= 0
			if math.Abs(oiAccum) < 1 {
				pump = cvd >= 0
			}
			s0, s0h := kl[i].Low, kl[i].High
			for j := i - 5; j <= i; j++ {
				if kl[j].Low < s0 {
					s0 = kl[j].Low
				}
				if kl[j].High > s0h {
					s0h = kl[j].High
				}
			}
			var fib0, fib1 float64
			if pump {
				fib0, fib1 = s0, kl[i].High
			} else {
				fib0, fib1 = s0h, kl[i].Low
			}
			rng := fib1 - fib0
			if (pump && rng <= 0) || (!pump && rng >= 0) {
				continue
			}
			L := func(x float64) float64 { return fib0 + x*rng }
			tp, sl := L(tp1x), L(slx)
			// baseline: market entry at signal close
			if r, w := simExitPx(kl, i, holdH, kl[i].Close, tp, sl, pump); true {
				base.n++
				base.sum += r
				if w {
					base.win++
				}
			}
			// sweep entry retracement depth
			for di, p := range retr {
				entry := L(1 - p)
				filled, fb := false, 0
				for j := i + 1; j <= i+waitPull && j < len(kl); j++ {
					if (pump && kl[j].Low <= entry) || (!pump && kl[j].High >= entry) {
						filled, fb = true, j
						break
					}
				}
				if !filled {
					accs[di].miss++
					continue
				}
				r, w := simExitPx(kl, fb, holdH, entry, tp, sl, pump)
				accs[di].fills++
				accs[di].sum += r
				if w {
					accs[di].win++
				}
			}
		}
	}

	fmt.Printf("=== 斐波回採深度掃描 (賭博≥%.0f · 0=起漲點 1=波1高 · TP=延伸1.236 SL=起漲點0 · 等%d/持%d根 · 費%.2f%%) ===\n",
		gate, waitPull, holdH, fee)
	if base.n > 0 {
		fmt.Printf("基準 市價進(同 TP/SL): 訊號 %d · 勝率 %.1f%% · 淨期望 %+.3f%%\n",
			base.n, float64(base.win)/float64(base.n)*100, base.sum/float64(base.n)-fee)
	}
	fmt.Printf("%-26s %8s %8s %10s\n", "回採深度(進場位)", "成交率", "勝率", "淨期望")
	for di, p := range retr {
		a := accs[di]
		tot := a.fills + a.miss
		label := fmt.Sprintf("回%.1f%% (進L%.3f)", p*100, 1-p)
		if a.fills == 0 {
			fmt.Printf("%-26s   全踏空(%d)\n", label, tot)
			continue
		}
		f := float64(a.fills)
		fmt.Printf("%-26s %7.0f%% %7.1f%% %+9.3f%%\n",
			label, f/float64(tot)*100, float64(a.win)/f*100, a.sum/f-fee)
	}
	fmt.Println("判讀: 淺回(上面幾列)=回採少;深回(下面)=接近起漲點。看哪個深度淨期望 > 基準市價進,且成交率不會太低")
}

// discAnalysis optimises the disciplined book's ENTRY: fresh cross up of the
// ignition gate, market entry, fixed TP0.618/SL0.5 — then adds validated
// overlays (BTC-trend alignment, avoid NY session) and higher gates, to see
// which gives a more stable win rate + expectancy.
func discAnalysis(ex *exchange.Client, coins []string, fee, gate float64) {
	const step = int64(3600000)
	const H = 24
	g := int(gate)
	type acc struct {
		n, wins int
		sum     float64
	}
	var at0, at1, at2, at0ny, at1ny acc
	add := func(a *acc, r float64, w bool) {
		a.n++
		a.sum += r
		if w {
			a.wins++
		}
	}
	score := func(kl []exchange.Candle, oiMap map[int64]float64, i int) (int, bool) {
		hb := kl[i].Ts / step
		now, ok := oiMap[hb]
		past, ok2 := oiMap[hb-12]
		if !ok || !ok2 || past == 0 {
			return 0, false
		}
		oiAccum := pctf(past, now)
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
		accel := pctf(kl[i-3].Close, kl[i].Close)
		cvd := indicator.CVDFromKlines(kl[:i+1], 6)
		earliness := clampf(1-math.Abs(pctf(kl[i-24].Close, kl[i].Close))/68, 0.3, 1)
		return ign(volSpike, oiAccum, 1, accel, cvd, earliness), true
	}
	dirOf := func(kl []exchange.Candle, oiMap map[int64]float64, i int) bool {
		hb := kl[i].Ts / step
		oiAccum := pctf(oiMap[hb-12], oiMap[hb])
		if math.Abs(oiAccum) < 1 {
			return indicator.CVDFromKlines(kl[:i+1], 6) >= 0
		}
		return oiAccum >= 0
	}
	swingR := func(kl []exchange.Candle, i int) float64 {
		hi, lo := kl[i].High, kl[i].Low
		for j := i - 11; j <= i; j++ {
			if kl[j].High > hi {
				hi = kl[j].High
			}
			if kl[j].Low < lo {
				lo = kl[j].Low
			}
		}
		return hi - lo
	}

	for _, coin := range coins {
		kl, err := ex.BinanceKlines(coin+"USDT", "1h", 1000)
		if err != nil || len(kl) < 60 {
			continue
		}
		oi, _ := ex.BinanceOIHist(coin+"USDT", "1h", 500)
		if len(oi) < 30 {
			continue
		}
		oiMap := map[int64]float64{}
		for _, p := range oi {
			oiMap[p.Ts/step] = p.SumOIValue
		}
		prev := 999
		for i := 48; i < len(kl)-H; i++ {
			sc, ok := score(kl, oiMap, i)
			if !ok {
				prev = 999
				continue
			}
			cross := sc >= g && prev < g
			prev = sc
			if !cross {
				continue
			}
			pump := dirOf(kl, oiMap, i)
			R := swingR(kl, i)
			hb := kl[i].Ts / step
			notNY := !(int(hb%24) >= 12 && int(hb%24) < 18)
			// entry at the cross bar (current behaviour)
			r0, w0 := simExitG(kl, i, H, kl[i].Close, R, 0.618, 0.5, pump)
			add(&at0, r0, w0)
			if notNY {
				add(&at0ny, r0, w0)
			}
			// delay 1 bar (let the thrust candle close, enter next)
			if i+1 < len(kl) {
				r1, w1 := simExitG(kl, i+1, H, kl[i+1].Close, R, 0.618, 0.5, pump)
				add(&at1, r1, w1)
				if notNY {
					add(&at1ny, r1, w1)
				}
			}
			// delay 2 bars
			if i+2 < len(kl) {
				r2, w2 := simExitG(kl, i+2, H, kl[i+2].Close, R, 0.618, 0.5, pump)
				add(&at2, r2, w2)
			}
		}
	}

	fmt.Printf("=== 延遲進場測試 (gate%d 新鮮穿越, 市價進, TP0.618/SL0.5, 持%d根, 費%.2f%%) ===\n", g, H, fee)
	fmt.Printf("%-22s %7s %8s %10s\n", "進場時點", "訊號數", "勝率", "淨期望")
	pr := func(label string, a acc) {
		if a.n == 0 {
			fmt.Printf("%-22s %7d\n", label, 0)
			return
		}
		fmt.Printf("%-22s %7d %7.1f%% %+9.3f%%\n", label, a.n, float64(a.wins)/float64(a.n)*100, a.sum/float64(a.n)-fee)
	}
	pr("穿越當下進(現用)", at0)
	pr("延1根進", at1)
	pr("延2根進", at2)
	pr("穿越當下+避紐約", at0ny)
	pr("延1根+避紐約", at1ny)
	fmt.Println("判讀: 若『延1根』勝率/期望明顯高於『當下進』→ 證實『買在尖頭』是問題,延後進場較好")
}

// mtfAnalysis tests whether requiring the 15m/30m to ALSO move in the signal's
// direction (at the moment the 1h signal fires) improves the 1h gamble signal.
// No look-ahead: the lower-TF bar used closes at the same time as the 1h bar.
func mtfAnalysis(ex *exchange.Client, coins []string, fee, gate float64) {
	const step, step15, step30 = int64(3600000), int64(900000), int64(1800000)
	const H = 24
	g := int(gate)
	type acc struct {
		n, wins int
		sum     float64
	}
	var base, c15, c15v, c30, c30v acc
	add := func(a *acc, r float64, w bool) {
		a.n++
		a.sum += r
		if w {
			a.wins++
		}
	}
	for _, coin := range coins {
		kl, err := ex.BinanceKlines(coin+"USDT", "1h", 1000)
		if err != nil || len(kl) < 60 {
			continue
		}
		oi, _ := ex.BinanceOIHist(coin+"USDT", "1h", 500)
		if len(oi) < 30 {
			continue
		}
		oiMap := map[int64]float64{}
		for _, p := range oi {
			oiMap[p.Ts/step] = p.SumOIValue
		}
		time.Sleep(150 * time.Millisecond) // be polite (4 fetches/coin)
		kl15, _ := ex.BinanceKlines(coin+"USDT", "15m", 1000)
		kl30, _ := ex.BinanceKlines(coin+"USDT", "30m", 1000)
		idx15 := map[int64]int{}
		for j, c := range kl15 {
			idx15[c.Ts] = j
		}
		idx30 := map[int64]int{}
		for j, c := range kl30 {
			idx30[c.Ts] = j
		}
		for i := 48; i < len(kl)-H; i++ {
			hb := kl[i].Ts / step
			now, ok := oiMap[hb]
			past, ok2 := oiMap[hb-12]
			if !ok || !ok2 || past == 0 {
				continue
			}
			oiAccum := pctf(past, now)
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
			accel := pctf(kl[i-3].Close, kl[i].Close)
			cvd := indicator.CVDFromKlines(kl[:i+1], 6)
			earliness := clampf(1-math.Abs(pctf(kl[i-24].Close, kl[i].Close))/68, 0.3, 1)
			if float64(ign(volSpike, oiAccum, 1, accel, cvd, earliness)) < float64(g) {
				continue
			}
			pump := oiAccum >= 0
			if math.Abs(oiAccum) < 1 {
				pump = cvd >= 0
			}
			hi, lo := kl[i].High, kl[i].Low
			for j := i - 11; j <= i; j++ {
				if kl[j].High > hi {
					hi = kl[j].High
				}
				if kl[j].Low < lo {
					lo = kl[j].Low
				}
			}
			R := hi - lo
			r, w := simExitG(kl, i, H, kl[i].Close, R, 0.618, 0.5, pump)
			add(&base, r, w)
			ts := kl[i].Ts
			aligned := func(x float64) bool { return (pump && x > 0) || (!pump && x < 0) }
			alignedCV := func(x float64) bool { return (pump && x >= 0) || (!pump && x <= 0) }
			// 15m bar that closes at the 1h bar's close = open ts+45m
			if j, ok := idx15[ts+45*60000]; ok && j >= 2 {
				if aligned(pctf(kl15[j-2].Close, kl15[j].Close)) {
					add(&c15, r, w)
					if alignedCV(indicator.CVDFromKlines(kl15[:j+1], 6)) {
						add(&c15v, r, w)
					}
				}
			}
			// 30m bar that closes at the 1h bar's close = open ts+30m
			if j, ok := idx30[ts+30*60000]; ok && j >= 2 {
				if aligned(pctf(kl30[j-2].Close, kl30[j].Close)) {
					add(&c30, r, w)
					if alignedCV(indicator.CVDFromKlines(kl30[:j+1], 6)) {
						add(&c30v, r, w)
					}
				}
			}
		}
	}
	fmt.Printf("=== 多時間框架確認 (1h gate%d 訊號, +15m/30m 同向, 市價進, TP0.618/SL0.5, 持%d根, 費%.2f%%) ===\n", g, H, fee)
	fmt.Printf("%-22s %7s %8s %10s\n", "條件", "訊號數", "勝率", "淨期望")
	pr := func(label string, a acc) {
		if a.n == 0 {
			fmt.Printf("%-22s %7d\n", label, 0)
			return
		}
		fmt.Printf("%-22s %7d %7.1f%% %+9.3f%%\n", label, a.n, float64(a.wins)/float64(a.n)*100, a.sum/float64(a.n)-fee)
	}
	pr("1h(基準)", base)
	pr("+15m 同向", c15)
	pr("+15m 同向+CVD", c15v)
	pr("+30m 同向", c30)
	pr("+30m 同向+CVD", c30v)
	fmt.Println("判讀: 加確認後勝率/期望↑ 且訊號數沒崩太多 → 有用。15m 只回溯~15天,樣本較少")
}

// leadAnalysis tests the user's idea: detect the ignition on a SMALLER timeframe
// (earlier) but trade it at 1h scale (R = 12h swing, hold 24h, TP0.618/SL0.5).
// Reports entry "elevation" (|24h change| at entry) — lower = entered earlier.
func leadAnalysis(ex *exchange.Client, coins []string, fee, gate float64) {
	type tfDef struct {
		name string
		ms   int64
	}
	tfs := []tfDef{{"1h", 3600000}, {"30m", 1800000}, {"15m", 900000}}
	type acc struct {
		n, wins int
		sum, elev float64
	}
	g := float64(int(gate))

	fmt.Printf("=== 小框架早偵測 (偵測TF, 交易=1h級 R=12h/持24h/TP0.618/SL0.5, gate%.0f, 費%.2f%%) ===\n", g, fee)
	fmt.Printf("%-8s %7s %8s %10s %12s\n", "偵測TF", "訊號數", "勝率", "淨期望", "進場時24h漲幅")

	for _, tf := range tfs {
		step := tf.ms
		bars12 := int(int64(12*3600000) / step) // 12h in TF bars: 12 / 24 / 48
		bars24 := bars12 * 2                     // 24h hold
		var a acc
		for _, coin := range coins {
			kl, err := ex.BinanceKlines(coin+"USDT", tf.name, 1000)
			if err != nil || len(kl) < 60 {
				continue
			}
			oi, _ := ex.BinanceOIHist(coin+"USDT", tf.name, 500)
			if len(oi) < 30 {
				continue
			}
			oiMap := map[int64]float64{}
			for _, p := range oi {
				oiMap[p.Ts/step] = p.SumOIValue
			}
			time.Sleep(60 * time.Millisecond)
			start := 48
			if bars24 > start {
				start = bars24
			}
			for i := start; i < len(kl)-bars24; i++ {
				hb := kl[i].Ts / step
				now, ok := oiMap[hb]
				past, ok2 := oiMap[hb-12]
				if !ok || !ok2 || past == 0 {
					continue
				}
				oiAccum := pctf(past, now)
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
				accel := pctf(kl[i-3].Close, kl[i].Close)
				cvd := indicator.CVDFromKlines(kl[:i+1], 6)
				earliness := clampf(1-math.Abs(pctf(kl[i-24].Close, kl[i].Close))/68, 0.3, 1)
				if float64(ign(volSpike, oiAccum, 1, accel, cvd, earliness)) < g {
					continue
				}
				pump := oiAccum >= 0
				if math.Abs(oiAccum) < 1 {
					pump = cvd >= 0
				}
				hi, lo := kl[i].High, kl[i].Low
				for j := i - bars12 + 1; j <= i; j++ {
					if kl[j].High > hi {
						hi = kl[j].High
					}
					if kl[j].Low < lo {
						lo = kl[j].Low
					}
				}
				R := hi - lo
				ret, win := simExit(kl, i, bars24, kl[i].Close, R, pump)
				a.n++
				a.sum += ret
				if win {
					a.wins++
				}
				a.elev += math.Abs(pctf(kl[i-bars24].Close, kl[i].Close))
			}
		}
		if a.n == 0 {
			fmt.Printf("%-8s %7d\n", tf.name, 0)
			continue
		}
		n := float64(a.n)
		fmt.Printf("%-8s %7d %7.1f%% %+9.3f%% %11.2f%%\n", tf.name, a.n, float64(a.wins)/n*100, a.sum/n-fee, a.elev/n)
	}
	fmt.Println("判讀: 若小框架『進場時24h漲幅』明顯較低(進更早)且勝率/期望沒崩 → 早偵測有用;若期望掉 → 早=雜訊")
}

// bbAnalysis tests adding a Bollinger mid-band (SMA20) filter to the ignition
// entries: "above mid" (trend filter) and "fresh break of mid" (breakout trigger).
func bbAnalysis(ex *exchange.Client, coins []string, fee, gate float64) {
	const step = int64(3600000)
	const H = 24
	g := float64(int(gate))
	sma := func(kl []exchange.Candle, i, n int) float64 {
		s := 0.0
		for j := i - n + 1; j <= i; j++ {
			s += kl[j].Close
		}
		return s / float64(n)
	}
	type acc struct {
		n, wins int
		sum     float64
	}
	var base, above, brk acc
	add := func(a *acc, r float64, w bool) {
		a.n++
		a.sum += r
		if w {
			a.wins++
		}
	}
	for _, coin := range coins {
		kl, err := ex.BinanceKlines(coin+"USDT", "1h", 1000)
		if err != nil || len(kl) < 60 {
			continue
		}
		oi, _ := ex.BinanceOIHist(coin+"USDT", "1h", 500)
		if len(oi) < 30 {
			continue
		}
		oiMap := map[int64]float64{}
		for _, p := range oi {
			oiMap[p.Ts/step] = p.SumOIValue
		}
		for i := 48; i < len(kl)-H; i++ {
			hb := kl[i].Ts / step
			now, ok := oiMap[hb]
			past, ok2 := oiMap[hb-12]
			if !ok || !ok2 || past == 0 {
				continue
			}
			oiAccum := pctf(past, now)
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
			accel := pctf(kl[i-3].Close, kl[i].Close)
			cvd := indicator.CVDFromKlines(kl[:i+1], 6)
			earliness := clampf(1-math.Abs(pctf(kl[i-24].Close, kl[i].Close))/68, 0.3, 1)
			if float64(ign(volSpike, oiAccum, 1, accel, cvd, earliness)) < g {
				continue
			}
			pump := oiAccum >= 0
			if math.Abs(oiAccum) < 1 {
				pump = cvd >= 0
			}
			hi, lo := kl[i].High, kl[i].Low
			for j := i - 11; j <= i; j++ {
				if kl[j].High > hi {
					hi = kl[j].High
				}
				if kl[j].Low < lo {
					lo = kl[j].Low
				}
			}
			R := hi - lo
			r, w := simExitG(kl, i, H, kl[i].Close, R, 0.618, 0.5, pump)
			add(&base, r, w)
			mid, midPrev := sma(kl, i, 20), sma(kl, i-1, 20)
			c, cPrev := kl[i].Close, kl[i-1].Close
			isAbove := (pump && c > mid) || (!pump && c < mid)
			isBreak := (pump && c > mid && cPrev <= midPrev) || (!pump && c < mid && cPrev >= midPrev)
			if isAbove {
				add(&above, r, w)
			}
			if isBreak {
				add(&brk, r, w)
			}
		}
	}
	fmt.Printf("=== 布林中軌(SMA20)過濾 (1h gate%.0f, 市價進, TP0.618/SL0.5, 持%d根, 費%.2f%%) ===\n", g, H, fee)
	fmt.Printf("%-22s %7s %8s %10s\n", "條件", "訊號數", "勝率", "淨期望")
	pr := func(label string, a acc) {
		if a.n == 0 {
			fmt.Printf("%-22s %7d\n", label, 0)
			return
		}
		fmt.Printf("%-22s %7d %7.1f%% %+9.3f%%\n", label, a.n, float64(a.wins)/float64(a.n)*100, a.sum/float64(a.n)-fee)
	}
	pr("1h(基準)", base)
	pr("+中軌之上(順勢)", above)
	pr("+突破中軌(剛站上)", brk)
	fmt.Println("判讀: 過濾後勝率/期望↑ 且訊號數沒崩太多 → 有用")
}

func emaSeries(kl []exchange.Candle, p int) []float64 {
	out := make([]float64, len(kl))
	k := 2.0 / (float64(p) + 1)
	for i := range kl {
		if i == 0 {
			out[i] = kl[i].Close
		} else {
			out[i] = kl[i].Close*k + out[i-1]*(1-k)
		}
	}
	return out
}

// emaAnalysis tests EMA trend filters on the ignition entries: price above
// EMA20/EMA50, EMA20>EMA50 (trend aligned), and a fresh break of EMA20.
func emaAnalysis(ex *exchange.Client, coins []string, fee, gate float64) {
	const step = int64(3600000)
	const H = 24
	g := float64(int(gate))
	type acc struct {
		n, wins int
		sum     float64
	}
	var base, a20, a50, trend, cross acc
	add := func(a *acc, r float64, w bool) {
		a.n++
		a.sum += r
		if w {
			a.wins++
		}
	}
	for _, coin := range coins {
		kl, err := ex.BinanceKlines(coin+"USDT", "1h", 1000)
		if err != nil || len(kl) < 60 {
			continue
		}
		oi, _ := ex.BinanceOIHist(coin+"USDT", "1h", 500)
		if len(oi) < 30 {
			continue
		}
		oiMap := map[int64]float64{}
		for _, p := range oi {
			oiMap[p.Ts/step] = p.SumOIValue
		}
		ema20, ema50 := emaSeries(kl, 20), emaSeries(kl, 50)
		for i := 48; i < len(kl)-H; i++ {
			hb := kl[i].Ts / step
			now, ok := oiMap[hb]
			past, ok2 := oiMap[hb-12]
			if !ok || !ok2 || past == 0 {
				continue
			}
			oiAccum := pctf(past, now)
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
			accel := pctf(kl[i-3].Close, kl[i].Close)
			cvd := indicator.CVDFromKlines(kl[:i+1], 6)
			earliness := clampf(1-math.Abs(pctf(kl[i-24].Close, kl[i].Close))/68, 0.3, 1)
			if float64(ign(volSpike, oiAccum, 1, accel, cvd, earliness)) < g {
				continue
			}
			pump := oiAccum >= 0
			if math.Abs(oiAccum) < 1 {
				pump = cvd >= 0
			}
			hi, lo := kl[i].High, kl[i].Low
			for j := i - 11; j <= i; j++ {
				if kl[j].High > hi {
					hi = kl[j].High
				}
				if kl[j].Low < lo {
					lo = kl[j].Low
				}
			}
			R := hi - lo
			r, w := simExitG(kl, i, H, kl[i].Close, R, 0.618, 0.5, pump)
			add(&base, r, w)
			c := kl[i].Close
			up20 := (pump && c > ema20[i]) || (!pump && c < ema20[i])
			up50 := (pump && c > ema50[i]) || (!pump && c < ema50[i])
			tr := (pump && ema20[i] > ema50[i]) || (!pump && ema20[i] < ema50[i])
			cr := (pump && c > ema20[i] && kl[i-1].Close <= ema20[i-1]) ||
				(!pump && c < ema20[i] && kl[i-1].Close >= ema20[i-1])
			if up20 {
				add(&a20, r, w)
			}
			if up50 {
				add(&a50, r, w)
			}
			if tr {
				add(&trend, r, w)
			}
			if cr {
				add(&cross, r, w)
			}
		}
	}
	fmt.Printf("=== EMA 趨勢過濾 (1h gate%.0f, 市價進, TP0.618/SL0.5, 持%d根, 費%.2f%%) ===\n", g, H, fee)
	fmt.Printf("%-22s %7s %8s %10s\n", "條件", "訊號數", "勝率", "淨期望")
	pr := func(label string, a acc) {
		if a.n == 0 {
			fmt.Printf("%-22s %7d\n", label, 0)
			return
		}
		fmt.Printf("%-22s %7d %7.1f%% %+9.3f%%\n", label, a.n, float64(a.wins)/float64(a.n)*100, a.sum/float64(a.n)-fee)
	}
	pr("1h(基準)", base)
	pr("+價>EMA20(順勢)", a20)
	pr("+價>EMA50", a50)
	pr("+EMA20>EMA50(多頭排列)", trend)
	pr("+剛站上EMA20", cross)
	fmt.Println("判讀: 過濾後勝率/期望↑ 且訊號數沒崩太多 → 有用")
}

// sessionAnalysis breaks ignition-signal performance down by UTC session block,
// to show whether the NY block (12-18 UTC) really is the weakest.
func sessionAnalysis(ex *exchange.Client, coins []string, fee, gate float64) {
	const step = int64(3600000)
	const H = 24
	g := float64(int(gate))
	type acc struct {
		n, wins int
		sum     float64
	}
	var blk [4]acc // 00-06 / 06-12 / 12-18(NY) / 18-24
	add := func(a *acc, r float64, w bool) {
		a.n++
		a.sum += r
		if w {
			a.wins++
		}
	}
	for _, coin := range coins {
		kl, err := ex.BinanceKlines(coin+"USDT", "1h", 1000)
		if err != nil || len(kl) < 60 {
			continue
		}
		oi, _ := ex.BinanceOIHist(coin+"USDT", "1h", 500)
		if len(oi) < 30 {
			continue
		}
		oiMap := map[int64]float64{}
		for _, p := range oi {
			oiMap[p.Ts/step] = p.SumOIValue
		}
		for i := 48; i < len(kl)-H; i++ {
			hb := kl[i].Ts / step
			now, ok := oiMap[hb]
			past, ok2 := oiMap[hb-12]
			if !ok || !ok2 || past == 0 {
				continue
			}
			oiAccum := pctf(past, now)
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
			accel := pctf(kl[i-3].Close, kl[i].Close)
			cvd := indicator.CVDFromKlines(kl[:i+1], 6)
			earliness := clampf(1-math.Abs(pctf(kl[i-24].Close, kl[i].Close))/68, 0.3, 1)
			if float64(ign(volSpike, oiAccum, 1, accel, cvd, earliness)) < g {
				continue
			}
			pump := oiAccum >= 0
			if math.Abs(oiAccum) < 1 {
				pump = cvd >= 0
			}
			hi, lo := kl[i].High, kl[i].Low
			for j := i - 11; j <= i; j++ {
				if kl[j].High > hi {
					hi = kl[j].High
				}
				if kl[j].Low < lo {
					lo = kl[j].Low
				}
			}
			R := hi - lo
			r, w := simExitG(kl, i, H, kl[i].Close, R, 0.618, 0.5, pump)
			add(&blk[int(hb%24)/6], r, w)
		}
	}
	fmt.Printf("=== 時段分塊回測 (1h gate%.0f, 市價進, TP0.618/SL0.5, 持%d根, 費%.2f%%) ===\n", g, H, fee)
	fmt.Printf("%-22s %7s %8s %10s\n", "UTC 時段", "訊號數", "勝率", "淨期望")
	names := []string{"00-06 (亞洲)", "06-12 (倫敦)", "12-18 (紐約)", "18-24 (美盤晚)"}
	for i, a := range blk {
		if a.n == 0 {
			fmt.Printf("%-22s %7d\n", names[i], 0)
			continue
		}
		fmt.Printf("%-22s %7d %7.1f%% %+9.3f%%\n", names[i], a.n, float64(a.wins)/float64(a.n)*100, a.sum/float64(a.n)-fee)
	}
	fmt.Println("判讀: 12-18(紐約)若勝率/期望明顯墊底 → 避開有理")
}

// simExitG: fixed TP/SL with configurable multiples of R. Returns return%, win.
func simExitG(kl []exchange.Candle, i, h int, entry, R, tpM, slM float64, pump bool) (float64, bool) {
	if entry <= 0 || R <= 0 {
		return 0, false
	}
	var tp, sl float64
	if pump {
		tp, sl = entry+tpM*R, entry-slM*R
	} else {
		tp, sl = entry-tpM*R, entry+slM*R
	}
	end := i + h
	if end >= len(kl) {
		end = len(kl) - 1
	}
	for j := i + 1; j <= end; j++ {
		if pump {
			if kl[j].Low <= sl {
				return -slM * R / entry * 100, false
			}
			if kl[j].High >= tp {
				return tpM * R / entry * 100, true
			}
		} else {
			if kl[j].High >= sl {
				return -slM * R / entry * 100, false
			}
			if kl[j].Low <= tp {
				return tpM * R / entry * 100, true
			}
		}
	}
	r := (kl[end].Close - entry) / entry * 100
	if !pump {
		r = -r
	}
	return r, r > 0
}

// simTrail: initial SL at initSL*R, then a trailing stop trailK*R behind the peak
// (lets winners run). Returns return%, win, hold bars.
func simTrail(kl []exchange.Candle, i, h int, entry, R, initSL, trailK float64, pump bool) (float64, bool, int) {
	if entry <= 0 || R <= 0 {
		return 0, false, 0
	}
	end := i + h
	if end >= len(kl) {
		end = len(kl) - 1
	}
	if pump {
		stop := entry - initSL*R
		peak := entry
		for j := i + 1; j <= end; j++ {
			if kl[j].Low <= stop {
				return (stop - entry) / entry * 100, stop > entry, j - i
			}
			if kl[j].High > peak {
				peak = kl[j].High
				if ns := peak - trailK*R; ns > stop {
					stop = ns
				}
			}
		}
		return (kl[end].Close - entry) / entry * 100, kl[end].Close > entry, end - i
	}
	stop := entry + initSL*R
	trough := entry
	for j := i + 1; j <= end; j++ {
		if kl[j].High >= stop {
			return (entry - stop) / entry * 100, stop < entry, j - i
		}
		if kl[j].Low < trough {
			trough = kl[j].Low
			if ns := trough + trailK*R; ns < stop {
				stop = ns
			}
		}
	}
	return (entry - kl[end].Close) / entry * 100, kl[end].Close < entry, end - i
}

// exitAnalysis re-optimises exits on gamble signals (market entry, R = 12-bar
// swing), comparing fixed TP/SL grids, trailing stops and pure time-exit.
func exitAnalysis(ex *exchange.Client, coins []string, fee, gate float64) {
	const step = int64(3600000)
	const H = 24
	type sig struct {
		kl   []exchange.Candle
		i    int
		R    float64
		pump bool
	}
	var sigs []sig
	for _, coin := range coins {
		kl, err := ex.BinanceKlines(coin+"USDT", "1h", 1000)
		if err != nil || len(kl) < 60 {
			continue
		}
		oi, _ := ex.BinanceOIHist(coin+"USDT", "1h", 500)
		if len(oi) < 30 {
			continue
		}
		oiMap := map[int64]float64{}
		for _, p := range oi {
			oiMap[p.Ts/step] = p.SumOIValue
		}
		for i := 48; i < len(kl)-H; i++ {
			hb := kl[i].Ts / step
			now, ok := oiMap[hb]
			past, ok2 := oiMap[hb-12]
			if !ok || !ok2 || past == 0 {
				continue
			}
			oiAccum := pctf(past, now)
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
			accel := pctf(kl[i-3].Close, kl[i].Close)
			cvd := indicator.CVDFromKlines(kl[:i+1], 6)
			earliness := clampf(1-math.Abs(pctf(kl[i-24].Close, kl[i].Close))/68, 0.3, 1)
			if float64(ign(volSpike, oiAccum, 1, accel, cvd, earliness)) < gate {
				continue
			}
			pump := oiAccum >= 0
			if math.Abs(oiAccum) < 1 {
				pump = cvd >= 0
			}
			hi, lo := kl[i].High, kl[i].Low
			for j := i - 11; j <= i; j++ {
				if kl[j].High > hi {
					hi = kl[j].High
				}
				if kl[j].Low < lo {
					lo = kl[j].Low
				}
			}
			sigs = append(sigs, sig{kl, i, hi - lo, pump})
		}
	}

	fmt.Printf("=== 賭博單出場優化 (gate%.0f · 市價進 · R=12根擺盪 · 持%d根 · 費%.2f%% · 訊號 %d) ===\n",
		gate, H, fee, len(sigs))
	stat := func(label string, f func(s sig) (float64, bool)) {
		if len(sigs) == 0 {
			return
		}
		var wins int
		var sum float64
		for _, s := range sigs {
			r, w := f(s)
			sum += r
			if w {
				wins++
			}
		}
		n := float64(len(sigs))
		fmt.Printf("  %-26s 勝率%.1f%%  淨期望%+.3f%%\n", label, float64(wins)/n*100, sum/n-fee)
	}

	fmt.Println("[固定 TP/SL]")
	type g struct{ tp, sl float64 }
	for _, c := range []g{{0.618, 0.5}, {1.0, 0.5}, {1.5, 0.5}, {1.0, 0.75}, {1.5, 0.75}, {2.0, 1.0}, {3.0, 1.0}} {
		lbl := fmt.Sprintf("TP%.3g/SL%.3g", c.tp, c.sl)
		if c.tp == 0.618 && c.sl == 0.5 {
			lbl += "(現用)"
		}
		cc := c
		stat(lbl, func(s sig) (float64, bool) { return simExitG(s.kl, s.i, H, s.kl[s.i].Close, s.R, cc.tp, cc.sl, s.pump) })
	}
	fmt.Println("[移動停損 trailing(初始SL 0.5R)]")
	for _, tk := range []float64{0.5, 0.75, 1.0, 1.5} {
		t := tk
		stat(fmt.Sprintf("trail %.2gR", t), func(s sig) (float64, bool) {
			r, w, _ := simTrail(s.kl, s.i, H, s.kl[s.i].Close, s.R, 0.5, t, s.pump)
			return r, w
		})
	}
	fmt.Println("[純時間出場(無 TP/SL,持滿 24 根)]")
	stat("time-exit", func(s sig) (float64, bool) {
		end := s.i + H
		if end >= len(s.kl) {
			end = len(s.kl) - 1
		}
		r := (s.kl[end].Close - s.kl[s.i].Close) / s.kl[s.i].Close * 100
		if !s.pump {
			r = -r
		}
		return r, r > 0
	})
	fmt.Println("[頭對頭:賭博固定 TP0.618/SL0.5  vs  移動止損 初0.5R/跟蹤1R]")
	detail := func(label string, f func(s sig) (float64, bool)) {
		if len(sigs) == 0 {
			return
		}
		var wins, losses int
		var sumW, sumL, sum, best float64
		for _, s := range sigs {
			r, _ := f(s)
			sum += r
			if r > 0 {
				wins++
				sumW += r
			} else {
				losses++
				sumL += r
			}
			if r > best {
				best = r
			}
		}
		n := float64(len(sigs))
		aw, al := 0.0, 0.0
		if wins > 0 {
			aw = sumW / float64(wins)
		}
		if losses > 0 {
			al = sumL / float64(losses)
		}
		fmt.Printf("  %-12s 勝率%.1f%% 均盈%+.2f%% 均虧%+.2f%% 淨期望%+.3f%% 最佳單%+.1f%% 累計%+.1f%%\n",
			label, float64(wins)/n*100, aw, al, sum/n-fee, best, sum-fee*n)
	}
	detail("賭博固定", func(s sig) (float64, bool) {
		return simExitG(s.kl, s.i, H, s.kl[s.i].Close, s.R, 0.618, 0.5, s.pump)
	})
	detail("移動止損1R", func(s sig) (float64, bool) {
		r, w, _ := simTrail(s.kl, s.i, H, s.kl[s.i].Close, s.R, 0.5, 1.0, s.pump)
		return r, w
	})
	fmt.Println("判讀: 移動止損→均盈/最佳單更大(吃到大噴)、但勝率低、均虧大(回吐);看淨期望與累計誰高")
}

func fmtSpan(d time.Duration) string {
	h := int(d.Hours())
	if h < 48 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dd", h/24)
}

// fundingAnalysis tests whether the funding rate, layered on top of the live
// ignition signal, improves win rate (as a crowding/squeeze filter). Funding
// settles every 8h, so it is a slow regime variable, NOT an intraday earlier
// trigger — this measures it purely as an entry FILTER. OOS: first 70% of each
// coin's bars = train, last 30% = test; a filter only counts if TEST improves.
func fundingAnalysis(ex *exchange.Client, coins []string, fee, g float64, horizon int) {
	const step = int64(3600000)
	H := horizon
	if H <= 0 {
		H = 24
	}
	const extremeAbs = 0.0005 // |funding| ≥ 0.05% per 8h = elevated/crowded

	type acc struct {
		n, wins int
		sum     float64
	}
	add := func(a *acc, r float64, w bool) {
		a.n++
		a.sum += r
		if w {
			a.wins++
		}
	}
	// latest settled funding at-or-before t (carry-forward); funding is sorted oldest→newest
	fundingAt := func(fps []exchange.FundingPoint, t int64) (float64, bool) {
		lo, hi, res := 0, len(fps)-1, -1
		for lo <= hi {
			m := (lo + hi) / 2
			if fps[m].Ts <= t {
				res = m
				lo = m + 1
			} else {
				hi = m - 1
			}
		}
		if res < 0 {
			return 0, false
		}
		return fps[res].Rate, true
	}

	// buckets, each split train/test
	var baseTr, baseTe, fuelTr, fuelTe, crowdTr, crowdTe acc
	var fuelXTr, fuelXTe, skipCXTr, skipCXTe acc

	for _, coin := range coins {
		time.Sleep(150 * time.Millisecond) // 3 fetches/coin — be polite to Binance
		kl, err := ex.BinanceKlines(coin+"USDT", "1h", 1000)
		if err != nil || len(kl) < 60 {
			continue
		}
		oi, _ := ex.BinanceOIHist(coin+"USDT", "1h", 500)
		if len(oi) < 30 {
			continue
		}
		fps, _ := ex.BinanceFundingHist(coin+"USDT", 1000)
		if len(fps) < 10 {
			continue
		}
		oiMap := map[int64]float64{}
		for _, p := range oi {
			oiMap[p.Ts/step] = p.SumOIValue
		}
		cutoff := 48 + int(0.70*float64(len(kl)-H-48))
		for i := 48; i < len(kl)-H; i++ {
			hb := kl[i].Ts / step
			now, ok := oiMap[hb]
			past, ok2 := oiMap[hb-12]
			if !ok || !ok2 || past == 0 {
				continue
			}
			oiAccum := pctf(past, now)
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
			accel := pctf(kl[i-3].Close, kl[i].Close)
			cvd := indicator.CVDFromKlines(kl[:i+1], 6)
			earliness := clampf(1-math.Abs(pctf(kl[i-24].Close, kl[i].Close))/68, 0.3, 1)
			if float64(ign(volSpike, oiAccum, 1, accel, cvd, earliness)) < g {
				continue
			}
			pump := oiAccum >= 0
			if math.Abs(oiAccum) < 1 {
				pump = cvd >= 0
			}
			fund, ok3 := fundingAt(fps, kl[i].Ts)
			if !ok3 {
				continue
			}
			hi, lo := kl[i].High, kl[i].Low
			for j := i - 11; j <= i; j++ {
				if kl[j].High > hi {
					hi = kl[j].High
				}
				if kl[j].Low < lo {
					lo = kl[j].Low
				}
			}
			R := hi - lo
			r, w := simExitG(kl, i, H, kl[i].Close, R, 0.618, 0.5, pump)

			// classify by funding vs trade direction
			fuel := (pump && fund < 0) || (!pump && fund > 0)   // contrarian: squeeze fuel
			crowd := (pump && fund > 0) || (!pump && fund < 0)  // aligned: crowded/late
			extreme := math.Abs(fund) >= extremeAbs
			train := i < cutoff

			if train {
				add(&baseTr, r, w)
			} else {
				add(&baseTe, r, w)
			}
			if fuel {
				if train {
					add(&fuelTr, r, w)
				} else {
					add(&fuelTe, r, w)
				}
				if extreme {
					if train {
						add(&fuelXTr, r, w)
					} else {
						add(&fuelXTe, r, w)
					}
				}
			}
			if crowd {
				if train {
					add(&crowdTr, r, w)
				} else {
					add(&crowdTe, r, w)
				}
			}
			if !(crowd && extreme) { // skip only the crowded-extreme (late+overheated)
				if train {
					add(&skipCXTr, r, w)
				} else {
					add(&skipCXTe, r, w)
				}
			}
		}
	}

	fmt.Printf("=== 資金費率過濾 (1h gate%.0f, 市價進, TP0.618/SL0.5, 持%d根, 費%.2f%%, 極端|f|≥%.2f%%) ===\n",
		g, H, fee, extremeAbs*100)
	fmt.Println("逆勢燃料 = 做多時費率為負(空頭擁擠→軋空) / 做空時費率為正;擁擠 = 費率與方向同邊(追高追低)")
	fmt.Printf("%-22s | %-26s | %-26s\n", "", "訓練(前70%)", "測試(後30% OOS)")
	fmt.Printf("%-22s | %7s %8s %10s | %7s %8s %10s\n", "分組", "訊號", "勝率", "淨期望", "訊號", "勝率", "淨期望")
	pr := func(label string, tr, te acc) {
		f := func(a acc) string {
			if a.n == 0 {
				return fmt.Sprintf("%7d %8s %10s", 0, "-", "-")
			}
			return fmt.Sprintf("%7d %7.1f%% %+9.3f%%", a.n, float64(a.wins)/float64(a.n)*100, a.sum/float64(a.n)-fee)
		}
		fmt.Printf("%-22s | %s | %s\n", label, f(tr), f(te))
	}
	pr("基準(全部)", baseTr, baseTe)
	pr("逆勢燃料", fuelTr, fuelTe)
	pr("逆勢燃料+極端", fuelXTr, fuelXTe)
	pr("擁擠(同向)", crowdTr, crowdTe)
	pr("剔除擁擠+極端", skipCXTr, skipCXTe)
	fmt.Println("判讀: 只有當某分組『測試』勝率/淨期望 > 基準測試、且訊號數沒崩 → 才採用;否則是雜訊或過擬合")
}

// fundLiveAnalysis tests the funding-rate filter against the REAL recorded paper
// trades (a forward, already-out-of-sample sample, larger than the 30-day OI
// backtest). For each closed trade it looks up the funding settled at entry and
// buckets win rate by whether funding was "fuel" (contrarian) or "crowd" (aligned).
func fundLiveAnalysis(ex *exchange.Client) {
	path := os.Getenv("DB_PATH")
	if path == "" {
		path = "datahunter.db"
	}
	db, err := sql.Open("sqlite", path+"?mode=ro&_pragma=busy_timeout(4000)")
	if err != nil {
		fmt.Println("open db:", err)
		return
	}
	defer db.Close()

	type trade struct {
		coin, dir, book string
		open            int64
		pnl             float64
	}
	rows, err := db.Query(`SELECT book,coin,dir,open_time,pnl_pct FROM paper_trades WHERE status='closed'`)
	if err != nil {
		fmt.Println("query:", err)
		return
	}
	var trades []trade
	coinSet := map[string]bool{}
	for rows.Next() {
		var t trade
		if rows.Scan(&t.book, &t.coin, &t.dir, &t.open, &t.pnl) == nil {
			trades = append(trades, t)
			coinSet[t.coin] = true
		}
	}
	rows.Close()
	fmt.Printf("讀到 %d 筆已平倉實單,%d 個幣種,抓取資金費率歷史...\n", len(trades), len(coinSet))

	fundMap := map[string][]exchange.FundingPoint{}
	for c := range coinSet {
		time.Sleep(120 * time.Millisecond)
		fp, _ := ex.BinanceFundingHist(c+"USDT", 1000)
		if len(fp) > 0 {
			sort.Slice(fp, func(i, j int) bool { return fp[i].Ts < fp[j].Ts })
			fundMap[c] = fp
		}
	}
	fundingAt := func(fps []exchange.FundingPoint, t int64) (float64, bool) {
		lo, hi, res := 0, len(fps)-1, -1
		for lo <= hi {
			m := (lo + hi) / 2
			if fps[m].Ts <= t {
				res = m
				lo = m + 1
			} else {
				hi = m - 1
			}
		}
		if res < 0 {
			return 0, false
		}
		return fps[res].Rate, true
	}

	type acc struct {
		n, wins int
		sum     float64
	}
	add := func(a *acc, pnl float64) {
		a.n++
		a.sum += pnl
		if pnl > 0 {
			a.wins++
		}
	}
	report := func(book string) {
		var base, fuel, crowd, fuelX, skipCX acc
		const extremeAbs = 0.0005
		matched := 0
		for _, t := range trades {
			if book != "all" && t.book != book {
				continue
			}
			fps, ok := fundMap[t.coin]
			if !ok {
				continue
			}
			fund, ok2 := fundingAt(fps, t.open)
			if !ok2 {
				continue
			}
			matched++
			pump := t.dir == "long"
			fuelB := (pump && fund < 0) || (!pump && fund > 0)
			crowdB := (pump && fund > 0) || (!pump && fund < 0)
			extreme := math.Abs(fund) >= extremeAbs
			add(&base, t.pnl)
			if fuelB {
				add(&fuel, t.pnl)
				if extreme {
					add(&fuelX, t.pnl)
				}
			}
			if crowdB {
				add(&crowd, t.pnl)
			}
			if !(crowdB && extreme) {
				add(&skipCX, t.pnl)
			}
		}
		label := map[string]string{"gamble": "動能狙擊倉", "main": "紀律倉", "all": "全部"}[book]
		fmt.Printf("\n=== %s(對到資金費率 %d 筆) ===\n", label, matched)
		fmt.Printf("%-18s %7s %8s %10s\n", "分組", "筆數", "勝率", "均損益")
		pr := func(name string, a acc) {
			if a.n == 0 {
				fmt.Printf("%-18s %7d %8s %10s\n", name, 0, "-", "-")
				return
			}
			fmt.Printf("%-18s %7d %7.1f%% %+9.3f%%\n", name, a.n, float64(a.wins)/float64(a.n)*100, a.sum/float64(a.n))
		}
		pr("基準(全部)", base)
		pr("逆勢燃料", fuel)
		pr("逆勢燃料+極端", fuelX)
		pr("擁擠(同向)", crowd)
		pr("剔除擁擠+極端", skipCX)
	}
	report("gamble")
	report("main")
	fmt.Println("\n判讀: 逆勢燃料/剔除擁擠 的勝率與均損益若穩定 > 基準 且筆數夠 → 值得加入過濾;反之無效")
}

// outcomesAnalysis breaks down the REAL recorded trades by exit outcome to
// quantify how much of the damage comes from "expired" (timeout-bleed) losers —
// the ones a break-even / time-stop could have cut short. Pure SQLite, no network.
func outcomesAnalysis() {
	path := os.Getenv("DB_PATH")
	if path == "" {
		path = "datahunter.db"
	}
	db, err := sql.Open("sqlite", path+"?mode=ro&_pragma=busy_timeout(4000)")
	if err != nil {
		fmt.Println("open db:", err)
		return
	}
	defer db.Close()
	type row struct {
		book, outcome string
		pnl, hrs      float64
	}
	rows, _ := db.Query(`SELECT book,outcome,pnl_pct,(close_time-open_time)/3600000.0 FROM paper_trades WHERE status='closed'`)
	var all []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.book, &r.outcome, &r.pnl, &r.hrs) == nil {
			all = append(all, r)
		}
	}
	rows.Close()

	for _, book := range []string{"gamble", "main"} {
		var br []row
		for _, r := range all {
			if r.book == book {
				br = append(br, r)
			}
		}
		if len(br) == 0 {
			continue
		}
		type agg struct {
			n, wins    int
			sum, hrs   float64
		}
		m := map[string]*agg{}
		var tot float64
		var totWins int
		for _, r := range br {
			a := m[r.outcome]
			if a == nil {
				a = &agg{}
				m[r.outcome] = a
			}
			a.n++
			a.sum += r.pnl
			a.hrs += r.hrs
			if r.pnl > 0 {
				a.wins++
				totWins++
			}
			tot += r.pnl
		}
		label := map[string]string{"gamble": "動能狙擊倉", "main": "紀律倉"}[book]
		fmt.Printf("\n===== %s (已結束 %d 筆) =====\n", label, len(br))
		fmt.Printf("%-10s %4s %6s %9s %9s %9s %7s\n", "出場", "筆數", "勝率", "均損益", "累計", "佔總累計", "均持時")
		// stable order: expired, sl, tp, reversed, others
		order := []string{"expired", "sl", "tp", "reversed", "trail"}
		seen := map[string]bool{}
		printOne := func(k string, a *agg) {
			share := 0.0
			if tot != 0 {
				share = a.sum / tot * 100
			}
			fmt.Printf("%-10s %4d %5.0f%% %+8.2f%% %+8.1f%% %8.0f%% %6.1fh\n",
				k, a.n, float64(a.wins)/float64(a.n)*100, a.sum/float64(a.n), a.sum, share, a.hrs/float64(a.n))
		}
		for _, k := range order {
			if a := m[k]; a != nil {
				printOne(k, a)
				seen[k] = true
			}
		}
		for k, a := range m {
			if !seen[k] {
				printOne(k, a)
			}
		}
		fmt.Printf("%-10s %4d %5.0f%% %+8.2f%% %+8.1f%%\n", "TOTAL", len(br), float64(totWins)/float64(len(br))*100, tot/float64(len(br)), tot)

		// expired split: winners vs losers (losers = the bleeders)
		var exL, exW []row
		for _, r := range br {
			if r.outcome == "expired" {
				if r.pnl <= 0 {
					exL = append(exL, r)
				} else {
					exW = append(exW, r)
				}
			}
		}
		if len(exL)+len(exW) > 0 {
			sumL, sumW := 0.0, 0.0
			for _, r := range exL {
				sumL += r.pnl
			}
			for _, r := range exW {
				sumW += r.pnl
			}
			avgL := 0.0
			if len(exL) > 0 {
				avgL = sumL / float64(len(exL))
			}
			avgW := 0.0
			if len(exW) > 0 {
				avgW = sumW / float64(len(exW))
			}
			fmt.Printf("  逾時拆解: 虧損 %d 單(均%+.2f%%,累計%+.1f%%) | 獲利 %d 單(均%+.2f%%)\n",
				len(exL), avgL, sumL, len(exW), avgW)
		}
	}
	fmt.Println("\n判讀: 若『expired 虧損』累計佔總虧損很大 → break-even/時間停損 有很大改善空間(待 IP 解封跑路徑回測驗證)")
}

// exTrade is one recorded trade for the early-exit replay.
type exTrade struct {
	book, coin, dir    string
	entry, tp, sl, pnl float64
	open               int64
}

// replayExit walks a trade's 1H path and returns the directional pnl% under one
// exit rule: optional break-even arming (after +kR favourable, move stop to
// entry) and optional time-stop (force-exit at bar nbars; 0 = none). TP/SL are
// the trade's actual levels; this is identical to the live rule when be=false,
// nbars=0 → reproduces the recorded outcome (a sanity check).
func replayExit(kl []exchange.Candle, t exTrade, be bool, k float64, nbars int) (pnl float64, win, ok bool) {
	if len(kl) == 0 || t.entry <= 0 || kl[0].Ts > t.open {
		return 0, false, false
	}
	i0 := -1
	for i := 0; i < len(kl) && kl[i].Ts <= t.open; i++ {
		i0 = i
	}
	if i0 < 0 || i0+1 >= len(kl) {
		return 0, false, false
	}
	end := i0 + 24 // live book expires at 24 bars (24h)
	if end >= len(kl) {
		end = len(kl) - 1
	}
	pump := t.dir == "long"
	risk := math.Abs(t.entry - t.sl)
	ex := func(px float64) (float64, bool) {
		var r float64
		if pump {
			r = (px - t.entry) / t.entry * 100
		} else {
			r = (t.entry - px) / t.entry * 100
		}
		return r, r > 0
	}
	stop, armed := t.sl, false
	armLvl := t.entry + k*risk
	if !pump {
		armLvl = t.entry - k*risk
	}
	for j := i0 + 1; j <= end; j++ {
		if pump {
			if kl[j].Low <= stop {
				r, w := ex(stop)
				return r, w, true
			}
			if kl[j].High >= t.tp {
				r, w := ex(t.tp)
				return r, w, true
			}
		} else {
			if kl[j].High >= stop {
				r, w := ex(stop)
				return r, w, true
			}
			if kl[j].Low <= t.tp {
				r, w := ex(t.tp)
				return r, w, true
			}
		}
		if be && !armed { // arm break-even at bar close
			if (pump && kl[j].High >= armLvl) || (!pump && kl[j].Low <= armLvl) {
				armed, stop = true, t.entry
			}
		}
		if nbars > 0 && j-i0 >= nbars { // time-stop
			r, w := ex(kl[j].Close)
			return r, w, true
		}
	}
	r, w := ex(kl[end].Close)
	return r, w, true
}

// earlyExitAnalysis reconstructs each REAL recorded trade's 1H price path from
// Binance klines and replays alternative exits. Same trades + entries, only the
// exit differs → apples-to-apples vs the live fixed-TP/SL + 24h-expiry rule.
func earlyExitAnalysis(ex *exchange.Client) {
	path := os.Getenv("DB_PATH")
	if path == "" {
		path = "datahunter.db"
	}
	db, err := sql.Open("sqlite", path+"?mode=ro&_pragma=busy_timeout(4000)")
	if err != nil {
		fmt.Println("open db:", err)
		return
	}
	defer db.Close()
	rows, _ := db.Query(`SELECT book,coin,dir,entry,tp,sl,pnl_pct,open_time FROM paper_trades WHERE status='closed'`)
	var trades []exTrade
	coins := map[string]bool{}
	for rows.Next() {
		var t exTrade
		if rows.Scan(&t.book, &t.coin, &t.dir, &t.entry, &t.tp, &t.sl, &t.pnl, &t.open) == nil {
			trades = append(trades, t)
			coins[t.coin] = true
		}
	}
	rows.Close()
	fmt.Printf("讀到 %d 筆已平倉單,%d 幣;抓 1H K 線重建走勢...\n", len(trades), len(coins))

	klMap := map[string][]exchange.Candle{}
	for c := range coins {
		time.Sleep(250 * time.Millisecond)
		if kl, err := ex.BinanceKlines(c+"USDT", "1h", 1000); err == nil && len(kl) >= 30 {
			klMap[c] = kl
		}
	}

	type variant struct {
		name  string
		be    bool
		k     float64
		nbars int
	}
	variants := []variant{
		{"baseline(複現)", false, 0, 0},
		{"保本@0.5R", true, 0.5, 0},
		{"保本@1.0R", true, 1.0, 0},
		{"時間停損 8h", false, 0, 8},
		{"時間停損 12h", false, 0, 12},
		{"保本1R+時停12h", true, 1.0, 12},
	}
	type acc struct {
		n, wins int
		sum     float64
	}
	for _, book := range []string{"gamble", "main"} {
		res := make([]acc, len(variants))
		var actSum float64
		var matched int
		for _, t := range trades {
			if t.book != book {
				continue
			}
			kl := klMap[t.coin]
			if _, _, ok := replayExit(kl, t, false, 0, 0); !ok {
				continue
			}
			matched++
			actSum += t.pnl
			for vi, v := range variants {
				r, w, _ := replayExit(kl, t, v.be, v.k, v.nbars)
				res[vi].n++
				res[vi].sum += r
				if w {
					res[vi].wins++
				}
			}
		}
		label := map[string]string{"gamble": "動能狙擊倉", "main": "紀律倉"}[book]
		fmt.Printf("\n===== %s (重建 %d 筆;實際紀錄累計 %+.1f%%) =====\n", label, matched, actSum)
		fmt.Printf("%-18s %5s %7s %9s %9s\n", "出場規則", "筆數", "勝率", "均損益", "累計")
		for vi, v := range variants {
			a := res[vi]
			if a.n == 0 {
				continue
			}
			fmt.Printf("%-18s %5d %6.1f%% %+8.2f%% %+8.1f%%\n",
				v.name, a.n, float64(a.wins)/float64(a.n)*100, a.sum/float64(a.n), a.sum)
		}
	}
	fmt.Println("\n判讀: baseline 應≈實際紀錄(複現正確);再看哪個變體『累計』最高且勝率沒崩 → 值得實作")
}

// oiCvdAnalysis answers: among ignition>=gate entries where OI is positive
// (new positions building → a long/pump bias), does CVD's sign at entry matter?
// "OI+ CVD+" = OI building AND taker-buying confirming; "OI+ CVD-" = OI building
// but taker-SELLING (divergence). Both traded long, fixed TP0.618/SL0.5, holding
// H bars. OOS: first 70% of each coin's bars = train, last 30% = test.
func oiCvdAnalysis(ex *exchange.Client, coins []string, fee, g float64, horizon int) {
	const step = int64(3600000)
	H := horizon
	if H <= 0 {
		H = 24
	}

	// --- real recorded trades first (big sample, actual outcomes, no network) ---
	// trades persist OI% and CVD% at entry, so we can split them directly.
	dbp := os.Getenv("DB_PATH")
	if dbp == "" {
		dbp = "datahunter.db"
	}
	if db, err := sql.Open("sqlite", dbp+"?mode=ro&_pragma=busy_timeout(4000)"); err == nil {
		type rt struct {
			book, dir    string
			oi, cvd, pnl float64
		}
		var trs []rt
		rows, _ := db.Query(`SELECT book,dir,oi,cvd,pnl_pct FROM paper_trades WHERE status='closed' AND oi<>0`)
		for rows.Next() {
			var r rt
			if rows.Scan(&r.book, &r.dir, &r.oi, &r.cvd, &r.pnl) == nil {
				trs = append(trs, r)
			}
		}
		rows.Close()
		db.Close()
		fmt.Printf("=== 真實單:OI正進場,CVD 正 vs 負 (%d 筆有 OI/CVD 紀錄) ===\n", len(trs))
		for _, book := range []string{"gamble", "main"} {
			type ag struct {
				n, wins, longs int
				sum            float64
			}
			var pos, neg ag
			addr := func(a *ag, r rt) {
				a.n++
				a.sum += r.pnl
				if r.pnl > 0 {
					a.wins++
				}
				if r.dir == "long" {
					a.longs++
				}
			}
			for _, r := range trs {
				if r.book != book || r.oi <= 0 {
					continue
				}
				if r.cvd >= 0 {
					addr(&pos, r)
				} else {
					addr(&neg, r)
				}
			}
			fmt.Printf("[%s]\n", map[string]string{"gamble": "動能狙擊倉", "main": "紀律倉"}[book])
			prr := func(name string, a ag) {
				if a.n == 0 {
					fmt.Printf("  %-10s 無\n", name)
					return
				}
				fmt.Printf("  %-10s %3d 單(多%d/空%d) 勝率%.1f%% 均損益%+.2f%% 累計%+.1f%%\n",
					name, a.n, a.longs, a.n-a.longs, float64(a.wins)/float64(a.n)*100, a.sum/float64(a.n), a.sum)
			}
			prr("OI+ CVD+", pos)
			prr("OI+ CVD−", neg)
		}
		fmt.Println()
	}

	type acc struct {
		n, wins int
		sum     float64
	}
	add := func(a *acc, r float64, w bool) {
		a.n++
		a.sum += r
		if w {
			a.wins++
		}
	}
	var posTr, posTe, negTr, negTe acc
	for _, coin := range coins {
		time.Sleep(200 * time.Millisecond) // 2 fetches/coin — be polite
		kl, err := ex.BinanceKlines(coin+"USDT", "1h", 1000)
		if err != nil || len(kl) < 60 {
			continue
		}
		oi, _ := ex.BinanceOIHist(coin+"USDT", "1h", 500)
		if len(oi) < 30 {
			continue
		}
		oiMap := map[int64]float64{}
		for _, p := range oi {
			oiMap[p.Ts/step] = p.SumOIValue
		}
		cutoff := 48 + int(0.70*float64(len(kl)-H-48))
		for i := 48; i < len(kl)-H; i++ {
			hb := kl[i].Ts / step
			now, ok := oiMap[hb]
			past, ok2 := oiMap[hb-12]
			if !ok || !ok2 || past == 0 {
				continue
			}
			oiAccum := pctf(past, now)
			if oiAccum <= 0 { // require OI positive (the user's premise)
				continue
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
			accel := pctf(kl[i-3].Close, kl[i].Close)
			cvd := indicator.CVDFromKlines(kl[:i+1], 6)
			earliness := clampf(1-math.Abs(pctf(kl[i-24].Close, kl[i].Close))/68, 0.3, 1)
			if float64(ign(volSpike, oiAccum, 1, accel, cvd, earliness)) < g {
				continue
			}
			hi, lo := kl[i].High, kl[i].Low
			for j := i - 11; j <= i; j++ {
				if kl[j].High > hi {
					hi = kl[j].High
				}
				if kl[j].Low < lo {
					lo = kl[j].Low
				}
			}
			R := hi - lo
			r, w := simExitG(kl, i, H, kl[i].Close, R, 0.618, 0.5, true) // OI+ → long
			train := i < cutoff
			if cvd >= 0 {
				if train {
					add(&posTr, r, w)
				} else {
					add(&posTe, r, w)
				}
			} else {
				if train {
					add(&negTr, r, w)
				} else {
					add(&negTe, r, w)
				}
			}
		}
	}
	fmt.Printf("=== OI正 進場,CVD 正 vs 負 (gate%.0f, 做多, 市價進, TP0.618/SL0.5, 持%d根, 費%.2f%%) ===\n", g, H, fee)
	fmt.Printf("%-14s | %-26s | %-26s\n", "", "訓練(前70%)", "測試(後30% OOS)")
	fmt.Printf("%-14s | %7s %8s %10s | %7s %8s %10s\n", "分組", "訊號", "勝率", "淨期望", "訊號", "勝率", "淨期望")
	pr := func(label string, tr, te acc) {
		f := func(a acc) string {
			if a.n == 0 {
				return fmt.Sprintf("%7d %8s %10s", 0, "-", "-")
			}
			return fmt.Sprintf("%7d %7.1f%% %+9.3f%%", a.n, float64(a.wins)/float64(a.n)*100, a.sum/float64(a.n)-fee)
		}
		fmt.Printf("%-14s | %s | %s\n", label, f(tr), f(te))
	}
	pr("OI+ CVD+", posTr, posTe)
	pr("OI+ CVD−", negTr, negTe)
	// combined totals
	tot := func(a, b acc) acc { return acc{a.n + b.n, a.wins + b.wins, a.sum + b.sum} }
	allP, allN := tot(posTr, posTe), tot(negTr, negTe)
	winr := func(a acc) float64 {
		if a.n == 0 {
			return 0
		}
		return float64(a.wins) / float64(a.n) * 100
	}
	exp := func(a acc) float64 {
		if a.n == 0 {
			return 0
		}
		return a.sum/float64(a.n) - fee
	}
	fmt.Printf("\n全樣本: OI+CVD+ %d單 勝率%.1f%% 淨期望%+.3f%%  |  OI+CVD− %d單 勝率%.1f%% 淨期望%+.3f%%\n",
		allP.n, winr(allP), exp(allP), allN.n, winr(allN), exp(allN))
	fmt.Println("判讀: 若 CVD+ 勝率/期望明顯 > CVD−,且測試段也成立 → CVD 同向是有效的進場確認")
}

// matrixAnalysis tests the classic price/OI/CVD reading: when PRICE and CVD agree
// (trend + aggressive flow point the same way), does OI rising (new positions /
// conviction) really beat OI falling (positions closing / exhaustion)?
//   bull = price↑ & CVD+ → long;  bear = price↓ & CVD− → short.
// Each split by OI↑ (強, new money) vs OI↓ (弱, closing). Entries trade with the
// book's exit (TP0.618/SL0.5, R=12-bar swing). OOS: 70% train / 30% test.
func matrixAnalysis(ex *exchange.Client, coins []string, fee float64, horizon int) {
	const step = int64(3600000)
	const W = 6        // window (bars) for price & OI direction
	const minMove = 1.0 // require |price move| ≥ 1% to count as a real move
	H := horizon
	if H <= 0 {
		H = 24
	}
	type acc struct {
		n, wins int
		sum     float64
	}
	add := func(a *acc, r float64, w bool) {
		a.n++
		a.sum += r
		if w {
			a.wins++
		}
	}
	var sLongTr, sLongTe, wLongTr, wLongTe acc            // strong/weak long
	var sShortTr, sShortTe, wShortTr, wShortTe acc        // strong/weak short
	var wLongRevTr, wLongRevTe, wShortRevTr, wShortRevTe acc // FADE the weak (exhaustion) cells
	for _, coin := range coins {
		time.Sleep(200 * time.Millisecond)
		kl, err := ex.BinanceKlines(coin+"USDT", "1h", 1000)
		if err != nil || len(kl) < 60 {
			continue
		}
		oi, _ := ex.BinanceOIHist(coin+"USDT", "1h", 500)
		if len(oi) < 30 {
			continue
		}
		oiMap := map[int64]float64{}
		for _, p := range oi {
			oiMap[p.Ts/step] = p.SumOIValue
		}
		cutoff := 48 + int(0.70*float64(len(kl)-H-48))
		for i := 48; i < len(kl)-H; i++ {
			hb := kl[i].Ts / step
			now, ok := oiMap[hb]
			past, ok2 := oiMap[hb-W]
			if !ok || !ok2 || past == 0 {
				continue
			}
			oiUp := pctf(past, now) > 0
			pchg := pctf(kl[i-W].Close, kl[i].Close)
			if math.Abs(pchg) < minMove {
				continue
			}
			cvd := indicator.CVDFromKlines(kl[:i+1], 6)
			bull := pchg > 0 && cvd > 0
			bear := pchg < 0 && cvd < 0
			if !bull && !bear { // price & CVD must agree (skip divergence cells)
				continue
			}
			hi, lo := kl[i].High, kl[i].Low
			for j := i - 11; j <= i; j++ {
				if kl[j].High > hi {
					hi = kl[j].High
				}
				if kl[j].Low < lo {
					lo = kl[j].Low
				}
			}
			R := hi - lo
			r, w := simExitG(kl, i, H, kl[i].Close, R, 0.618, 0.5, bull)
			rv, wv := simExitG(kl, i, H, kl[i].Close, R, 0.618, 0.5, !bull) // FADE (reverse) the cell
			train := i < cutoff
			switch {
			case bull && oiUp:
				if train {
					add(&sLongTr, r, w)
				} else {
					add(&sLongTe, r, w)
				}
			case bull && !oiUp: // 弱多/回補 — fade = go short
				if train {
					add(&wLongTr, r, w)
					add(&wLongRevTr, rv, wv)
				} else {
					add(&wLongTe, r, w)
					add(&wLongRevTe, rv, wv)
				}
			case bear && oiUp:
				if train {
					add(&sShortTr, r, w)
				} else {
					add(&sShortTe, r, w)
				}
			default: // 弱空/去槓桿 — fade = go long
				if train {
					add(&wShortTr, r, w)
					add(&wShortRevTr, rv, wv)
				} else {
					add(&wShortTe, r, w)
					add(&wShortRevTe, rv, wv)
				}
			}
		}
	}
	fmt.Printf("=== 價/OI/CVD 判斷表回測 (價&CVD同向, 視窗%d根, |動|≥%.0f%%, TP0.618/SL0.5, 持%d根, 費%.2f%%) ===\n",
		W, minMove, H, fee)
	fmt.Printf("%-22s | %-26s | %-26s\n", "", "訓練(前70%)", "測試(後30% OOS)")
	fmt.Printf("%-22s | %7s %8s %10s | %7s %8s %10s\n", "格子", "訊號", "勝率", "淨期望", "訊號", "勝率", "淨期望")
	pr := func(label string, tr, te acc) {
		f := func(a acc) string {
			if a.n == 0 {
				return fmt.Sprintf("%7d %8s %10s", 0, "-", "-")
			}
			return fmt.Sprintf("%7d %7.1f%% %+9.3f%%", a.n, float64(a.wins)/float64(a.n)*100, a.sum/float64(a.n)-fee)
		}
		fmt.Printf("%-22s | %s | %s\n", label, f(tr), f(te))
	}
	pr("強多 價↑OI↑CVD↑", sLongTr, sLongTe)
	pr("弱多 價↑OI↓CVD↑(回補)", wLongTr, wLongTe)
	pr("強空 價↓OI↑CVD↓", sShortTr, sShortTe)
	pr("弱空 價↓OI↓CVD↓(去槓桿)", wShortTr, wShortTe)
	fmt.Println("--- 反做衰竭格(fade):弱多→做空 / 弱空→做多 ---")
	pr("反做弱多(=做空)", wLongRevTr, wLongRevTe)
	pr("反做弱空(=做多)", wShortRevTr, wShortRevTe)
	fmt.Println("判讀: 順勢看『強>弱』是否成立;反做看『弱格反做』勝率/期望是否 > 同格順做 → 反轉策略較優")
}

// recentAnalysis dumps recent closed gamble trades with entry OI/CVD, whether
// they were OI+CVD-aligned, funding, hold time and outcome — to see if a bad
// day's losers share a common factor (divergence / long-cluster / drag time).
func recentAnalysis() {
	path := os.Getenv("DB_PATH")
	if path == "" {
		path = "datahunter.db"
	}
	db, err := sql.Open("sqlite", path+"?mode=ro&_pragma=busy_timeout(4000)")
	if err != nil {
		fmt.Println("open db:", err)
		return
	}
	defer db.Close()
	rows, _ := db.Query(`SELECT coin,dir,oi,cvd,funding,outcome,pnl_pct,open_time,close_time
	  FROM paper_trades WHERE book='gamble' AND status='closed' ORDER BY close_time DESC LIMIT 30`)
	fmt.Printf("%-10s %-5s %8s %7s %9s %-8s %8s %7s %-7s\n",
		"幣", "方向", "OI%", "CVD%", "費率%", "結果", "損益%", "持時h", "同向?")
	type ag struct {
		n, wins int
		sum     float64
	}
	var al, di, longs, shorts ag
	addw := func(a *ag, p float64) {
		a.n++
		a.sum += p
		if p > 0 {
			a.wins++
		}
	}
	for rows.Next() {
		var coin, dir, outcome string
		var oi, cvd, fund, pnl float64
		var ot, ct int64
		if rows.Scan(&coin, &dir, &oi, &cvd, &fund, &outcome, &pnl, &ot, &ct) != nil {
			continue
		}
		aligned := (dir == "long" && oi > 0 && cvd > 0) || (dir == "short" && oi < 0 && cvd < 0)
		hold := float64(ct-ot) / 3600000.0
		tag := "背離"
		if aligned {
			tag = "同向"
		}
		if oi == 0 && cvd == 0 {
			tag = "—" // no OI/CVD recorded (old)
		}
		fmt.Printf("%-10s %-5s %+7.2f %+6.2f %+8.4f %-8s %+7.2f %6.1f %-7s\n",
			coin, dir, oi, cvd, fund*100, outcome, pnl, hold, tag)
		if oi != 0 || cvd != 0 {
			if aligned {
				addw(&al, pnl)
			} else {
				addw(&di, pnl)
			}
		}
		if dir == "long" {
			addw(&longs, pnl)
		} else {
			addw(&shorts, pnl)
		}
	}
	rows.Close()
	wr := func(a ag) string {
		if a.n == 0 {
			return "無"
		}
		return fmt.Sprintf("%d單 勝率%.0f%% 累計%+.1f%%", a.n, float64(a.wins)/float64(a.n)*100, a.sum)
	}
	fmt.Printf("\n同向(OI+CVD一致): %s\n背離(OI/CVD相反): %s\n做多: %s\n做空: %s\n",
		wr(al), wr(di), wr(longs), wr(shorts))
}

// extendAnalysis splits REAL gamble trades by how far OI had already spiked at
// entry. If big-OI-spike entries (late / over-extended) do worse, the entry
// "point" optimisation is to enter earlier / skip over-extended chases.
func extendAnalysis() {
	path := os.Getenv("DB_PATH")
	if path == "" {
		path = "datahunter.db"
	}
	db, err := sql.Open("sqlite", path+"?mode=ro&_pragma=busy_timeout(4000)")
	if err != nil {
		fmt.Println("open db:", err)
		return
	}
	defer db.Close()
	rows, _ := db.Query(`SELECT oi,cvd,pnl_pct FROM paper_trades
	  WHERE book='gamble' AND status='closed' AND oi<>0`)
	type bucket struct {
		label    string
		lo, hi   float64
		n, wins  int
		sum      float64
	}
	// |OI| spike magnitude buckets
	bk := []*bucket{
		{label: "OI 0-10%", lo: 0, hi: 10},
		{label: "OI 10-25%", lo: 10, hi: 25},
		{label: "OI 25-50%", lo: 25, hi: 50},
		{label: "OI 50-100%", lo: 50, hi: 100},
		{label: "OI >100%", lo: 100, hi: 1e9},
	}
	// also split aligned vs divergence within each, to separate the two effects
	type ad struct{ aln, div bucket }
	mag := func(v float64) *bucket {
		a := v
		if a < 0 {
			a = -a
		}
		for _, b := range bk {
			if a >= b.lo && a < b.hi {
				return b
			}
		}
		return bk[len(bk)-1]
	}
	for rows.Next() {
		var oi, cvd, pnl float64
		if rows.Scan(&oi, &cvd, &pnl) != nil {
			continue
		}
		b := mag(oi)
		b.n++
		b.sum += pnl
		if pnl > 0 {
			b.wins++
		}
		_ = cvd
		_ = ad{}
	}
	rows.Close()
	fmt.Println("=== 動能狙擊單:依進場時 OI 噴幅分組(過度延伸測試) ===")
	fmt.Printf("%-12s %6s %7s %10s %10s\n", "OI 噴幅", "筆數", "勝率", "均損益", "累計")
	for _, b := range bk {
		if b.n == 0 {
			fmt.Printf("%-12s %6d\n", b.label, 0)
			continue
		}
		fmt.Printf("%-12s %6d %6.0f%% %+9.2f%% %+9.1f%%\n",
			b.label, b.n, float64(b.wins)/float64(b.n)*100, b.sum/float64(b.n), b.sum)
	}
	fmt.Println("判讀: 若 OI 噴幅越大、勝率/均損益越差 → 進場太晚(追過度延伸),『早進/設 OI 上限』有意義")
}

// premiumAnalysis stacks the two validated, DB-computable filters — OI/CVD
// alignment and funding "fuel" (contrarian funding) — to see whether the
// intersection is a smaller, higher-quality subset of the gamble book.
func premiumAnalysis() {
	path := os.Getenv("DB_PATH")
	if path == "" {
		path = "datahunter.db"
	}
	db, err := sql.Open("sqlite", path+"?mode=ro&_pragma=busy_timeout(4000)")
	if err != nil {
		fmt.Println("open db:", err)
		return
	}
	defer db.Close()
	type t struct {
		dir            string
		oi, cvd, f, pnl float64
	}
	var trs []t
	rows, _ := db.Query(`SELECT dir,oi,cvd,funding,pnl_pct FROM paper_trades
	  WHERE book='gamble' AND status='closed' AND oi<>0`)
	nzFund := 0
	for rows.Next() {
		var r t
		if rows.Scan(&r.dir, &r.oi, &r.cvd, &r.f, &r.pnl) == nil {
			trs = append(trs, r)
			if r.f != 0 {
				nzFund++
			}
		}
	}
	rows.Close()

	type acc struct {
		n, wins int
		sum     float64
	}
	add := func(a *acc, p float64) {
		a.n++
		a.sum += p
		if p > 0 {
			a.wins++
		}
	}
	aligned := func(r t) bool {
		return (r.dir == "long" && r.oi > 0 && r.cvd > 0) || (r.dir == "short" && r.oi < 0 && r.cvd < 0)
	}
	fuel := func(r t) bool { // contrarian funding = squeeze fuel
		return (r.dir == "long" && r.f < 0) || (r.dir == "short" && r.f > 0)
	}
	var base, aln, fu, both acc
	for _, r := range trs {
		add(&base, r.pnl)
		a, f := aligned(r), fuel(r)
		if a {
			add(&aln, r.pnl)
		}
		if f {
			add(&fu, r.pnl)
		}
		if a && f {
			add(&both, r.pnl)
		}
	}
	fmt.Printf("=== 動能狙擊單:疊加過濾精選層 (%d 筆有 OI/CVD;其中 %d 筆有非零費率) ===\n", len(trs), nzFund)
	fmt.Printf("%-20s %6s %7s %10s %10s\n", "子集", "筆數", "勝率", "均損益", "累計")
	pr := func(label string, a acc) {
		if a.n == 0 {
			fmt.Printf("%-20s %6d\n", label, 0)
			return
		}
		fmt.Printf("%-20s %6d %6.0f%% %+9.2f%% %+9.1f%%\n",
			label, a.n, float64(a.wins)/float64(a.n)*100, a.sum/float64(a.n), a.sum)
	}
	pr("全部(基準)", base)
	pr("同向", aln)
	pr("費率燃料", fu)
	pr("同向 + 費率燃料", both)
	fmt.Println("判讀: 疊加層若勝率/均損益明顯 > 基準且筆數還夠用 → 精選層成立(代價是單量變少)")
}

// premiumBtAnalysis is the KLINE-backtest version of the premium stack: it
// generates ignition signals (gate g), then computes OI/CVD alignment and
// funding "fuel" at each signal from real history, and buckets base / aligned /
// fuel / both, OOS-split. Bigger sample than the recorded trades (no dedup), but
// capped by the ~30-day OI window. Funding settles every 8h (carry-forward).
func premiumBtAnalysis(ex *exchange.Client, coins []string, fee, g float64, horizon int) {
	const step = int64(3600000)
	H := horizon
	if H <= 0 {
		H = 24
	}
	type acc struct {
		n, wins int
		sum     float64
	}
	add := func(a *acc, r float64, w bool) {
		a.n++
		a.sum += r
		if w {
			a.wins++
		}
	}
	fundingAt := func(fps []exchange.FundingPoint, t int64) (float64, bool) {
		lo, hi, res := 0, len(fps)-1, -1
		for lo <= hi {
			m := (lo + hi) / 2
			if fps[m].Ts <= t {
				res = m
				lo = m + 1
			} else {
				hi = m - 1
			}
		}
		if res < 0 {
			return 0, false
		}
		return fps[res].Rate, true
	}
	var baseTr, baseTe, alnTr, alnTe, fuTr, fuTe, bothTr, bothTe acc
	for _, coin := range coins {
		time.Sleep(250 * time.Millisecond) // 3 fetches/coin — avoid re-ban
		kl, err := ex.BinanceKlines(coin+"USDT", "1h", 1000)
		if err != nil || len(kl) < 60 {
			continue
		}
		oi, _ := ex.BinanceOIHist(coin+"USDT", "1h", 500)
		if len(oi) < 30 {
			continue
		}
		fps, _ := ex.BinanceFundingHist(coin+"USDT", 1000)
		oiMap := map[int64]float64{}
		for _, p := range oi {
			oiMap[p.Ts/step] = p.SumOIValue
		}
		cutoff := 48 + int(0.70*float64(len(kl)-H-48))
		for i := 48; i < len(kl)-H; i++ {
			hb := kl[i].Ts / step
			now, ok := oiMap[hb]
			past, ok2 := oiMap[hb-12]
			if !ok || !ok2 || past == 0 {
				continue
			}
			oiAccum := pctf(past, now)
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
			accel := pctf(kl[i-3].Close, kl[i].Close)
			cvd := indicator.CVDFromKlines(kl[:i+1], 6)
			earliness := clampf(1-math.Abs(pctf(kl[i-24].Close, kl[i].Close))/68, 0.3, 1)
			if float64(ign(volSpike, oiAccum, 1, accel, cvd, earliness)) < g {
				continue
			}
			pump := oiAccum >= 0
			if math.Abs(oiAccum) < 1 {
				pump = cvd >= 0
			}
			aln := (pump && oiAccum > 0 && cvd > 0) || (!pump && oiAccum < 0 && cvd < 0)
			fund, okf := fundingAt(fps, kl[i].Ts)
			fuel := okf && ((pump && fund < 0) || (!pump && fund > 0))
			hi2, lo2 := kl[i].High, kl[i].Low
			for j := i - 11; j <= i; j++ {
				if kl[j].High > hi2 {
					hi2 = kl[j].High
				}
				if kl[j].Low < lo2 {
					lo2 = kl[j].Low
				}
			}
			r, w := simExitG(kl, i, H, kl[i].Close, hi2-lo2, 0.618, 0.5, pump)
			train := i < cutoff
			if train {
				add(&baseTr, r, w)
			} else {
				add(&baseTe, r, w)
			}
			if aln {
				if train {
					add(&alnTr, r, w)
				} else {
					add(&alnTe, r, w)
				}
			}
			if fuel {
				if train {
					add(&fuTr, r, w)
				} else {
					add(&fuTe, r, w)
				}
			}
			if aln && fuel {
				if train {
					add(&bothTr, r, w)
				} else {
					add(&bothTe, r, w)
				}
			}
		}
	}
	fmt.Printf("=== 精選層 K線回測 (gate%.0f, 市價進, TP0.618/SL0.5, 持%d根, 費%.2f%%) ===\n", g, H, fee)
	fmt.Printf("%-16s | %-26s | %-26s\n", "", "訓練(前70%)", "測試(後30% OOS)")
	fmt.Printf("%-16s | %7s %8s %10s | %7s %8s %10s\n", "子集", "訊號", "勝率", "淨期望", "訊號", "勝率", "淨期望")
	pr := func(label string, tr, te acc) {
		f := func(a acc) string {
			if a.n == 0 {
				return fmt.Sprintf("%7d %8s %10s", 0, "-", "-")
			}
			return fmt.Sprintf("%7d %7.1f%% %+9.3f%%", a.n, float64(a.wins)/float64(a.n)*100, a.sum/float64(a.n)-fee)
		}
		fmt.Printf("%-16s | %s | %s\n", label, f(tr), f(te))
	}
	pr("全部(基準)", baseTr, baseTe)
	pr("同向", alnTr, alnTe)
	pr("費率燃料", fuTr, fuTe)
	pr("同向+費率燃料", bothTr, bothTe)
	fmt.Println("判讀: 精選層『測試段』勝率/期望 > 基準且筆數夠 → 過濾器穩;測試崩或筆數太少 → 別過度相信")
}
