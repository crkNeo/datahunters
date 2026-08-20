package collector

import (
	"database/sql"
	"fmt"
	"log"
	"math"
	"time"
)

// pattern.go turns the two hand-read setups into live detectors, and — more
// importantly — records every firing together with what happened afterwards.
//
// The recording is the point. The threshold sweep already showed that a plain
// price-momentum trigger loses money at every level, with only 12.9% of
// triggers landing in a real move. These patterns are a hypothesis that the
// precision can be lifted by demanding a specific shape rather than a specific
// size. That hypothesis is worth exactly nothing until the hit rate is measured
// on signals that fired BEFORE the outcome was known — which is why every hit
// is written the moment it triggers, and the outcome is backfilled later
// against rows that can no longer be edited.
//
// Both detectors are deliberately literal transcriptions of 爆發型態.md. When a
// threshold here and a number in that document disagree, the document is the
// specification and this file is the bug.

const (
	// ---- 型態 A:投降反彈 ----
	aLookback   = 8     // bars examined for the sell-off
	aMinRedBars = 5     // how many of them must close red
	aMinDrop    = -0.03 // cumulative fall across the run
	aMaxOIChg   = -0.05 // ΔOI5m — longs being liquidated
	aMinVolZ    = 3.0
	aMaxTaker   = 40.0 // sellers in control
	aTrigTaker  = 60.0 // buyers seize it back
	// ---- 型態 B:OI 擴張動能 ----
	bBuildBars = 3    // consecutive bars of rising, positive ΔOI
	bMinTaker  = 55.0 // sustained aggressive buying
	bTrigVolZ  = 10.0
	// 0.05 was read off the single BTW case (+6.2%) and rejected almost every
	// other real move: WLD +3.5, STAR +3.0, PUMP +2.7 all cleared their volume
	// condition and were thrown away on this one. GPS — the 型態 C failure — sat
	// at -0.4, so it is still excluded with room to spare.
	bTrigOIChg  = 0.02
	bMinPriceUp = 0.0 // price must be up over the build window
	// ---- 型態 C 過濾(兩種型態都套用)----
	cMinSpotRatio = 0.05 // below this, a coin WITH a spot pair is pure leverage
)

// patternHitsDDL records every firing plus its outcome.
//
// The trigger columns are frozen copies of the values that caused the firing.
// Recomputing them later from snap_1m would look equivalent and is not: a
// change to a threshold or a window would silently rewrite history, and the
// hit rate would then be measured against conditions nobody actually traded.
const patternHitsDDL = `CREATE TABLE IF NOT EXISTS pattern_hits (
  day          INT    NOT NULL,
  ts           BIGINT NOT NULL,
  symbol       VARCHAR(32) NOT NULL,
  pattern      VARCHAR(8)  NOT NULL,
  price        DOUBLE NOT NULL DEFAULT 0,
  oi_chg_5m    DOUBLE NOT NULL DEFAULT 0,
  vol_z        DOUBLE NOT NULL DEFAULT 0,
  taker_pct    DOUBLE NOT NULL DEFAULT 0,
  basis_bps    DOUBLE NOT NULL DEFAULT 0,
  funding_bps  DOUBLE NOT NULL DEFAULT 0,
  spot_ratio   DOUBLE NOT NULL DEFAULT 0,
  mkt_ret      DOUBLE NOT NULL DEFAULT 0,
  run_bars     INT    NOT NULL DEFAULT 0,
  run_pct      DOUBLE NOT NULL DEFAULT 0,
  outcome_done TINYINT NOT NULL DEFAULT 0,
  mfe_5m       DOUBLE NOT NULL DEFAULT 0,
  mae_5m       DOUBLE NOT NULL DEFAULT 0,
  ret_5m       DOUBLE NOT NULL DEFAULT 0,
  mfe_15m      DOUBLE NOT NULL DEFAULT 0,
  PRIMARY KEY (day, ts, symbol, pattern),
  KEY idx_ph_ts (ts),
  KEY idx_ph_pat (pattern, ts)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`

// pbar is one minute with the derived quantities both detectors read.
type pbar struct {
	ts                      int64
	open, high, low, close_ float64
	volQuote, takerPct      float64
	oiUSD, oiChg5m          float64
	fundingBps, basisBps    float64
	volZ, spotRatio         float64
	mktRet                  float64 // market median return that minute
}

