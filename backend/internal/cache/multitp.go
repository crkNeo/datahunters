package cache

import "time"

// multitp.go: shared 分批止盈 (multi take-profit) engine. A trade's existing TP is
// the FINAL target (TP3). Two partial targets (TP1/TP2) are placed as fractions of
// the entry→TP3 distance; a slice of the position is booked at each, and the stop
// ratchets up (TP1 → break-even+, TP2 → TP1). The accounting is position-fraction
// based: Filled = fraction closed, Realized = locked-in PnL% — so closeTrade can
// blend the remaining tranche without knowing the plan.

// tpPlan is a book's multi-TP configuration. Two placement modes:
//   - fraction mode (rMult=false): TP1/TP2 = a,b as fractions of the entry→TP3 distance.
//   - R-multiple mode (rMult=true): TP1/TP2 = a,b × R, where R = |entry−SL| (initial risk).
type tpPlan struct {
	rMult       bool    // true: a,b are R-multiples; false: fractions of entry→TP3
	a, b        float64 // TP1/TP2 placement (see rMult)
	w1, w2, w3  float64 // fraction of the position closed at TP1 / TP2 / TP3 (sum = 1)
	beBuf       float64 // break-even buffer after TP1: SL → entry × (1 ± beBuf)
	minSplitPct float64 // fraction mode: skip splitting when |TP3−entry|/entry is below this
	// trailAfterTP2 (脈衝星v3): after TP2 the LAST leg (w3) rides a trailing stop
	// instead of a fixed TP3 — peak − trailR × R (R = TP1−entry), floored at TP1.
	// No fixed TP3 close: the runner exits only on the trailed stop (or the book's
	// runnerExpiry). Lets the small runner test the extended move past the 4h core cap.
	trailAfterTP2 bool
	trailR        float64
}

// Presets from the design discussion.
var (
	// mean-reversion, extra front-loaded (bollfade/meanrev): these targets are the
	// mean itself (EMA20 / 布林中軌) so there's no trend tail to ride — banking more
	// at TP1 beats holding. jmch_posts.csv sweep: 50/30/20→60/25/15 lifted both books'
	// net/trade monotonically. K-line replay then showed TP1 placement a=0.30→0.45
	// lifts net/trade further (meanrev +0.68→+1.17%, bollfade +0.78→+1.12%) with ~0
	// win→SL flips: the 30–45% band is empty (reversions overshoot past it or fail
	// before 30%). Raising b or loosening the post-TP2 stop both tested worse (extra
	// TP3s don't pay for the give-back), so only a moved.
	tpMeanRevFront = &tpPlan{a: 0.45, b: 0.60, w1: 0.60, w2: 0.25, w3: 0.15, beBuf: 0.0005, minSplitPct: 0.008}
	// momentum / disciplined (also 超新星): TP1/TP2 at 40%/70% of the entry→TP3
	// distance, size 40/30/30. Fraction placement adapts to each book's target.
	tpMomentum = &tpPlan{a: 0.40, b: 0.70, w1: 0.40, w2: 0.30, w3: 0.30, beBuf: 0.0005, minSplitPct: 0.008}
	// 脈衝星v3:R 倍數 TP1=1R / TP2=2R,最後一段(w3)追尾。結構在此,比例(w1/w2/w3)
	// 與保本緩衝由 strat 設定驅動(tpFor)。trailR=1.5 → runner 停損 = 峰值 − 1.5R,鎖 ≥1R。
	tpPulsarV3 = &tpPlan{rMult: true, a: 1, b: 2, w1: 0.50, w2: 0.25, w3: 0.25, beBuf: 0.0005, trailAfterTP2: true, trailR: 1.5}

	// 訂單塊 SMC:四段斐波止盈(1.0/1.382/1.618/2.0),TP1/TP2/TP3 各平 25%,最終段平剩下 25%。
	// a/b 只是佔位 —— 實際 TP1/TP2/TP3 由 smcFibTPLevels 依斐波格覆蓋。沿路套保:TP1→保本、
	// TP2→TP1、TP3→TP2(見 stepTP)。
	tpSMCFib = &tpPlan{a: 0.40, b: 0.70, w1: 0.25, w2: 0.25, w3: 0.25, beBuf: 0.0005, minSplitPct: 0.008}
)

