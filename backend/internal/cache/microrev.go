package cache

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"datahunter/internal/exchange"
)

// microrev.go: admin-only mean-reversion strategies, evaluated once per
// closed bar over the 銀河 (emaCoins) universe — same shape as convergence.go.

//	1. 布林重回 (bollfade)  1h 雙向:前一根收盤在布林(20,2σ)外、本根收回通道內(過度延伸
//	                        失敗)且方向與 EMA200 同側 → 朝中軌交易。止損 2.5 ATR,目標=中軌,
//	                        RR 需 0.4–3.0。
//	2. 乖離回歸 (meanrev)   1h 雙向:收盤偏離 EMA20 超過 2 ATR、且與 EMA200 同側(上方接多、
//	                        下方接空)→ 朝 EMA20 回歸。止損 3 ATR,目標=EMA20。
//
// All display-only (admin). Entry + TP/SL exit are both judged on the CLOSED bar;
// open positions are marked to the live WS price for display.

// microBook is one strategy's config + simulated trade state.
type microBook struct {
	name     string // db book name + trade-id prefix (bollfade|meanrev|bgv2dev…)
	tf       string // "30m" | "1h"
	barSec   int64  // bar length in seconds (bucketing + expiry)
	klimit   int
	minBars  int
	expiry   int     // max hold in bars → market exit ("expired")
	// runnerExpiry (脈衝星v3): once the runner is active (Legs≥2, TP2 已鎖利),the core
	// 4h `expiry` no longer applies; this longer cap (e.g. 24h) lets the runner test the
	// extended move, then still closes it if it just grinds. 0 = runner uses `expiry` too.
	runnerExpiry int
	cooldown int     // bars to wait after a close before re-entering the same coin
	keep     int     // closed-trade cap
	plan     *tpPlan // 分批止盈 config (nil = single TP)
	maxSLPct float64 // skip entries whose SL distance exceeds this % of entry (0 = no filter)
	beAt     float64 // >0: 保本位 cue at entry + beAt×(TP−entry). NOTIFY-ONLY — never moves the stop.
	signal   func(cs []exchange.Candle) (dir string, entry, sl, tp float64, ok bool)
	// exitSignal (optional): checked on each bar close for an OPEN position — return
	// true to close it at that close ("reversed"). Used for signal-based exits like
	// 2155多's 死叉 (EMA21 crosses back below EMA55). nil = no signal exit.
	exitSignal func(cs []exchange.Candle) bool
	// tfTag: stamp this book's tf onto its trades at serve time, so a multi-timeframe
	// page (2155多 = 1h/4h/1d 同頁) can show a 週期 column. Off for single-tf books.
	tfTag bool
	// universe (optional): the coin set this book scans each tick. nil = 銀河 top-80
	// (emaCoins). 脈衝星 injects the 爆量熱名單 here so it can reach coins outside top-80.
	universe func() []string
	// gate (optional): a quality filter checked AFTER signal + SL/TP pass. Return
	// false to veto the entry. 脈衝星v2 uses it for the OI/CVD 品質閘門. nil = no gate.
	gate func(coin string, cs []exchange.Candle) bool
	// tpLevels (optional): explicit TP1/TP2/final prices, overriding the plan's a/b
	// placement (but keeping its 分批比例/保本). 2155多 uses it for 固定5% + 1:1 + 1:2.
	tpLevels func(entry, sl float64) (tp1, tp2, tp3 float64)
	// tpLevels4 (optional): explicit TP1/TP2/TP3 partial prices for FOUR-stage books
	// (訂單塊 SMC). tr.TP stays the signal's final target. Gets the final so it can
	// recover the fib grid. nil = not a 4-stage book.
	tpLevels4 func(entry, sl, finalTP float64) (tp1, tp2, tp3 float64)

	// A "family" is a multi-leg strategy shown as ONE tab (e.g. 布乖v2 = 1h 乖離腿 +
	// 4h 布林腿). Legs are separate books so each keeps its own tf/expiry/signal, but
	// they share a coin budget: 同幣互斥 — whichever leg fires first takes the slot.
	stratKey string       // StrategyEnabled key ("" = use name); a family shares one switch
	famMu    *sync.Mutex  // nil = no family. Shared by every leg; serialises entry so the
	family   []*microBook // same-coin check below can't race two legs into one coin.

	mu     sync.Mutex
	trades []*PaperTrade
	bucket int64 // last processed wall-clock bar bucket (single ticker goroutine)
	seeded bool  // first tick only sets the baseline bucket — no boot-time backfill of entries
}

// ---- indicator helpers (aligned full-length series, like emaSeries/atrSeries) ----

// smaSeries is the p-bar simple moving average of closes.
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

// stdevSeries is the p-bar population standard deviation of closes (for Bollinger).
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

// ---- strategy signals ----

// meanRevSignal: 1h both — close deviates > 2 ATR from EMA20, trend-aligned with
// EMA200 (above → long only, below → short only). Target = EMA20; SL = 3 ATR.
func meanRevSignal(cs []exchange.Candle) (dir string, entry, sl, tp float64, ok bool) {
	n := len(cs)
	ema20 := emaSeries(cs, 20)[n-1]
	ema200 := emaSeries(cs, 200)[n-1]
	atr := atrSeries(cs, 14)[n-1]
	if atr <= 0 || ema20 <= 0 || ema200 <= 0 {
		return
	}
	price := cs[n-1].Close
	dev := price - ema20
	switch {
	case price > ema200 && dev < -2.0*atr: // uptrend dip → long back to EMA20
		return "long", roundPx(price), roundPx(price - 3.0*atr), roundPx(ema20), true
	case price < ema200 && dev > 2.0*atr: // downtrend spike → short back to EMA20
		return "short", roundPx(price), roundPx(price + 3.0*atr), roundPx(ema20), true
	}
	return
}

