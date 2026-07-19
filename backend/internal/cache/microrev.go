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
	cooldown int     // bars to wait after a close before re-entering the same coin
	keep     int     // closed-trade cap
	plan     *tpPlan // 分批止盈 config (nil = single TP)
	maxSLPct float64 // skip entries whose SL distance exceeds this % of entry (0 = no filter)
	beAt     float64 // >0: 保本位 cue at entry + beAt×(TP−entry). NOTIFY-ONLY — never moves the stop.
	signal   func(cs []exchange.Candle) (dir string, entry, sl, tp float64, ok bool)

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

// bollFadeSignal: 1h both — prior bar closed OUTSIDE the Bollinger band and this
// bar closed back INSIDE (failed over-extension), aligned with the EMA200 side.
// Target = middle band; SL = 2.5 ATR; keep only RR in [0.4, 3.0].
func bollFadeSignal(cs []exchange.Candle) (dir string, entry, sl, tp float64, ok bool) {
	n := len(cs)
	sma := smaSeries(cs, 20)
	std := stdevSeries(cs, 20)
	ema200 := emaSeries(cs, 200)[n-1]
	atr := atrSeries(cs, 14)[n-1]
	if atr <= 0 || ema200 <= 0 || std[n-1] <= 0 || std[n-2] <= 0 {
		return
	}
	price, prev, mid := cs[n-1].Close, cs[n-2].Close, sma[n-1]
	upPrev, loPrev := sma[n-2]+2*std[n-2], sma[n-2]-2*std[n-2]
	upNow, loNow := sma[n-1]+2*std[n-1], sma[n-1]-2*std[n-1]
	switch {
	case prev > upPrev && price <= upNow && price < ema200: // poked above, back in, downtrend → short
		if mid >= price {
			return
		}
		s := price + 2.5*atr
		if rr := (price - mid) / (s - price); rr < 0.4 || rr > 3.0 {
			return
		}
		return "short", roundPx(price), roundPx(s), roundPx(mid), true
	case prev < loPrev && price >= loNow && price > ema200: // poked below, back in, uptrend → long
		if mid <= price {
			return
		}
		s := price - 2.5*atr
		if rr := (mid - price) / (price - s); rr < 0.4 || rr > 3.0 {
			return
		}
		return "long", roundPx(price), roundPx(s), roundPx(mid), true
	}
	return
}

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

// ---- 布乖v2 (bgv2): a two-leg SHORT-only family, one tab, 同幣互斥 ----
//
// Both legs share the same skeleton — short only, close < that timeframe's own
// EMA200, SL = entry + 4.0 ATR, target = the mean itself, 64-bar timeout, RR gate
// 0.4–3.0, 4-bar cooldown — and differ only in what counts as "over-extended":
//
//	腿1 bgv2Dev  (1h): 收盤高出 EMA50 逾 2 ATR            → 目標 EMA50
//	腿2 bgv2Boll (4h): 衝出布林(50,2σ)上軌後收回、仍在中軌上 → 目標 中軌(SMA50)
//
// bgv2BollSignal: 4h SHORT-only — a failed breakout above the upper Bollinger band
// inside a downtrend. Unlike bollFadeSignal (1h, 20-period, both directions) this
// leg uses a 50-period band on 4h, is short-only, and additionally requires the bar
// to close back inside the band but still ABOVE the mid — i.e. it pulled back, but
// hasn't already fallen through the target.
func bgv2BollSignal(cs []exchange.Candle) (dir string, entry, sl, tp float64, ok bool) {
	n := len(cs)
	sma := smaSeries(cs, 50)
	std := stdevSeries(cs, 50)
	ema200 := emaSeries(cs, 200)[n-1]
	atr := atrSeries(cs, 14)[n-1]
	if atr <= 0 || ema200 <= 0 || sma[n-1] <= 0 || std[n-1] <= 0 || std[n-2] <= 0 {
		return
	}
	price, prev, mid := cs[n-1].Close, cs[n-2].Close, sma[n-1]
	if price >= ema200 { // 1. 空頭環境
		return
	}
	if prev <= sma[n-2]+2*std[n-2] { // 2. 前一根要衝出上軌
		return
	}
	if price > sma[n-1]+2*std[n-1] { // 3a. 本根要收回通道內
		return
	}
	if price <= mid { // 3b. 但仍需在中軌上方(收回,但沒跌過頭 → 目標還在下方)
		return
	}
	s := price + 4.0*atr
	// 4. RR 閘門。這裡下限是實質有效的(不像腿1):要求 收盤−中軌 > 1.6 ATR。
	if rr := (price - mid) / (s - price); rr < 0.4 || rr > 3.0 {
		return
	}
	return "short", roundPx(price), roundPx(s), roundPx(mid), true
}