// setupTP computes TP1/TP2 for a freshly opened trade from its entry + final TP
// (=TP3). If there's no plan or the target is too close, the trade stays single-TP
// (TP1/TP2 = 0) and behaves exactly as before.
func setupTP(tr *PaperTrade, p *tpPlan) {
	tr.Legs, tr.Filled, tr.Realized, tr.TP1, tr.TP2 = 0, 0, 0, 0, 0
	if p == nil || tr.TP == 0 || tr.Entry == 0 {
		return
	}
	dist := tr.TP - tr.Entry // signed: + for long, − for short
	if p.rMult {
		r := abs2(tr.Entry - tr.SL) // initial risk
		if r <= 0 || p.b*r >= abs2(dist) {
			return // TP3 not beyond TP2 (1.5R) → single TP (no room to split)
		}
		sign := 1.0
		if dist < 0 {
			sign = -1
		}
		tr.TP1 = roundPx(tr.Entry + sign*p.a*r)
		tr.TP2 = roundPx(tr.Entry + sign*p.b*r)
		return
	}
	if abs2(dist)/tr.Entry < p.minSplitPct {
		return // target too close → single TP
	}
	tr.TP1 = roundPx(tr.Entry + p.a*dist)
	tr.TP2 = roundPx(tr.Entry + p.b*dist)
}

// stepTP advances a trade against the live price: it books partial exits as each TP
// fills (ratcheting the stop up), and closes the trade fully at TP3 or the current
// stop. Returns true when the trade is now fully closed. Single-TP trades (TP1==0)
// just do the TP3/SL check on the whole position.
// be=false keeps the original stop after TP1 (admin turned 保本 off); the TP2→鎖TP1
// ratchet is 鎖利, not 保本, so it always applies.
func stepTP(tr *PaperTrade, price float64, p *tpPlan, be bool, now time.Time) bool {
	// 最大漲幅:進場後最高有利波動%(峰值,含這次要平倉的價)。所有走 stepTP 的策略共用。
	if g := pnl(tr.Dir, tr.Entry, price); g > tr.MaxGain {
		tr.MaxGain = round2(g)
	}
	long := tr.Dir == "long"
	reached := func(level float64) bool {
		if level == 0 {
			return false
		}
		if long {
			return price >= level
		}
		return price <= level
	}
	if p != nil && tr.TP1 > 0 { // partial legs (split active)
		if tr.Legs < 1 && reached(tr.TP1) {
			tr.Realized += p.w1 * pnl(tr.Dir, tr.Entry, tr.TP1)
			tr.Filled += p.w1
			tr.Legs = 1
			if be { // TP1 → move stop to break-even+ (skipped when 保本 is off)
				if long {
					tr.SL = roundPx(tr.Entry * (1 + p.beBuf))
				} else {
					tr.SL = roundPx(tr.Entry * (1 - p.beBuf))
				}
			}
			mirrorLeg(tr, p.w1) // Bitunix 完全跟隨:平 w1 + 止損移保本
		}
		if tr.Legs < 2 && reached(tr.TP2) {
			tr.Realized += p.w2 * pnl(tr.Dir, tr.Entry, tr.TP2)
			tr.Filled += p.w2
			tr.Legs = 2
			tr.SL = tr.TP1 // TP2 → lock the stop at TP1
			mirrorLeg(tr, p.w2)
		}
		if tr.TP3 > 0 && tr.Legs < 3 && reached(tr.TP3) { // 四段策略的第三段(訂單塊 SMC);三段書 TP3=0 不進來
			tr.Realized += p.w3 * pnl(tr.Dir, tr.Entry, tr.TP3)
			tr.Filled += p.w3
			tr.Legs = 3
			tr.SL = tr.TP2 // TP3 → 止損移到 TP2(沿路套保)
			mirrorLeg(tr, p.w3)
		}
	}
	if p != nil && p.trailAfterTP2 { // 脈衝星v3:runner 追尾,沒有固定 TP3
		if tr.Legs >= 2 { // TP2 後最後一段跟著峰值走,停損只上不下、不低於 TP1
			if tr.Peak == 0 || (long && price > tr.Peak) || (!long && price < tr.Peak) {
				tr.Peak = price
			}
			if rRisk := tr.TP1 - tr.Entry; rRisk != 0 && p.trailR > 0 {
				if long {
					if t := tr.Peak - p.trailR*rRisk; t > tr.SL {
						tr.SL = roundPx(t)
					}
				} else {
					if t := tr.Peak - p.trailR*rRisk; t < tr.SL {
						tr.SL = roundPx(t)
					}
				}
			}
		}
	} else if reached(tr.TP) { // final target → close the remainder
		if tr.TP3 > 0 {
			tr.Legs = 4 // 四段策略:最終段
		} else {
			tr.Legs = 3
		}
		closeTrade(tr, tr.TP, "tp3", now)
		return true
	}
	stopHit := price <= tr.SL
	if !long {
		stopHit = price >= tr.SL
	}
	if stopHit { // (possibly trailed-up) stop → close the remainder
		closeTrade(tr, tr.SL, slOutcome(tr), now)
		return true
	}
	tr.Cur = roundPx(price)
	tr.PnLPct = round2(settledPnL(tr, price))
	return false
}