// bollEMASignal (布林EMA): 4H 突破蓄勢, long+short. Unlike every other signal here
// this is a 3-BAR SEQUENCE, judged on the last closed bar (= K2):
//
//	cs[n-4] 突破K的前一根 — 收盤還在中軌下方(多)
//	cs[n-3] 突破K        — 收盤由下往上站上中軌
//	cs[n-2] 蓄勢K1       — 守在中軌上方,且 ≤ 突破K收盤×1.02
//	cs[n-1] 蓄勢K2       — 守在中軌上方,且 ≤ 突破K收盤×1.02 → 本根收盤進場
//
// 「蓄勢」= 突破後兩根都沒續噴(累計漲幅 ≤2%),賭的是盤整後的再啟動,不是追突破。
// 空單完全鏡像(站上→跌破、×1.02→×0.98、上方→下方)。
//
// 趨勢過濾用 4H EMA50(原版是 1H EMA200,實測等價)。
// 原版的過濾 A(%B/帶寬喇叭口)與 過濾 B(長影線)刻意不實作 — 消融證實是負貢獻。
func bollEMASignal(cs []exchange.Candle) (dir string, entry, sl, tp float64, ok bool) {
	n := len(cs)
	if n < 5 {
		return
	}
	sma := smaSeries(cs, 20) // 布林中軌
	ema50 := emaSeries(cs, 50)[n-1]
	atr := atrSeries(cs, 14)[n-1]
	if atr <= 0 || ema50 <= 0 || sma[n-1] <= 0 || sma[n-2] <= 0 || sma[n-3] <= 0 || sma[n-4] <= 0 {
		return
	}
	pre, brk, k1, k2 := cs[n-4].Close, cs[n-3].Close, cs[n-2].Close, cs[n-1].Close
	mid := sma[n-1] // 進場當下的中軌 → 止損基準
	switch {
	case k2 > ema50: // 1. 順大勢(多)
		if !(pre <= sma[n-4] && brk > sma[n-3]) { // 2. 突破K:由下往上站上中軌
			return
		}
		if !(k1 > sma[n-2] && k1 <= brk*1.02) { // 3. 蓄勢K1
			return
		}
		if !(k2 > sma[n-1] && k2 <= brk*1.02) { // 4. 蓄勢K2(累計漲幅 ≤2%)
			return
		}
		// 先四捨五入 entry/SL,再由「取整後」的值算 TP —— 否則存下來的三個數字
		// 之間不是精準的 1:3(TP 由未取整的 SL 導出,顯示出來會對不上)。
		e, s := roundPx(k2), roundPx(mid-1.5*atr)
		if s >= e { // 中軌已在下方夠遠才有結構止損可用
			return
		}
		return "long", e, s, roundPx(e + 3*(e-s)), true // 1:3 RR
	case k2 < ema50: // 1. 順大勢(空)
		if !(pre >= sma[n-4] && brk < sma[n-3]) { // 2. 跌破K
			return
		}
		if !(k1 < sma[n-2] && k1 >= brk*0.98) { // 3. 蓄勢K1
			return
		}
		if !(k2 < sma[n-1] && k2 >= brk*0.98) { // 4. 蓄勢K2(累計跌幅 ≤2%)
			return
		}
		e, s := roundPx(k2), roundPx(mid+1.5*atr)
		if s <= e {
			return
		}
		return "short", e, s, roundPx(e - 3*(s-e)), true
	}
	return
}

// ema2155Signal (2155多): 只做多。EMA21 上穿 EMA55(金叉)→ 進場。止損=最近 20 根的
// 最低低點;風險 R=進場−止損;最終止盈 TP3=進場+4R(1:4)。分批止盈由 strat 設定
// (SplitA=50%→TP1=2R、SplitB=75%→TP2=3R)。持倉中若死叉,由 ema2155DeathCross 收盤平倉。
func ema2155Signal(cs []exchange.Candle) (dir string, entry, sl, tp float64, ok bool) {
	n := len(cs)
	if n < 56 {
		return
	}
	e21 := emaSeries(cs, 21)
	e55 := emaSeries(cs, 55)
	// 金叉:本根 EMA21>EMA55,前一根 EMA21<=EMA55
	if !(e21[n-1] > e55[n-1] && e21[n-2] <= e55[n-2]) {
		return
	}
	price := cs[n-1].Close
	low := cs[n-1].Low
	for i := n - 20; i < n; i++ { // 近 20 根最低低點
		if i < 0 {
			continue
		}
		if cs[i].Low < low {
			low = cs[i].Low
		}
	}
	risk := price - low
	if risk <= 0 {
		return
	}
	return "long", roundPx(price), roundPx(low), roundPx(price + 2*risk), true // 最終止盈 = 1:2(實際三價位由 ema2155TPLevels 決定)
}

// pulsarPctTPLevels 是 脈衝星v5 的固定百分比止盈:TP1=+5%、TP2=+10%、最終=+15%
// (只做多)。與盈虧比無關,純看價格漲幅。sl 不影響(仍作停損)。
func pulsarPctTPLevels(entry, sl float64) (tp1, tp2, tp3 float64) {
	return roundPx(entry * 1.05), roundPx(entry * 1.10), roundPx(entry * 1.15)
}

