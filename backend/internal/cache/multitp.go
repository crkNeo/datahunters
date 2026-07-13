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
}

// Presets from the design discussion.
var (
	// mean-reversion: reversion fades fast → front-load size + place partials nearer.
	tpMeanRev = &tpPlan{a: 0.30, b: 0.60, w1: 0.50, w2: 0.30, w3: 0.20, beBuf: 0.0005, minSplitPct: 0.008}
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
func stepTP(tr *PaperTrade, price float64, p *tpPlan, now time.Time) bool {
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
			if long { // TP1 → move stop to break-even+
				tr.SL = roundPx(tr.Entry * (1 + p.beBuf))
			} else {
				tr.SL = roundPx(tr.Entry * (1 - p.beBuf))
			}
		}
		if tr.Legs < 2 && reached(tr.TP2) {
			tr.Realized += p.w2 * pnl(tr.Dir, tr.Entry, tr.TP2)
			tr.Filled += p.w2
			tr.Legs = 2
			tr.SL = tr.TP1 // TP2 → lock the stop at TP1
		}
	}
	if reached(tr.TP) { // final target → close the remainder at TP3
		tr.Legs = 3
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
	tr.PnLPct = round2(tr.Realized + (1-tr.Filled)*pnl(tr.Dir, tr.Entry, price))
	return false
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