// settledPnL 回傳「以 price(出場價或現價)結算時要顯示的損益%」——採『已達到的最高
// TP 階梯』口徑,取代舊的加權混合值(顯示 + 統計都用它):
//   - 未達任何 TP(Legs==0):就是 price 相對進場的 %(含虧損原樣)。
//   - 已達 TP:回傳 max(price 相對進場的 %, 已達最高 TP 的 %)。已達最高 TP = 第 Legs 段
//     (TP1→TP1、TP2→TP2、TP3→TP3……不降一階);Legs==1 時保本被打也自然顯示 TP1。
//
// 於是:回踩打到保利停損出場 → 顯示「已達到的那一階」;往上吃到更高的價 → 顯示實際到價 %。
// 各批的實際結算(Realized/Filled)仍照舊累計,只是不再拿來當顯示數字。
func settledPnL(tr *PaperTrade, price float64) float64 {
	base := pnl(tr.Dir, tr.Entry, price)
	if tr.Legs <= 0 {
		return base
	}
	floor := pnl(tr.Dir, tr.Entry, tpAtLeg(tr, tr.Legs)) // 已達到的最高 TP(第 Legs 段)
	if base > floor {
		return base
	}
	return floor
}

// tpAtLeg 回傳第 n 段的目標價(1→TP1、2→TP2、3→TP3)。未設(如三段書沒有 TP3)或 n≥4
// → 回傳最終 TP。
func tpAtLeg(tr *PaperTrade, n int) float64 {
	switch n {
	case 1:
		if tr.TP1 > 0 {
			return tr.TP1
		}
	case 2:
		if tr.TP2 > 0 {
			return tr.TP2
		}
	case 3:
		if tr.TP3 > 0 {
			return tr.TP3
		}
	}
	return tr.TP
}

// applyBreakeven is the STANDALONE 保本 mode: once price has travelled `at` of the
// way from entry to TP, move the stop to break-even (± buf). Single-TP books only —
// in split mode TP1 already does this, and the two modes are mutually exclusive.
//
// It reuses BEHit/BEPrice, which the notify-only 保本位提示 also uses; that's safe
// because a strategy is in exactly one mode, never both.
//
// ⚠️ 回測提醒:單段部位走到 X% 就移保本,實測是負優化(+35.6% → −37.5%/−26.3%/+0.7%,
// 對應 1/3、1/2、2/3),因為剪掉了肥尾止盈。這裡照設定執行,好壞由設定的人負責。
func applyBreakeven(tr *PaperTrade, price, at, buf float64) bool {
	if at <= 0 || tr.TP == 0 || tr.Entry == 0 || tr.BEHit {
		return false
	}
	lvl := tr.Entry + at*(tr.TP-tr.Entry) // TP−Entry 帶正負號,多空皆適用
	if !((tr.Dir == "long" && price >= lvl) || (tr.Dir == "short" && price <= lvl)) {
		return false
	}
	tr.BEHit, tr.BEPrice = true, roundPx(lvl)
	if tr.Dir == "long" {
		tr.SL = roundPx(tr.Entry * (1 + buf))
	} else {
		tr.SL = roundPx(tr.Entry * (1 - buf))
	}
	return true
}

// slOutcome labels a stop-out by how far the trade got before stopping.
func slOutcome(tr *PaperTrade) string {
	switch tr.Legs {
	case 1:
		return "tp1sl" // TP1後保本 (stop已在保本)
	case 2:
		return "tp2sl" // TP2後出場 (stop已鎖在 TP1)
	default:
		return "sl"
	}
}

// stopOutcome 是 slOutcome 的最終防線:停損價有可能被保本/鎖利機制上調到成本價
// 之上(分批的 TP1→保本、單段模式的 applyBreakeven),這時候出場其實是「保本出場」
// 而不是虧損的「止損 SL」。用實際損益判定,任何路徑漏掉 slOutcome 都能兜住。
// exit 是這次成交價;pnl 只看剩餘倉位,已實現的分批獲利不算(那是 tp1sl/tp2sl 的事)。
func stopOutcome(tr *PaperTrade, exit string, price float64) string {
	if exit != "sl" {
		return exit
	}
	if pnl(tr.Dir, tr.Entry, price) > 0 {
		return "besl" // 保本出場
	}
	return exit
}

// tpStats folds one closed trade into multi-TP funnel + profit-factor accumulators.
func tpStats(tr *PaperTrade, tp1, tp2, tp3 *int, grossWin, grossLoss *float64) {
	if tr.Legs >= 1 {
		*tp1++
	}
	if tr.Legs >= 2 {
		*tp2++
	}
	if tr.Legs >= 3 {
		*tp3++
	}
	if tr.PnLPct >= 0 {
		*grossWin += tr.PnLPct
	} else {
		*grossLoss += -tr.PnLPct
	}
}