// ema2155TPLevels 是 2155多 的三價位止盈:TP1=固定 +5%、TP2=1:1(1R)、最終=1:2(2R)。
// 三者排序後由小到大 = TP1/TP2/最終,所以「1:1 < 5%」時 TP1/TP2 自然對調,且順序永遠合法。
func ema2155TPLevels(entry, sl float64) (tp1, tp2, tp3 float64) {
	// 2155多 v2:固定百分比階梯。TP1=+5%(平50%→保本)、TP2=+10%(平40%→鎖TP1)、
	// 剩10%追尾(plan.trailAfterTP2,不看 tp3 這個 final)。tp3 僅供顯示的 runner 目標。
	_ = sl // 止損由訊號給的 swing low 決定,TP 位改用固定百分比,不再依 R
	return roundPx(entry * 1.05), roundPx(entry * 1.10), roundPx(entry * 1.15)
}

// ema2155DeathCross: EMA21 下穿 EMA55(死叉)→ 收盤即時平倉(2155多 的訊號出場)。
func ema2155DeathCross(cs []exchange.Candle) bool {
	n := len(cs)
	if n < 56 {
		return false
	}
	e21 := emaSeries(cs, 21)
	e55 := emaSeries(cs, 55)
	return e21[n-1] < e55[n-1] && e21[n-2] >= e55[n-2]
}

// surgeSignal — 脈衝星進場規則。此書的宇宙已是「爆量熱名單」(成交量條件由 universe
// 篩過),這裡在 15m K 線上做多重過濾。只做多。脈衝星與 v2 共用;v2 另有 OI/CVD 閘門。
//
//   ① 動能狀態:EMA5 > EMA20(多頭排列)且 本根收 > 前一根
//   進場K棒體質(反插針):收陽、實體 ≥ 全距一半、上影線 ≤ 實體(排除衝高被打下來)
//   ② 量能確認:進場根量 ≥ 1.2× 靜默基線(不在量縮回檔根追進)
//   ③ 新鮮度:近 6 根內要有一根「爆量根」(量 ≥ 2.5× 基線),確保是剛起漲、不是追高
//   不追高:現價離近20根低點 ≤ 25% ｜ 止損:近10根 swing low ｜ 止盈 1:4 分批
func surgeSignal(cs []exchange.Candle) (dir string, entry, sl, tp float64, ok bool) {
	const (
		surgeMaxExt  = 0.25 // 現價高出近20根低點 >25% 就算追高
		pinBodyMin   = 0.5  // 進場K棒實體 ≥ 全距的一半
		pinWickMax   = 1.0  // 上影線 ≤ 實體
		entryVolMult = 1.2  // 進場根量 ≥ 基線的倍數
		freshVolMult = 2.5  // 「爆量根」= 量 ≥ 基線的倍數
		freshWithin  = 6    // 爆量根需落在最近幾根內
	)
	n := len(cs)
	if n < 40 {
		return
	}
	price := cs[n-1].Close
	last := cs[n-1]

	// ① 動能:EMA5 > EMA20(狀態,不是金叉,避免落後)+ 本根向上
	e5 := emaSeries(cs, 5)
	e20 := emaSeries(cs, 20)
	if !(e5[n-1] > e20[n-1] && price > cs[n-2].Close) {
		return
	}
	// 進場K棒體質(反插針):收陽、實體夠大、上影線不過長
	rng := last.High - last.Low
	body := last.Close - last.Open
	if rng <= 0 || body <= 0 || body < pinBodyMin*rng || (last.High-last.Close) > pinWickMax*body {
		return
	}
	// 靜默基線量 = 近期之前那 20 根(n-30..n-11)的均量,避開最近的爆量,避免自我循環
	var vsum float64
	for i := n - 30; i < n-10; i++ {
		if i >= 0 {
			vsum += cs[i].Volume
		}
	}
	baseVol := vsum / 20
	// ② 進場根量能確認
	if baseVol > 0 && last.Volume < entryVolMult*baseVol {
		return
	}
	// ③ 新鮮度:最近 freshWithin 根內要有一根爆量根
	fresh := false
	for i := n - freshWithin; i < n; i++ {
		if i >= 0 && baseVol > 0 && cs[i].Volume >= freshVolMult*baseVol {
			fresh = true
			break
		}
	}
	if !fresh {
		return
	}
	// 不追高護欄
	low20 := cs[n-1].Low
	for i := n - 20; i < n; i++ {
		if i >= 0 && cs[i].Low < low20 {
			low20 = cs[i].Low
		}
	}
	if low20 <= 0 || (price-low20)/low20 > surgeMaxExt {
		return
	}
	// 止損 = 近10根 swing low
	low10 := cs[n-1].Low
	for i := n - 10; i < n; i++ {
		if i >= 0 && cs[i].Low < low10 {
			low10 = cs[i].Low
		}
	}
	risk := price - low10
	if risk <= 0 {
		return
	}
	return "long", roundPx(price), roundPx(low10), roundPx(price + 4*risk), true // TP3 = 1:4
}

// robustATR = mean of the last `period` true ranges after dropping the `trimTop`
// largest — so a single monster breakout candle can't blow the ATR up (and the
// stop with it), while it still reflects the current elevated regime.
func robustATR(cs []exchange.Candle, period, trimTop int) float64 {
	n := len(cs)
	if n < period+1 {
		return 0
	}
	trs := make([]float64, 0, period)
	for i := n - period; i < n; i++ {
		if i < 1 {
			continue
		}
		h, l, pc := cs[i].High, cs[i].Low, cs[i-1].Close
		tr := h - l
		if d := math.Abs(h - pc); d > tr {
			tr = d
		}
		if d := math.Abs(l - pc); d > tr {
			tr = d
		}
		trs = append(trs, tr)
	}
	if len(trs) == 0 {
		return 0
	}
	sort.Float64s(trs)
	keep := trs
	if trimTop > 0 && len(trs) > trimTop {
		keep = trs[:len(trs)-trimTop]
	}
	var sum float64
	for _, t := range keep {
		sum += t
	}
	return sum / float64(len(keep))
}