// DetectPatterns evaluates both setups on the most recently closed bar of every
// tracked symbol and records any firing. Called once per minute, after the
// snapshot for that minute has been written.
func (c *Collector) DetectPatterns(barTs int64) {
	c.mu.RLock()
	syms := append([]string(nil), c.universe...)
	c.mu.RUnlock()
	if len(syms) == 0 {
		return
	}
	// 90 minutes covers the 60-bar volume z-score plus the longest setup window
	from := barTs - 90*60_000
	series, err := loadPatternSeries(c.db, from, barTs)
	if err != nil {
		log.Printf("patterns: load: %v", err)
		return
	}
	var hits []patternHit
	for _, sym := range syms {
		bs := series[sym]
		if len(bs) < 70 || bs[len(bs)-1].ts != barTs {
			continue // not enough history, or this symbol has no bar this minute
		}
		i := len(bs) - 1
		if h, ok := detectA(sym, bs, i); ok {
			hits = append(hits, h)
		}
		if h, ok := detectB(sym, bs, i); ok {
			hits = append(hits, h)
		}
	}
	if len(hits) == 0 {
		return
	}
	if err := writePatternHits(c.db, hits); err != nil {
		// Losing a detection is worse than a noisy log: the board stops gaining
		// rows and that is indistinguishable from nothing having triggered.
		log.Printf("patterns: ⚠ 偵測到 %d 筆但寫入失敗,這些訊號已經遺失: %v", len(hits), err)
		return
	}
	for _, h := range hits {
		log.Printf("patterns: %s 型態%s 觸發 price=%.6g volZ=%+.1f ΔOI5m=%+.2f%% 主買=%.0f%% 基差=%+.1fbp",
			h.Symbol, h.Pattern, h.Price, h.VolZ, h.OIChg5m*100, h.TakerPct, h.BasisBps)
	}
}

// detectA looks for a liquidation flush that has just turned.
//
// The setup is a run of accelerating red bars on falling open interest — longs
// being closed out — and the trigger is the first bar where aggressive buying
// returns and the perp stops trading below its index. Nothing here predicts the
// reversal; it waits for a cascade that is already visible to run out of fuel.
func detectA(sym string, bs []pbar, i int) (patternHit, bool) {
	if i < aLookback+1 {
		return patternHit{}, false
	}
	prev := bs[i-1] // the flush bar; bs[i] is the turn
	if prev.oiChg5m > aMaxOIChg || prev.volZ < aMinVolZ ||
		prev.takerPct >= aMaxTaker || prev.basisBps >= 0 {
		return patternHit{}, false
	}
	red := 0
	for j := i - aLookback; j < i; j++ {
		if bs[j].close_ < bs[j].open {
			red++
		}
	}
	if red < aMinRedBars {
		return patternHit{}, false
	}
	start := bs[i-aLookback].close_
	if start <= 0 {
		return patternHit{}, false
	}
	runPct := prev.close_/start - 1
	if runPct > aMinDrop {
		return patternHit{}, false
	}
	// the turn
	cur := bs[i]
	if cur.close_ <= cur.open || cur.takerPct <= aTrigTaker || cur.basisBps <= 0 {
		return patternHit{}, false
	}
	return newHit(sym, "A", cur, red, runPct), true
}

// detectB looks for open interest and price climbing together, then igniting.
//
// Rising OI alongside rising price means new longs rather than shorts covering,
// so there is real demand under the move and it does not immediately unwind —
// that is the whole distinction from the 型態 C failure case, where a 10.6%
// move returned 0.1% because nothing was holding it up.
func detectB(sym string, bs []pbar, i int) (patternHit, bool) {
	if i < bBuildBars+2 {
		return patternHit{}, false
	}
	cur := bs[i]
	if cur.volZ < bTrigVolZ || cur.oiChg5m < bTrigOIChg {
		return patternHit{}, false
	}
	// OI expanding, and expanding faster, through the build-up.
	//
	// The two loops are separate on purpose: the growth check must compare bars
	// INSIDE the window against each other. Folding it into the first loop
	// reaches one bar further back than the window and skips the most recent
	// comparison entirely, which lets a decelerating build-up pass.
	for k := bBuildBars; k >= 1; k-- {
		if bs[i-k].oiChg5m <= 0 {
			return patternHit{}, false
		}
	}
	for k := bBuildBars - 1; k >= 1; k-- {
		if bs[i-k].oiChg5m < bs[i-k-1].oiChg5m {
			return patternHit{}, false
		}
	}
	start := bs[i-bBuildBars-1].close_
	if start <= 0 || cur.close_/start-1 <= bMinPriceUp {
		return patternHit{}, false
	}
	// sustained aggressive buying through the build-up, not just on the spike
	var taker float64
	for k := 1; k <= bBuildBars; k++ {
		taker += bs[i-k].takerPct
	}
	if taker/float64(bBuildBars) < bMinTaker {
		return patternHit{}, false
	}
	// basis must be positive AND rising — real bid, not leverage alone
	if cur.basisBps <= 0 || cur.basisBps <= bs[i-bBuildBars].basisBps {
		return patternHit{}, false
	}
	if !passesCFilter(cur) {
		return patternHit{}, false
	}
	return newHit(sym, "B", cur, bBuildBars, cur.close_/start-1), true
}