// bgv2DevSignal: 1h SHORT-only — fade an over-extended bounce inside a downtrend.
// A ground-up redesign of meanRevSignal, every axis backed by ablation on the full
// dataset (see STRATEGIES.md):
//
//	mean = EMA50, not EMA20  — the fast MA only catches hours-long noise that nets
//	                           to zero after fees; EMA50's bounces are big enough to pay.
//	SL = 4.0 ATR, not 3.0    — 2.0→4.0 improved monotonically; tight stops die to wicks.
//	hold = 64 bars, not 24   — 4→48 improved monotonically, 48→96 flat.
//	target = the EMA itself  — beat a fixed 2 ATR target and a pure time exit.
//	EMA200 filter is load-bearing — removing it flipped alt shorts +0.043 → −0.023.
//	SHORT only               — the long mirror (buying a dip below EMA50) had no edge.
//
// No confirmation bar: waiting for a close-back-down only bought +5pp win rate with
// no expectancy gain, so we fade the bounce top directly.
func bgv2DevSignal(cs []exchange.Candle) (dir string, entry, sl, tp float64, ok bool) {
	n := len(cs)
	ema50 := emaSeries(cs, 50)[n-1]
	ema200 := emaSeries(cs, 200)[n-1]
	atr := atrSeries(cs, 14)[n-1]
	if atr <= 0 || ema50 <= 0 || ema200 <= 0 {
		return
	}
	price := cs[n-1].Close
	if price >= ema200 { // 1. 必須處於空頭環境
		return
	}
	if (price-ema50)/atr <= 2.0 { // 2. 反彈過度:高出 EMA50 逾 2 ATR
		return
	}
	if ema50 >= price { // 目標必須在進場價下方(乖離為正時恆成立,防呆)
		return
	}
	s := price + 4.0*atr
	// 3. 盈虧比閘門。注意:條件 2 已保證乖離 > 2 ATR,故 RR = 乖離/(4 ATR) > 0.5,
	// 下限 0.4 實際上永遠不會觸發;真正生效的是上限(擋掉乖離 > 12 ATR 的極端值)。
	if rr := (price - ema50) / (s - price); rr < 0.4 || rr > 3.0 {
		return
	}
	return "short", roundPx(price), roundPx(s), roundPx(ema50), true
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
	for _, coin := range s.emaCoins() {
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
		if !exit && (last.Ts-open.OpenTime.UnixMilli())/barMs >= int64(b.expiry) {
			exit, outcome, px = true, "expired", last.Close
		}
		if exit {
			if outcome == "tp3" {
				open.Legs = 3
			}
			closeTrade(open, px, outcome, now) // blends any realized tranches
		} else {
			open.Cur = roundPx(last.Close)
			open.PnLPct = round2(open.Realized + (1-open.Filled)*pnl(open.Dir, open.Entry, last.Close))
		}
		dirty = open
	} else if s.StrategyEnabled(b.strat()) && !microCooling(b, coin, last.Ts, barMs) && !familyHolds(b, coin) {
		if dir, entry, sl, tp, ok := b.signal(cs); ok && s.microSLOK(b, entry, sl) {
			tr := &PaperTrade{
				ID:       fmt.Sprintf("%s|%s|%d", b.name, coin, now.UnixMilli()),
				Coin:     coin,
				Dir:      dir,
				Entry:    entry,
				SL:       sl,
				TP:       tp,
				Cur:      entry,
				Status:   "open",
				OpenTime: time.UnixMilli(last.Ts).UTC(),
			}
			plan, _ := s.tpFor(b.strat(), b.plan)
			setupTP(tr, plan) // compute TP1/TP2 (分批止盈) at entry — nil when admin turned it off
			b.trades = append(b.trades, tr)
			dirty = tr
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

func (s *Store) BollFadeTick() { s.microTick(s.bollFadeBook) }
func (s *Store) MeanRevTick()  { s.microTick(s.meanRevBook) }
func (s *Store) BGV2DevTick()  { s.microTick(s.bgv2Dev) }
func (s *Store) BGV2BollTick() { s.microTick(s.bgv2Boll) }
func (s *Store) BollEMATick()  { s.microTick(s.bollEMABook) }

func (s *Store) BollFadeMarkTick() { s.microMarkTick(s.bollFadeBook) }
func (s *Store) MeanRevMarkTick()  { s.microMarkTick(s.meanRevBook) }
func (s *Store) BGV2MarkTick()     { s.microMarkTick(s.bgv2Dev); s.microMarkTick(s.bgv2Boll) }
func (s *Store) BollEMAMarkTick()  { s.microMarkTick(s.bollEMABook) }

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
	case "bollfade":
		s.bollFadeBook.mu.Lock()
		s.bollFadeBook.trades = keepIf(s.bollFadeBook.trades, closedOnly)
		s.bollFadeBook.mu.Unlock()
	case "meanrev":
		s.meanRevBook.mu.Lock()
		s.meanRevBook.trades = keepIf(s.meanRevBook.trades, closedOnly)
		s.meanRevBook.mu.Unlock()
	case "bollema":
		s.bollEMABook.mu.Lock()
		s.bollEMABook.trades = keepIf(s.bollEMABook.trades, closedOnly)
		s.bollEMABook.mu.Unlock()
	case "bgv2": // 家族:一個開關清兩腿
		for _, b := range []*microBook{s.bgv2Dev, s.bgv2Boll} {
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
		return true // DB 已在上面各腿處理
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
	s.bollFadeBook.mu.Lock()
	fill("bollfade", s.bollFadeBook.plan, s.bollFadeBook.trades)
	s.bollFadeBook.mu.Unlock()
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

func (s *Store) BollFadeState() PaperState { return s.microState(s.bollFadeBook) }
func (s *Store) MeanRevState() PaperState  { return s.microState(s.meanRevBook) }

// BGV2State merges both legs into ONE tab payload (布乖v2 是一個策略,不是兩個)。
func (s *Store) BGV2State() PaperState    { return s.microState(s.bgv2Dev, s.bgv2Boll) }
func (s *Store) BollEMAState() PaperState { return s.microState(s.bollEMABook) }