// trimmedBaseVol = mean volume of bars [n-30, n-10) (the quiet window before the
// recent surge) after dropping the 2 highest — a spike-robust baseline so both the
// entry-volume and freshness checks measure against the真正 quiet level.
func trimmedBaseVol(cs []exchange.Candle) float64 {
	n := len(cs)
	vols := make([]float64, 0, 20)
	for i := n - 30; i < n-10; i++ {
		if i >= 0 {
			vols = append(vols, cs[i].Volume)
		}
	}
	if len(vols) == 0 {
		return 0
	}
	sort.Float64s(vols)
	keep := vols
	if len(vols) > 2 {
		keep = vols[:len(vols)-2]
	}
	var s float64
	for _, v := range keep {
		s += v
	}
	return s / float64(len(keep))
}

// surgeV3Signal — 脈衝星v3 進場規則(ATR 自適應)。宇宙已是爆量熱名單。只做多。
//
//   ① EMA5>EMA20 且 本根向上 ｜ 反插針(收陽、實體≥50%全距、上影≤實體)
//   拋物線護欄:單根全距 ≤ 3×ATR ｜ 量能:進場根 ≥ 1.2× 截尾基線
//   新鮮度:近6根有一根 ≥ 2.5× 基線 ｜ 不追高:距20根低點 ≤ 8×ATR
//   止損可行性:(close−近10根低) 需 ≤ 3×ATR(否則不進場),< 0.8×ATR 則推寬到 0.8×ATR
//   回傳 tp = 進場 + 5R 佔位(runner 追尾,setupTP rMult 需要 TP3 在 2R 之外)
func surgeV3Signal(cs []exchange.Candle) (dir string, entry, sl, tp float64, ok bool) {
	const (
		pinBodyMin   = 0.5
		pinWickMax   = 1.0
		entryVolMult = 1.2
		freshVolMult = 2.5
		freshWithin  = 6
		kExt         = 8.0 // 不追高:距20根低點 ≤ kExt×ATR
		slMinATR     = 0.8
		slMaxATR     = 4.0 // 止損可行性上限放寬(原 3.0):容許更寬的 swing-low 止損
		barMaxATR    = 3.0
	)
	n := len(cs)
	if n < 40 {
		return
	}
	price := cs[n-1].Close
	last := cs[n-1]
	atr := robustATR(cs, 14, 2)
	if atr <= 0 {
		return
	}
	// ① 動能狀態
	e5 := emaSeries(cs, 5)
	e20 := emaSeries(cs, 20)
	if !(e5[n-1] > e20[n-1] && price > cs[n-2].Close) {
		return
	}
	// 反插針
	rng := last.High - last.Low
	body := last.Close - last.Open
	if rng <= 0 || body <= 0 || body < pinBodyMin*rng || (last.High-last.Close) > pinWickMax*body {
		return
	}
	// 拋物線護欄:單根 K 太誇張 = 買在垂直頂
	if rng > barMaxATR*atr {
		return
	}
	base := trimmedBaseVol(cs)
	// ② 量能確認
	if base > 0 && last.Volume < entryVolMult*base {
		return
	}
	// ③ 新鮮度
	fresh := false
	for i := n - freshWithin; i < n; i++ {
		if i >= 0 && base > 0 && cs[i].Volume >= freshVolMult*base {
			fresh = true
			break
		}
	}
	if !fresh {
		return
	}
	// 不追高:距近20根低點 ≤ kExt×ATR
	low20 := cs[n-1].Low
	for i := n - 20; i < n; i++ {
		if i >= 0 && cs[i].Low < low20 {
			low20 = cs[i].Low
		}
	}
	if (price - low20) > kExt*atr {
		return
	}
	// 止損:近10根 swing low + 可行性閘門
	low10 := cs[n-1].Low
	for i := n - 10; i < n; i++ {
		if i >= 0 && cs[i].Low < low10 {
			low10 = cs[i].Low
		}
	}
	stopDist := price - low10
	if stopDist > slMaxATR*atr { // 止損太寬 → 不進場(不夾進結構)
		return
	}
	if stopDist < slMinATR*atr { // 太緊 → 往外推,放在雜訊之外
		stopDist = slMinATR * atr
	}
	if stopDist <= 0 {
		return
	}
	// tp = 5R 佔位;實際 runner 靠 trailAfterTP2 追尾,不會真的收在這
	return "long", roundPx(price), roundPx(price - stopDist), roundPx(price + 5*stopDist), true
}

// ---- generic engine ----