// passesCFilter rejects the pure-leverage shape that looks identical on price
// and volume but retraces immediately. spotRatio of exactly 0 means the coin
// has no spot pair at all, which is not the same as having one nobody trades.
//
// Funding sign is deliberately NOT a condition. It was in the first version
// because GPS had negative funding, but WLD ran negative funding throughout and
// still returned 9.5% — the two differ on BASIS at the trigger (GPS -10.9,
// WLD +26.0), not on funding. Rejecting on funding would have discarded the
// better of the two trades on a coincidence.
func passesCFilter(b pbar) bool {
	if b.basisBps <= 0 {
		return false
	}
	if b.spotRatio > 0 && b.spotRatio < cMinSpotRatio {
		return false
	}
	return true
}

type patternHit struct {
	Ts                    int64
	Symbol, Pattern       string
	Price                 float64
	OIChg5m, VolZ         float64
	TakerPct, BasisBps    float64
	FundingBps, SpotRatio float64
	MktRet                float64
	RunBars               int
	RunPct                float64
}

func newHit(sym, pat string, b pbar, runBars int, runPct float64) patternHit {
	return patternHit{
		Ts: b.ts, Symbol: sym, Pattern: pat, Price: b.close_,
		OIChg5m: b.oiChg5m, VolZ: b.volZ, TakerPct: b.takerPct,
		BasisBps: b.basisBps, FundingBps: b.fundingBps, SpotRatio: b.spotRatio,
		MktRet:  b.mktRet,
		RunBars: runBars, RunPct: runPct,
	}
}