// microTick evaluates one book once per newly closed bar over 銀河 coins.
func (s *Store) microTick(b *microBook) {
	bkt := time.Now().UTC().Unix() / b.barSec
	if bkt == b.bucket {
		return
	}
	b.bucket = bkt
	if !b.seeded { // boot: just set the baseline; only bars that close from now on can open trades
		b.seeded = true
		return
	}
	now := time.Now().UTC()
	base := s.emaCoins()
	if b.universe != nil { // 脈衝星:掃爆量熱名單(可含 top-80 以外的幣)
		base = b.universe()
	}
	coins := append([]string(nil), base...) // 複製,下面要 append 不能動到共用切片
	// 關鍵:一定要處理「有未平倉部位的幣」,即使它已掉出宇宙/熱名單 —— 否則逾時、
	// 訊號出場(死叉)、收盤 TP/SL 這些只在 microRun(收 K)跑的邏輯永遠不會觸發,
	// 部位會卡著出不掉(脈衝星的熱名單流動快,最容易踩到)。
	b.mu.Lock()
	seen := make(map[string]bool, len(coins))
	for _, c := range coins {
		seen[c] = true
	}
	for _, tr := range b.trades {
		if tr.Status == "open" && !seen[tr.Coin] {
			seen[tr.Coin] = true
			coins = append(coins, tr.Coin)
		}
	}
	b.mu.Unlock()
	for _, coin := range coins {
		cs, err := s.ex.BinanceKlines(coin+"USDT", b.tf, b.klimit)
		if err != nil || len(cs) < 2 {
			continue
		}
		cs = cs[:len(cs)-1] // drop the still-forming bar
		if len(cs) < b.minBars {
			continue
		}
		s.microRun(b, coin, cs, now)
		time.Sleep(25 * time.Millisecond) // pace the REST batch
	}
}

func (s *Store) microRun(b *microBook, coin string, cs []exchange.Candle, now time.Time) {
	last := cs[len(cs)-1]
	barMs := b.barSec * 1000
	if b.famMu != nil {
		b.famMu.Lock() // 家族:序列化各腿的進場判斷(同幣互斥不能有 race)
	}
	b.mu.Lock()
	var open *PaperTrade
	for _, tr := range b.trades {
		if tr.Coin == coin && tr.Status == "open" {
			open = tr
			break
		}
	}
	var dirty *PaperTrade
	opened, closed := false, false
	if open != nil {
		// bar-close backstop for when the WS feed is down (partial TP1/TP2 are booked
		// on the live stepTP tick). Full-close only: final target / current stop / expiry.
		exit, outcome, px := false, "", 0.0
		if open.Dir == "long" {
			if last.Low <= open.SL {
				exit, outcome, px = true, slOutcome(open), open.SL
			} else if last.High >= open.TP {
				exit, outcome, px = true, "tp3", open.TP
			}
		} else {
			if last.High >= open.SL {
				exit, outcome, px = true, slOutcome(open), open.SL
			} else if last.Low <= open.TP {
				exit, outcome, px = true, "tp3", open.TP
			}
		}
		if !exit && b.exitSignal != nil && b.exitSignal(cs) { // 訊號出場(2155多 死叉)→ 收盤平倉
			exit, outcome, px = true, "reversed", last.Close
		}
		// OpenTime 現在存的是進場 K 棒的收盤時刻(比開盤晚一根),故 +barMs 補回,持有根數與原本一致。
		// 分段逾時(脈衝星v3):主倉用 expiry(4h);一旦剩 runner(Legs≥2、已鎖利)改用 runnerExpiry(24h)。
		exp := b.expiry
		if b.runnerExpiry > 0 && open.Legs >= 2 {
			exp = b.runnerExpiry
		}
		if !exit && exp > 0 && (last.Ts-open.OpenTime.UnixMilli()+barMs)/barMs >= int64(exp) {
			exit, outcome, px = true, "expired", last.Close
		}
		if exit {
			if outcome == "tp3" {
				open.Legs = 3
			}
			closeTrade(open, px, outcome, now) // blends any realized tranches
			closed = true
		} else {
			open.Cur = roundPx(last.Close)
			open.PnLPct = round2(open.Realized + (1-open.Filled)*pnl(open.Dir, open.Entry, last.Close))
		}
		dirty = open
	} else if s.StrategyEnabled(b.strat()) && !microCooling(b, coin, last.Ts, barMs) && !familyHolds(b, coin) {
		if dir, entry, sl, tp, ok := b.signal(cs); ok && s.microSLOK(b, entry, sl) && s.microTPOK(b, entry, tp) &&
			(b.gate == nil || b.gate(coin, cs)) { // 品質閘門(脈衝星v2 的 OI/CVD),nil=不設限
			tr := &PaperTrade{
				ID:       fmt.Sprintf("%s|%s|%d", b.name, coin, now.UnixMilli()),
				Coin:     coin,
				Dir:      dir,
				Entry:    entry,
				SL:       sl,
				TP:       tp,
				Cur:      entry,
				Status:   "open",
				// 進場時間記「該根 K 棒的收盤時刻」(= 開盤 + 一根),因為進場是在收盤才確認/成交。
				// 例:15m 的 13:45 K 棒在 14:00 收盤確認進場 → 顯示 14:00,而不是 13:45。
				OpenTime: time.UnixMilli(last.Ts + barMs).UTC(),
			}
			plan, _ := s.tpFor(b.strat(), b.plan)
			setupTP(tr, plan) // compute TP1/TP2 (分批止盈) at entry — nil when admin turned it off
			if b.tpLevels != nil && plan != nil {
				// 明確三價位止盈(2155多):TP1/TP2/最終各自獨立,覆蓋 plan 的 a/b 位置,
				// 但仍沿用 plan 的分批比例(w1/w2/w3)與保本緩衝。
				tr.TP1, tr.TP2, tr.TP = b.tpLevels(tr.Entry, tr.SL)
			}
			if b.tpLevels4 != nil && plan != nil {
				// 四段斐波止盈(訂單塊 SMC):TP1/TP2/TP3 三個分批位,tr.TP 維持訊號給的
				// 最終目標(fib2.0)。從 SL(fib-0.13)與 tr.TP(fib2.0)還原斐波格再算三段。
				tr.TP1, tr.TP2, tr.TP3 = b.tpLevels4(tr.Entry, tr.SL, tr.TP)
			}
			b.trades = append(b.trades, tr)
			dirty = tr
			opened = true
			microTrim(b)
		}
	}
	b.mu.Unlock()
	if b.famMu != nil {
		b.famMu.Unlock() // 在 DB 寫入前放掉,別讓另一腿等 I/O
	}
	if dirty != nil && s.db != nil {
		s.db.upsertTrade(b.name, dirty)
	}
	// 開倉/平倉通知(原本只有 TP/保本會通知)。解鎖後才發,避免持鎖查訂閱者。
	// notifyOpenBook/CloseBook 內部用 stratKeyOf 把 bgv2dev/bgv2boll 併回 bgv2 讀開關。
	if opened {
		s.notifyOpenBook(b.name, dirty)
	}
	if closed {
		s.notifyCloseBook(b.name, dirty, now, false) // force=false → 吃「平倉通知」開關
	}
}

// microSLOK reports whether the entry's stop distance is within the book's
// maxSLPct filter. Backtest (jmch_posts.csv) showed a handful of wide-stop trades
// caused the bulk of the losses; capping SL distance at entry removes them without
// touching the trend of small TP1-then-breakeven wins. 0 = no filter.
// The admin 最大止損% setting overrides the book's own value when present.
func (s *Store) microSLOK(b *microBook, entry, sl float64) bool {
	cap := s.stratMaxSL(b.strat(), b.maxSLPct)
	if cap <= 0 || entry <= 0 {
		return true
	}
	return math.Abs(entry-sl)/entry*100 <= cap
}

// microTPOK reports whether the entry's TP distance is at least the strategy's
// 最小止盈% (0 = no limit). Skips entries with too little room to run.
func (s *Store) microTPOK(b *microBook, entry, tp float64) bool {
	min := s.stratMinTP(b.strat())
	if min <= 0 || entry <= 0 {
		return true
	}
	return math.Abs(tp-entry)/entry*100 >= min
}

// strat returns the StrategyEnabled key for this book (a family shares one switch).
func (b *microBook) strat() string {
	if b.stratKey != "" {
		return b.stratKey
	}
	return b.name
}

// familyHolds reports whether a SIBLING leg already has an open position on coin
// (同幣互斥:誰先觸發誰佔位,另一腿跳過).
//
// Lock safety: the caller holds b.famMu and b.mu, and every leg's microRun takes
// famMu first — so only one goroutine in the family can ever hold two book locks,
// and no cycle is possible. microMarkTick/microState only ever take a single book's
// mu, so they can't close a cycle either.
func familyHolds(b *microBook, coin string) bool {
	for _, sib := range b.family {
		if sib == b {
			continue // self is already covered by microRun's own open-trade lookup
		}
		sib.mu.Lock()
		held := false
		for _, tr := range sib.trades {
			if tr.Coin == coin && tr.Status == "open" {
				held = true
				break
			}
		}
		sib.mu.Unlock()
		if held {
			return true
		}
	}
	return false
}

// microCooling reports whether coin is still in its post-exit cooldown window.
// Caller holds b.mu.
func microCooling(b *microBook, coin string, barTs, barMs int64) bool {
	cd := int64(b.cooldown) * barMs
	var recent int64
	for _, tr := range b.trades {
		if tr.Coin == coin && tr.Status == "closed" && tr.CloseTime != nil {
			if ms := tr.CloseTime.UnixMilli(); ms > recent {
				recent = ms
			}
		}
	}
	return recent > 0 && barTs-recent < cd
}

// microTrim bounds the closed-trade history. Caller holds b.mu.
func microTrim(b *microBook) {
	var open, closed []*PaperTrade
	for _, tr := range b.trades {
		if tr.Status == "open" {
			open = append(open, tr)
		} else {
			closed = append(closed, tr)
		}
	}
	sort.Slice(closed, func(i, j int) bool { return closed[i].CloseTime.After(*closed[j].CloseTime) })
	if len(closed) > b.keep {
		closed = closed[:b.keep]
	}
	b.trades = append(open, closed...)
}

// microMarkTick marks open positions to the live WS price and exits any that hit
// the fixed TP/SL intrabar. Entries are still judged on the closed bar in
// microTick; the closed-bar convExit in microRun stays a backstop for feed-down.
func (s *Store) microMarkTick(b *microBook) {
	px := s.livePrices()
	if len(px) == 0 {
		return
	}
	now := time.Now()
	// admin 出場設定,讀在鎖外:分批計畫 / 獨立保本 / 保本位提示,三者由 ExitMode 決定
	plan, beOn := s.tpFor(b.strat(), b.plan)
	beAt, beBuf := s.beFor(b.strat())
	cueAt := s.beCueFor(b.strat(), b.beAt)
	var dirty, beCues []*PaperTrade
	b.mu.Lock()
	for _, tr := range b.trades {
		if tr.Status != "open" {
			continue
		}
		p := px[tr.Coin]
		if p <= 0 {
			continue
		}
		// 保本位 cue — 通知用,不動止損。刻意放在 stepTP 之前:若同一 tick 直接衝到
		// 出場,仍要先記下曾經到過保本位,否則這筆單的紀錄會看不出它有走到過。
		beFired := false
		if cueAt > 0 && !tr.BEHit && tr.TP != 0 {
			lvl := tr.Entry + cueAt*(tr.TP-tr.Entry) // 多空皆適用:TP−Entry 帶正負號
			if (tr.Dir == "long" && p >= lvl) || (tr.Dir == "short" && p <= lvl) {
				tr.BEHit, tr.BEPrice = true, roundPx(lvl)
				beFired = true
			}
		}
		// 獨立保本模式:真的把止損移到保本(與上面的純提示互斥,由 ExitMode 決定)
		if applyBreakeven(tr, p, beAt, beBuf) {
			beFired = true
		}
		before := tr.Legs
		closed := stepTP(tr, p, plan, beOn, now) // books partial TPs, ratchets stop, closes at TP3/SL
		if closed || tr.Legs != before || beFired {
			dirty = append(dirty, tr) // persist on any leg change, BE latch, or final close
		}
		if tr.Legs > before { // a TP (TP1/TP2/TP3) just filled → 軟體通知 (admin book)
			s.notifyTPHit(b.name, tr, true, tr.Legs)
		}
		if beFired {
			beCues = append(beCues, tr) // 通知在鎖外送出
		}
	}
	b.mu.Unlock()
	for _, tr := range beCues {
		s.notifyBEHit(b.name, tr)
	}
	if s.db != nil {
		for _, tr := range dirty {
			s.db.upsertTrade(b.name, tr)
		}
	}
}