func loadPatternSeries(db *sql.DB, from, to int64) (map[string][]pbar, error) {
	rows, err := db.Query(`SELECT s.symbol, s.ts, s.open, s.high, s.low, s.close,
	                              s.vol_quote, s.taker_buy_quote, s.oi_usd, s.funding,
	                              s.mark, s.index_px, COALESCE(p.vol_quote, 0),
	                              COALESCE(g.median_ret, 0)
	                       FROM snap_1m s
	                       LEFT JOIN spot_1m p ON p.ts = s.ts AND p.symbol = s.symbol
	                       LEFT JOIN regime_1m g ON g.ts = s.ts
	                       WHERE s.ts >= ? AND s.ts <= ? AND s.close > 0
	                       ORDER BY s.symbol, s.ts`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string][]pbar{}
	for rows.Next() {
		var sym string
		var b pbar
		var taker, funding, mark, index, spotVol, mktRet float64
		if err := rows.Scan(&sym, &b.ts, &b.open, &b.high, &b.low, &b.close_,
			&b.volQuote, &taker, &b.oiUSD, &funding, &mark, &index, &spotVol, &mktRet); err != nil {
			return nil, err
		}
		if b.volQuote > 0 {
			b.takerPct = taker / b.volQuote * 100
			b.spotRatio = spotVol / b.volQuote
		}
		b.fundingBps = funding * 10000
		// Recorded rather than filtered on. Two of the observed moves fired in
		// the same minute the whole market jumped 1.36% — those are beta, not
		// selection, and taking several at once is one bet, not several. The
		// right threshold is unknown, so measure it before rejecting on it.
		b.mktRet = mktRet * 100
		if index > 0 && mark > 0 {
			b.basisBps = (mark - index) / index * 10000
		}
		out[sym] = append(out[sym], b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// derived series need the full history in order, so fill them afterwards
	for _, bs := range out {
		for i := range bs {
			if i >= 5 && bs[i-5].oiUSD > 0 && bs[i].oiUSD > 0 &&
				bs[i].ts-bs[i-5].ts == 5*60_000 {
				bs[i].oiChg5m = bs[i].oiUSD/bs[i-5].oiUSD - 1
			}
			if i >= 60 && bs[i].ts-bs[i-60].ts == 60*60_000 {
				var sum, sum2 float64
				for j := i - 60; j < i; j++ {
					sum += bs[j].volQuote
					sum2 += bs[j].volQuote * bs[j].volQuote
				}
				mean := sum / 60
				if v := sum2/60 - mean*mean; v > 0 {
					bs[i].volZ = (bs[i].volQuote - mean) / math.Sqrt(v)
				}
			}
		}
	}
	return out, nil
}

func writePatternHits(db execer, hits []patternHit) error {
	cols := []string{"day", "ts", "symbol", "pattern", "price", "oi_chg_5m", "vol_z",
		"taker_pct", "basis_bps", "funding_bps", "spot_ratio", "mkt_ret", "run_bars", "run_pct"}
	return insertChunkRows(db, "pattern_hits", cols, len(hits), func(i int) []any {
		h := hits[i]
		return []any{dayKeyMs(h.Ts), h.Ts, h.Symbol, h.Pattern, h.Price,
			h.OIChg5m, h.VolZ, h.TakerPct, h.BasisBps, h.FundingBps,
			h.SpotRatio, h.MktRet, h.RunBars, h.RunPct}
	})
}

// BackfillPatternOutcomes fills in what happened after each recorded hit.
//
// Kept strictly separate from detection so an outcome can never influence
// whether the signal was recorded — the hit row exists before the result does,
// which is the only arrangement under which the measured hit rate means
// anything.
func BackfillPatternOutcomes(db *sql.DB) error {
	cutoff := time.Now().Add(-20 * time.Minute).UnixMilli()
	rows, err := db.Query(`SELECT day, ts, symbol, pattern, price FROM pattern_hits
	                       WHERE outcome_done = 0 AND ts <= ? ORDER BY ts LIMIT 500`, cutoff)
	if err != nil {
		return err
	}
	type pend struct {
		day          int
		ts           int64
		symbol, patt string
		price        float64
	}
	var todo []pend
	for rows.Next() {
		var p pend
		if err := rows.Scan(&p.day, &p.ts, &p.symbol, &p.patt, &p.price); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, p := range todo {
		if p.price <= 0 {
			continue
		}
		var mfe5, mae5, ret5, mfe15 sql.NullFloat64
		err := db.QueryRow(`SELECT
		    MAX(CASE WHEN ts <= ? THEN high END), MIN(CASE WHEN ts <= ? THEN low END),
		    SUBSTRING_INDEX(GROUP_CONCAT(CASE WHEN ts <= ? THEN close END ORDER BY ts DESC), ',', 1),
		    MAX(high)
		  FROM snap_1m WHERE symbol = ? AND ts > ? AND ts <= ?`,
			p.ts+5*60_000, p.ts+5*60_000, p.ts+5*60_000,
			p.symbol, p.ts, p.ts+15*60_000).Scan(&mfe5, &mae5, &ret5, &mfe15)
		if err != nil {
			log.Printf("patterns: outcome %s %s: %v", p.symbol, p.patt, err)
			continue
		}
		f := func(v sql.NullFloat64) float64 {
			if !v.Valid || v.Float64 <= 0 {
				return 0
			}
			return v.Float64/p.price - 1
		}
		if _, err := db.Exec(`UPDATE pattern_hits SET outcome_done = 1,
		        mfe_5m = ?, mae_5m = ?, ret_5m = ?, mfe_15m = ?
		      WHERE day = ? AND ts = ? AND symbol = ? AND pattern = ?`,
			f(mfe5), f(mae5), f(ret5), f(mfe15),
			p.day, p.ts, p.symbol, p.patt); err != nil {
			log.Printf("patterns: outcome update: %v", err)
		}
	}
	return nil
}

// RunPatternOutcomes backfills on an interval until stop is closed.
func RunPatternOutcomes(db *sql.DB, every time.Duration, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case <-time.After(every):
			if err := BackfillPatternOutcomes(db); err != nil {
				log.Printf("patterns: %v", err)
			}
		}
	}
}

// LogPatternThresholds prints the conditions actually compiled into this
// binary.
//
// run.sh builds on start, so a pull without a restart leaves the previous
// thresholds running — and the only visible symptom is "fewer hits than
// expected", which is indistinguishable from a quiet market. Printing them at
// startup makes the running version answerable from the log rather than by
// guessing.
func LogPatternThresholds() {
	log.Printf("patterns: A 條件 連續紅K≥%d/%d根 跌幅≤%.1f%% ΔOI5m≤%.0f%% volZ≥%.0f 主買<%.0f%% 基差<0 → 觸發根需收紅+主買>%.0f%%+基差>0",
		aMinRedBars, aLookback, aMinDrop*100, aMaxOIChg*100, aMinVolZ, aMaxTaker, aTrigTaker)
	log.Printf("patterns: B 條件 ΔOI5m 連%d根為正且遞增 主買均值>%.0f%% 基差正且上升 → 觸發需 volZ>%.0f 且 ΔOI5m>%.0f%%(現貨比>0 時需≥%.2f)",
		bBuildBars, bMinTaker, bTrigVolZ, bTrigOIChg*100, cMinSpotRatio)
}

func ensurePatternSchema(db *sql.DB) error {
	if _, err := db.Exec(patternHitsDDL); err != nil {
		return fmt.Errorf("create pattern_hits: %w", err)
	}
	return nil
}