// microState returns the book(s) open/closed/stats, open positions marked live.
// Variadic so a multi-leg family (布乖v2) merges into ONE tab payload.
func (s *Store) microState(bs ...*microBook) PaperState {
	px := s.livePrices() // read before the locks; open positions get live 現價
	st := PaperState{Open: []*PaperTrade{}, Closed: []*PaperTrade{}}
	st.Stats.MultiTP = bs[0].plan != nil
	var sum, grossWin, grossLoss float64
	var all []*PaperTrade
	for _, b := range bs {
		b.mu.Lock()
		if b.tfTag { // 只有多週期同頁的書(2155多)才標 TF,其餘策略不顯示週期欄
			for _, tr := range b.trades {
				tr.TF = b.tf
			}
		}
		all = append(all, b.trades...)
		b.mu.Unlock()
	}
	for _, tr := range all {
		if tr.Status == "open" {
			st.Open = append(st.Open, tr)
			continue
		}
		st.Closed = append(st.Closed, tr)
		st.Stats.Closed++
		sum += tr.PnLPct
		if tr.PnLPct > 0 {
			st.Stats.Wins++
		} else {
			st.Stats.Losses++
		}
		tpStats(tr, &st.Stats.Tp1, &st.Stats.Tp2, &st.Stats.Tp3, &grossWin, &grossLoss)
	}
	markLiveOpen(st.Open, px)
	sort.Slice(st.Open, func(i, j int) bool { return st.Open[i].OpenTime.After(st.Open[j].OpenTime) })
	sort.Slice(st.Closed, func(i, j int) bool {
		return st.Closed[i].CloseTime != nil && st.Closed[j].CloseTime != nil && st.Closed[i].CloseTime.After(*st.Closed[j].CloseTime)
	})
	if st.Stats.Closed > 0 {
		st.Stats.WinRate = round2(float64(st.Stats.Wins) / float64(st.Stats.Closed) * 100)
		st.Stats.AvgPnl = round2(sum / float64(st.Stats.Closed))
		st.Stats.TotalPnl = round2(sum)
		if grossLoss > 0 {
			st.Stats.ProfitFactor = round2(grossWin / grossLoss)
		} else if grossWin > 0 {
			st.Stats.ProfitFactor = 99.99 // no losers yet
		}
	}
	return st
}

// ---- per-book public wrappers (ticks + state) ----

func (s *Store) MeanRevTick()  { s.microTick(s.meanRevBook) }
func (s *Store) BollEMATick()  { s.microTick(s.bollEMABook) }
func (s *Store) EMA2155Tick() {
	for _, b := range s.ema2155Books {
		s.microTick(b)
	}
}
func (s *Store) PulsarTick()       { s.microTick(s.pulsarBook) }
func (s *Store) PulsarMarkTick()   { s.microMarkTick(s.pulsarBook) }
func (s *Store) PulsarV2Tick()     { s.microTick(s.pulsarV2Book) }
func (s *Store) PulsarV2MarkTick() { s.microMarkTick(s.pulsarV2Book) }
func (s *Store) PulsarV3Tick()     { s.microTick(s.pulsarV3Book) }
func (s *Store) PulsarV3MarkTick() { s.microMarkTick(s.pulsarV3Book) }
func (s *Store) PulsarV4Tick()     { s.microTick(s.pulsarV4Book) }
func (s *Store) PulsarV4MarkTick() { s.microMarkTick(s.pulsarV4Book) }
func (s *Store) PulsarV5Tick()     { s.microTick(s.pulsarV5Book) }
func (s *Store) PulsarV5MarkTick() { s.microMarkTick(s.pulsarV5Book) }
func (s *Store) SMCTick() {
	for _, b := range s.smcBooks {
		s.microTick(b)
	}
}
func (s *Store) SMCMarkTick() {
	for _, b := range s.smcBooks {
		s.microMarkTick(b)
	}
}

func (s *Store) MeanRevMarkTick()  { s.microMarkTick(s.meanRevBook) }
func (s *Store) BollEMAMarkTick()  { s.microMarkTick(s.bollEMABook) }
func (s *Store) EMA2155MarkTick() {
	for _, b := range s.ema2155Books {
		s.microMarkTick(b)
	}
}

// keepIf filters trades to those still open (closedOnly=true) or wipes all (false).
func keepIf(trades []*PaperTrade, closedOnly bool) []*PaperTrade {
	if !closedOnly {
		return nil
	}
	var open []*PaperTrade
	for _, tr := range trades {
		if tr.Status == "open" {
			open = append(open, tr)
		}
	}
	return open
}

// ClearStrategy resets a strategy book's simulated trades (memory + DB). closedOnly
// keeps open positions and drops only the closed history. Returns false for an
// unknown book.
func (s *Store) ClearStrategy(book string, closedOnly bool) bool {
	switch book {
	case "meanrev":
		s.meanRevBook.mu.Lock()
		s.meanRevBook.trades = keepIf(s.meanRevBook.trades, closedOnly)
		s.meanRevBook.mu.Unlock()
	case "bollema":
		s.bollEMABook.mu.Lock()
		s.bollEMABook.trades = keepIf(s.bollEMABook.trades, closedOnly)
		s.bollEMABook.mu.Unlock()
	case "ema2155": // 一個開關清三個週期(1h/4h/1d)
		for _, b := range s.ema2155Books {
			b.mu.Lock()
			b.trades = keepIf(b.trades, closedOnly)
			b.mu.Unlock()
			if s.db != nil {
				if closedOnly {
					s.db.clearClosedTrades(b.name)
				} else {
					s.db.clearTrades(b.name)
				}
			}
		}
		return true // DB 已在上面各週期處理
	case "orderblock": // 一個開關清三個週期(15m/1h/4h)
		for _, b := range s.smcBooks {
			b.mu.Lock()
			b.trades = keepIf(b.trades, closedOnly)
			b.mu.Unlock()
			if s.db != nil {
				if closedOnly {
					s.db.clearClosedTrades(b.name)
				} else {
					s.db.clearTrades(b.name)
				}
			}
		}
		return true // DB 已在上面各週期處理
	case "pulsar":
		s.pulsarBook.mu.Lock()
		s.pulsarBook.trades = keepIf(s.pulsarBook.trades, closedOnly)
		s.pulsarBook.mu.Unlock()
	case "pulsarv2":
		s.pulsarV2Book.mu.Lock()
		s.pulsarV2Book.trades = keepIf(s.pulsarV2Book.trades, closedOnly)
		s.pulsarV2Book.mu.Unlock()
	case "pulsarv3":
		s.pulsarV3Book.mu.Lock()
		s.pulsarV3Book.trades = keepIf(s.pulsarV3Book.trades, closedOnly)
		s.pulsarV3Book.mu.Unlock()
	case "pulsarv4":
		s.pulsarV4Book.mu.Lock()
		s.pulsarV4Book.trades = keepIf(s.pulsarV4Book.trades, closedOnly)
		s.pulsarV4Book.mu.Unlock()
	case "pulsarv5":
		s.pulsarV5Book.mu.Lock()
		s.pulsarV5Book.trades = keepIf(s.pulsarV5Book.trades, closedOnly)
		s.pulsarV5Book.mu.Unlock()
	case "conv":
		s.convMu.Lock()
		s.convTrades = keepIf(s.convTrades, closedOnly)
		s.convMu.Unlock()
	case "main", "gamble", "emaonly":
		s.paperMu.Lock()
		b := s.paperMain
		switch book {
		case "gamble":
			b = s.paperGamble
		case "emaonly":
			b = s.paperEMA
		}
		b.trades = keepIf(b.trades, closedOnly)
		s.paperMu.Unlock()
	default:
		return false
	}
	if s.db != nil {
		if closedOnly {
			s.db.clearClosedTrades(book)
		} else {
			s.db.clearTrades(book)
		}
	}
	return true
}

// retrofitMultiTP backfills 分批止盈 levels onto OPEN trades that predate multi-TP,
// so on-going positions adopt the new rules. Idempotent: only trades with no TP1
// set (and no legs booked) are touched. Runs once at startup.
func (s *Store) retrofitMultiTP() {
	type dirtyRow struct {
		book string
		tr   *PaperTrade
	}
	var dirty []dirtyRow
	fill := func(book string, plan *tpPlan, trades []*PaperTrade) {
		if plan == nil {
			return
		}
		for _, tr := range trades {
			if tr.Status == "open" && tr.TP1 == 0 && tr.Legs == 0 && tr.Filled == 0 {
				setupTP(tr, plan)
				dirty = append(dirty, dirtyRow{book, tr})
			}
		}
	}
	s.meanRevBook.mu.Lock()
	fill("meanrev", s.meanRevBook.plan, s.meanRevBook.trades)
	s.meanRevBook.mu.Unlock()
	s.paperMu.Lock()
	fill("main", s.paperMain.plan, s.paperMain.trades)
	fill("gamble", s.paperGamble.plan, s.paperGamble.trades)
	fill("emaonly", s.paperEMA.plan, s.paperEMA.trades)
	s.paperMu.Unlock()
	s.convMu.Lock()
	fill("conv", tpMomentum, s.convTrades)
	s.convMu.Unlock()
	if s.db != nil {
		for _, d := range dirty {
			s.db.upsertTrade(d.book, d.tr)
		}
	}
}

func (s *Store) MeanRevState() PaperState  { return s.microState(s.meanRevBook) }
func (s *Store) BollEMAState() PaperState  { return s.microState(s.bollEMABook) }
func (s *Store) EMA2155State() PaperState  { return s.microState(s.ema2155Books...) }
func (s *Store) PulsarState() PaperState   { return s.microState(s.pulsarBook) }
func (s *Store) PulsarV2State() PaperState  { return s.microState(s.pulsarV2Book) }
func (s *Store) PulsarV3State() PaperState  { return s.microState(s.pulsarV3Book) }
func (s *Store) PulsarV4State() PaperState  { return s.microState(s.pulsarV4Book) }
func (s *Store) PulsarV5State() PaperState  { return s.microState(s.pulsarV5Book) }
func (s *Store) SMCState() PaperState       { return s.microState(s.smcBooks...) }
