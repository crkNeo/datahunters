package collector

import (
	"database/sql"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"
)

// analyze.go turns the collected history into the report that decides whether
// this strategy is worth building at all.
//
// It answers, in order:
//
//	Q1  How often does a tradable burst actually happen? If the answer is a
//	    handful per week, nothing downstream matters — there is no business.
//	Q4  What do the excursions look like? MFE against MAE is what sets the
//	    take-profit, the stop and the time stop. Every one of those numbers is
//	    a guess until this table exists.
//	Q3  Which observable quantities actually moved BEFORE the burst? This is
//	    the one that judges the original five conditions on evidence rather
//	    than on plausibility.
//
// Everything is computed in Go from raw rows, so it runs unchanged on MySQL 5.7
// and 8.0, and every feature window looks strictly backwards.

// AnalyzeConfig tunes the report.
type AnalyzeConfig struct {
	Days     int     // how much recent history to read (0 = all)
	EventPct float64 // burst threshold on mfe_5m, e.g. 0.10
	// EpisodeGap merges event bars belonging to the same burst. Without it a
	// single spike counts once per bar that still sees it inside its 5-minute
	// window, inflating the event count several-fold and quietly biasing every
	// per-event statistic toward the middle of moves rather than their start.
	EpisodeGap time.Duration
	TopCoins   int
	// Leverage converts price moves into margin terms and, more importantly,
	// tells the report where the exchange will close the position for you.
	// 0 or 1 = report in price terms only.
	Leverage float64

	// Side selects which tail to study: "up" for pumps, "down" for dumps.
	//
	// Both are the same physics — a forced-liquidation cascade into a thin book
	// — only the side being liquidated differs. A screener built solely on the
	// up tail is blind to half of the events its own thesis predicts, and the
	// down tail is where long liquidations live.
	Side string

	// VisiblePct is how far price must already have moved before a live system
	// could plausibly react. 0 = derive it from EventPct.
	//
	// It MUST scale with the event threshold. Detection eats the first slice of
	// every move, so a fixed 2% costs a tenth of a 20% burst and two thirds of
	// a 3% one — hold it constant and small targets look unprofitable purely
	// because of the measurement, not because of the market.
	VisiblePct float64
}

// dirOf maps the configured side to the sign every return is measured with:
// +1 goes long a pump, -1 goes short a dump. Keeping it as a multiplier means
// one code path measures both tails and neither can drift out of step.
func dirOf(side string) float64 {
	if side == "down" {
		return -1
	}
	return 1
}

// autoVisible derives the detection threshold from the event threshold, capped
// so it never exceeds a third of the move being chased.
func autoVisible(eventPct float64) float64 {
	v := eventPct / 3
	if v > 0.02 {
		v = 0.02
	}
	if v < 0.005 {
		v = 0.005
	}
	return v
}

func DefaultAnalyzeConfig() AnalyzeConfig {
	return AnalyzeConfig{Days: 0, EventPct: 0.10, EpisodeGap: 30 * time.Minute, TopCoins: 12, Side: "up", Leverage: 1}
}

// abar is one minute of one symbol, with everything the features need.
type abar struct {
	ts                                  int64
	open, high, low, close_             float64
	volQuote, takerBuyQuote             float64
	oiUSD, funding, mark, indexPx       float64
	mfe5, mae5, mfe15, secsPeak, isEvnt float64
	labelled                            bool
}

// feature windows, in minutes. Kept short: a burst that needs an hour of
// build-up to detect is not a burst you can trade on a few-minute horizon.
const (
	winShort = 15
	winLong  = 60
)

// RunAnalysis writes the full report to w.
func RunAnalysis(db *sql.DB, cfg AnalyzeConfig, w io.Writer) error {
	from := int64(0)
	if cfg.Days > 0 {
		from = time.Now().Add(-time.Duration(cfg.Days) * 24 * time.Hour).UnixMilli()
	}
	series, err := loadAnalysisSeries(db, from)
	if err != nil {
		return err
	}
	if len(series) == 0 {
		fmt.Fprintln(w, "沒有資料可以分析 — 採集器跑起來了嗎?")
		return nil
	}

	visPct := cfg.VisiblePct
	if visPct <= 0 {
		visPct = autoVisible(cfg.EventPct)
	}

	rep := newReport(w)
	rep.health(series)
	rep.sweep(series, cfg)

	eps, base, bySym := splitEpisodesFull(series, cfg)
	rep.q1(eps, series, cfg)
	rep.q4(eps, cfg, visPct, cfg.Leverage)
	rep.q3(eps, base)
	rep.q3b(eps, bySym)
	rep.footer(eps, cfg)
	return nil
}

// trigStats is the outcome of trading EVERY trigger, not only the ones that
// turned into events.
type trigStats struct {
	n, wins, hits int
	total         float64
}

// simulateTriggers is the honest counterpart to the episode simulation.
//
// The episode-based numbers are selected on the outcome: every bar in that
// population is there BECAUSE a large move followed it. Trading only those is
// not a strategy, it is hindsight, and it will report a high win rate no matter
// what rule is applied.
//
// This walks the same rule over every bar where the trigger would actually have
// fired — a 5-minute return crossing visPct — whether or not anything came of
// it. The trades that fire and go nowhere are exactly the ones missing above,
// and they are where live money is lost.
func simulateTriggers(series map[string][]abar, epsBySym map[string][]int64,
	visPct, tp, sl, cost float64, cooldownMin int, gapMs int64, dir float64) trigStats {

	if dir == 0 {
		dir = 1
	}

	var st trigStats
	for sym, bs := range series {
		lastTrig := -1 << 30
		for j := 5; j < len(bs); j++ {
			if j-lastTrig < cooldownMin {
				continue
			}
			// the lookback must really span five minutes, not five rows
			if bs[j].ts-bs[j-5].ts != 5*60_000 || bs[j-5].close_ <= 0 || bs[j].close_ <= 0 {
				continue
			}
			if dir*(bs[j].close_/bs[j-5].close_-1) < visPct {
				continue
			}
			lastTrig = j

			e := episode{entry: bs[j].close_, dir: dir}
			for k := j + 1; k < len(bs) && bs[k].ts <= bs[j].ts+5*60_000; k++ {
				e.path = append(e.path, pathBar{bs[k].high, bs[k].low, bs[k].close_})
			}
			if len(e.path) == 0 {
				continue
			}
			ret := e.simulate(tp, sl, cost)
			st.n++
			st.total += ret
			if ret > 0 {
				st.wins++
			}
			for _, ats := range epsBySym[sym] {
				if bs[j].ts >= ats && bs[j].ts <= ats+gapMs {
					st.hits++
					break
				}
			}
		}
	}
	return st
}

// sweepTargets are the move sizes worth comparing side by side. A smaller
// target fires far more often but leaves less room once detection has taken its
// cut, and the two effects pull in opposite directions — which is precisely why
// the choice has to be read off a table rather than argued about.
var sweepTargets = []float64{0.03, 0.05, 0.08, 0.10, 0.15}

// sweep reports the frequency/room trade-off across event thresholds.
//
// The simulated rule is fixed at TP = half the move and SL = a quarter of it,
// deliberately NOT optimised per row: picking each row's best cell in-sample
// would make every threshold look good and would say nothing about the next
// month of trading.
func (r *report) sweep(series map[string][]abar, cfg AnalyzeConfig) {
	sideZh := "暴漲"
	if dirOf(cfg.Side) < 0 {
		sideZh = "暴跌"
	}
	r.head(fmt.Sprintf("門檻掃描(%s)— 想抓多大的行情?", sideZh))
	r.line("  偵測門檻隨事件門檻自動縮放(取 1/3,上限 2%%),否則小行情會被偵測本身吃光。")
	r.line("  模擬規則固定為 TP = 幅度一半、SL = 幅度四分之一,不逐列最佳化。")
	r.line("")
	r.line("  %-7s %7s %8s %7s %10s %12s %12s", "門檻", "事件/天", "觸發/天", "精準度", "進場後MFE", "事件內淨報酬", "全觸發淨報酬")
	r.line("  %s", strings.Repeat("-", 76))

	days := 1.0
	for _, bs := range series {
		if len(bs) > 1 {
			d := float64(bs[len(bs)-1].ts-bs[0].ts) / float64(24*time.Hour/time.Millisecond)
			if d > days {
				days = d
			}
		}
	}
	gapMs := int64(cfg.EpisodeGap / time.Millisecond)

	for _, thr := range sweepTargets {
		c := cfg
		c.EventPct = thr
		c.VisiblePct = autoVisible(thr)
		eps, _ := splitEpisodes(series, c)

		var vis []episode
		epsBySym := map[string][]int64{}
		for _, e := range eps {
			epsBySym[e.symbol] = append(epsBySym[e.symbol], e.ts)
			if e.hasVis {
				vis = append(vis, e)
			}
		}
		tp, sl := thr/2, thr/4
		st := simulateTriggers(series, epsBySym, c.VisiblePct, tp, sl, 0.003,
			int(cfg.EpisodeGap/time.Minute), gapMs, dirOf(cfg.Side))

		if len(eps) == 0 && st.n == 0 {
			r.line("  +%-6.0f%% %7s %8s %7s %10s %12s %12s", thr*100, "0", "0", "-", "-", "-", "-")
			continue
		}
		perDay := float64(len(eps)) / days
		trigPerDay := float64(st.n) / days
		prec := 0.0
		if st.n > 0 {
			prec = float64(st.hits) / float64(st.n) * 100
		}
		medMFE, evAvg := 0.0, 0.0
		if len(vis) > 0 {
			var mf []float64
			var tot float64
			for _, e := range vis {
				mf = append(mf, e.visMFE)
				tot += e.simulate(tp, sl, 0.003)
			}
			sort.Float64s(mf)
			medMFE = pct(mf, 0.5) * 100
			evAvg = tot / float64(len(vis)) * 100
		}
		trigAvg := 0.0
		if st.n > 0 {
			trigAvg = st.total / float64(st.n) * 100
		}
		mark := ""
		if trigAvg > 0 {
			mark = " ←正"
		}
		r.line("  +%-6.0f%% %7.1f %8.1f %6.1f%% %9.1f%% %11.2f%% %11.2f%%%s",
			thr*100, perDay, trigPerDay, prec, medMFE, evAvg, trigAvg, mark)
	}
	r.line("")
	r.line("  ⚠ 「事件內淨報酬」是事後挑出真的有噴的那些 K 棒來模擬 —— 它一定漂亮,")
	r.line("    因為那個母體本身就是用結果篩出來的。實盤看的是「全觸發淨報酬」:")
	r.line("    訊號亮了就進場,包含所有亮了卻沒行情的單。兩欄的落差就是這個偏誤的大小。")
	r.line("  「精準度」= 觸發之中真的落在事件裡的比例。它的倒數就是你要吃多少次假訊號。")
	r.line("")
	r.line("  「進場後MFE」才是你真正能搶的那一段 — 門檻本身的數字已經被偵測延遲吃掉一部分。")
	r.line("  想實盤拿到 3%%,看的是這一欄有沒有明顯高於 3%% + 成本,而不是看門檻寫 3%%。")
}

func loadAnalysisSeries(db *sql.DB, from int64) (map[string][]abar, error) {
	// LEFT JOIN so unlabelled bars (the most recent hour, still inside their
	// forward window) still contribute to the feature baseline.
	q := `SELECT s.symbol, s.ts, s.open, s.high, s.low, s.close,
	             s.vol_quote, s.taker_buy_quote, s.oi_usd, s.funding, s.mark, s.index_px,
	             l.mfe_5m, l.mae_5m, l.mfe_15m, l.secs_to_peak_15m, l.is_event
	      FROM snap_1m s LEFT JOIN labels_1m l
	        ON l.ts = s.ts AND l.symbol = s.symbol
	      WHERE s.ts >= ? AND s.close > 0`
	rows, err := db.Query(q, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string][]abar{}
	for rows.Next() {
		var sym string
		var b abar
		var mfe5, mae5, mfe15, secs, ev sql.NullFloat64
		if err := rows.Scan(&sym, &b.ts, &b.open, &b.high, &b.low, &b.close_,
			&b.volQuote, &b.takerBuyQuote, &b.oiUSD, &b.funding, &b.mark, &b.indexPx,
			&mfe5, &mae5, &mfe15, &secs, &ev); err != nil {
			return nil, err
		}
		b.labelled = ev.Valid
		b.mfe5, b.mae5, b.mfe15 = mfe5.Float64, mae5.Float64, mfe15.Float64
		b.secsPeak, b.isEvnt = secs.Float64, ev.Float64
		out[sym] = append(out[sym], b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, bs := range out {
		sort.Slice(bs, func(i, j int) bool { return bs[i].ts < bs[j].ts })
	}
	return out, nil
}

// episode is one burst: the bar it started from, plus its outcome.
//
// The anchor is the first bar whose 5-minute window already contains the spike,
// which by construction sits up to 5 minutes BEFORE the peak — a bar nobody can
// identify while it is happening. Its MFE therefore describes the whole move and
// its MAE is near zero, and neither number is achievable.
//
// The vis* fields fix that: they re-measure everything from the first bar where
// the move is actually VISIBLE (price already up by visiblePct). That bar is one
// a live system could react to, so those are the numbers that decide whether
// there is a trade here at all.
type episode struct {
	symbol                      string
	i                           int // index into the symbol's series
	ts                          int64
	mfe5, mae5, mfe15, secsPeak float64
	feat                        map[string]float64

	dir                          float64 // +1 long a pump, -1 short a dump
	traj                         map[int]map[string]float64
	hasVis                       bool
	visSecs                      float64 // anchor → the move becoming visible
	visMFE, visMAE, visToPeakSec float64 // measured FROM the visible bar
	entry                        float64 // close of the visible bar
	path                         []pathBar
}

// magnitude is the size of the move in the direction being studied. Reading
// mfe5 regardless of side would rank dumps by how far they bounced UP, which
// silently surfaces the wrong events and mislabels every one of their sizes.
func (e episode) magnitude() float64 {
	if e.dir < 0 {
		return -e.mae5
	}
	return e.mfe5
}

// pathBar is one bar of what happened after a (hypothetical) entry. Keeping the
// path lets the report simulate any take-profit / stop pair without re-reading
// the database, and — more importantly — forces the verdict to account for the
// losing side instead of admiring the best-case excursion.
type pathBar struct {
	high, low, close_ float64
}

// splitEpisodes finds burst starts and collects a matched baseline.
//
// Baseline bars exclude anything within an hour either side of an episode:
// including the run-up or the aftermath in "normal conditions" would blunt
// exactly the contrast the comparison exists to measure.
func splitEpisodes(series map[string][]abar, cfg AnalyzeConfig) (eps []episode, base map[string][]float64) {
	e, b, _ := splitEpisodesFull(series, cfg)
	return e, b
}

// splitEpisodesFull additionally returns each SYMBOL's own baseline.
//
// A pooled baseline mixes two completely different questions. Any feature that
// merely differs between "coins that pop" and "coins that do not" — which for a
// list containing both BTC and a fresh micro-cap is most of them — shows a gap
// even with zero timing information. Comparing a coin against ITSELF strips
// that out, leaving only "did this coin deviate from its own normal, before the
// move". That is the only version of the question an entry rule can use.
func splitEpisodesFull(series map[string][]abar, cfg AnalyzeConfig) (eps []episode, base map[string][]float64, bySym map[string]map[string][]float64) {
	base = map[string][]float64{}
	bySym = map[string]map[string][]float64{}
	gapMs := int64(cfg.EpisodeGap / time.Millisecond)
	visPct := cfg.VisiblePct
	if visPct <= 0 {
		visPct = autoVisible(cfg.EventPct)
	}

	dir := dirOf(cfg.Side)
	isEvent := func(b abar) bool {
		if !b.labelled {
			return false
		}
		if dir < 0 {
			return b.mae5 <= -cfg.EventPct
		}
		return b.mfe5 >= cfg.EventPct
	}

	for sym, bs := range series {
		var lastEventTs int64 = math.MinInt32
		excl := map[int]bool{}
		for i, b := range bs {
			if !isEvent(b) {
				continue
			}
			if b.ts-lastEventTs <= gapMs {
				lastEventTs = b.ts
				continue // same burst, already anchored
			}
			lastEventTs = b.ts
			f, ok := featuresAt(bs, i)
			if !ok {
				continue // not enough history behind this bar
			}
			e := episode{symbol: sym, i: i, ts: b.ts, dir: dir,
				mfe5: b.mfe5, mae5: b.mae5, mfe15: b.mfe15, secsPeak: b.secsPeak, feat: f}
			e.traj = trajectoryAt(bs, i)
			addTradableView(&e, bs, i, visPct, dir)
			eps = append(eps, e)
		}
		// mark the neighbourhood of every event bar as unusable for baseline
		for i, b := range bs {
			if isEvent(b) {
				for j := i - winLong; j <= i+winLong; j++ {
					if j >= 0 && j < len(bs) {
						excl[j] = true
					}
				}
			}
		}
		for i := range bs {
			if excl[i] {
				continue
			}
			if f, ok := featuresAt(bs, i); ok {
				if bySym[sym] == nil {
					bySym[sym] = map[string][]float64{}
				}
				for k, v := range f {
					base[k] = append(base[k], v)
					bySym[sym][k] = append(bySym[sym][k], v)
				}
			}
		}
	}
	sort.Slice(eps, func(i, j int) bool { return eps[i].ts < eps[j].ts })
	return eps, base, bySym
}

// featuresAt computes the strictly backward-looking feature set at bs[i].
// Returns ok=false when there is not enough history behind the bar.
func featuresAt(bs []abar, i int) (map[string]float64, bool) {
	if i < winLong {
		return nil, false
	}
	b := bs[i]
	// windows are index-based here, but the collector writes one row per minute
	// per symbol, so an index gap IS a time gap — verify it before trusting the
	// window, otherwise a collection outage silently widens every lookback.
	if b.ts-bs[i-winLong].ts != int64(winLong)*60_000 {
		return nil, false
	}
	f := map[string]float64{}
	f["資金費率 funding(bps)"] = b.funding * 10000

	if o := bs[i-winShort].oiUSD; o > 0 && b.oiUSD > 0 {
		f["OI 15m 變化 %"] = (b.oiUSD/o - 1) * 100
	}
	if o := bs[i-winLong].oiUSD; o > 0 && b.oiUSD > 0 {
		f["OI 60m 變化 %"] = (b.oiUSD/o - 1) * 100
	}
	if c := bs[i-winShort].close_; c > 0 {
		f["價格 15m 報酬 %"] = (b.close_/c - 1) * 100
	}
	// volume z-score against the trailing hour: an absolute multiple of a
	// moving average means different things on a dead coin and a busy one.
	var sum, sum2 float64
	var n float64
	var lowest = math.MaxFloat64
	for j := i - winLong; j < i; j++ {
		sum += bs[j].volQuote
		sum2 += bs[j].volQuote * bs[j].volQuote
		n++
		if bs[j].low > 0 && bs[j].low < lowest {
			lowest = bs[j].low
		}
	}
	if n > 1 {
		mean := sum / n
		varr := sum2/n - mean*mean
		if varr > 0 {
			f["成交量 z-score(60m)"] = (b.volQuote - mean) / math.Sqrt(varr)
		}
	}
	if lowest < math.MaxFloat64 && lowest > 0 {
		f["距 60m 低點 %"] = (b.close_/lowest - 1) * 100
	}
	if b.volQuote > 0 {
		f["主動買佔比"] = b.takerBuyQuote / b.volQuote
	}
	if b.indexPx > 0 && b.mark > 0 {
		f["基差 basis(bps)"] = (b.mark - b.indexPx) / b.indexPx * 10000
	}
	return f, true
}

// addTradableView re-measures the episode from the first bar a live system
// could have reacted to, rather than from the unknowable anchor.
func addTradableView(e *episode, bs []abar, i int, visiblePct, dir float64) {
	base := bs[i].close_
	if base <= 0 {
		return
	}
	// walk forward within the 5-minute window looking for the move to show up
	vis := -1
	for j := i + 1; j < len(bs) && bs[j].ts <= bs[i].ts+5*60_000; j++ {
		if dir*(bs[j].close_/base-1) >= visiblePct {
			vis = j
			break
		}
	}
	if vis < 0 {
		return // the spike never showed in the closes — a wick-only move
	}
	entry := bs[vis].close_
	if entry <= 0 {
		return
	}
	e.hasVis = true
	e.visSecs = float64(bs[vis].ts-bs[i].ts) / 1000

	// From the visible bar, look ahead the same 5 minutes an entry would hold.
	// "best" and "worst" are relative to the side being traded: a short profits
	// from the low and is hurt by the high, so the two swap rather than being
	// re-derived — a separate down-side formula is how the tails drift apart.
	var best, worst float64
	var peakTs int64
	have := false
	for j := vis + 1; j < len(bs) && bs[j].ts <= bs[vis].ts+5*60_000; j++ {
		fav, adv := bs[j].high, bs[j].low
		if dir < 0 {
			fav, adv = bs[j].low, bs[j].high
		}
		if !have {
			best, worst, peakTs, have = fav, adv, bs[j].ts, true
			continue
		}
		if dir*(fav-best) > 0 {
			best, peakTs = fav, bs[j].ts
		}
		if dir*(adv-worst) < 0 {
			worst = adv
		}
	}
	if !have {
		return
	}
	e.visMFE = dir * (best/entry - 1)
	e.visMAE = dir * (worst/entry - 1)
	e.visToPeakSec = float64(peakTs-bs[vis].ts) / 1000
	e.entry = entry
	for j := vis + 1; j < len(bs) && bs[j].ts <= bs[vis].ts+5*60_000; j++ {
		e.path = append(e.path, pathBar{bs[j].high, bs[j].low, bs[j].close_})
	}
}

// simulate walks one episode's path under a take-profit / stop-loss pair with a
// time stop at the end of the window, and returns the NET return after cost.
//
// When a bar's range spans both levels the stop is assumed to hit first. Bar
// data cannot say which came first within the minute, and choosing the
// favourable reading is how a backtest quietly turns losses into wins.
func (e episode) simulate(tp, sl, cost float64) float64 {
	d := e.dir
	if d == 0 {
		d = 1
	}
	for _, b := range e.path {
		adv, fav := b.low, b.high
		if d < 0 {
			adv, fav = b.high, b.low
		}
		if d*(adv/e.entry-1) <= -sl {
			return -sl - cost
		}
		if d*(fav/e.entry-1) >= tp {
			return tp - cost
		}
	}
	if len(e.path) == 0 {
		return 0
	}
	return d*(e.path[len(e.path)-1].close_/e.entry-1) - cost
}

// trajOffsets are how many minutes BEFORE the anchor each column is sampled.
//
// This is the whole question: an indicator that only differs at T-0 is
// coincident and useless for entry — by the time it moves, so has price. One
// that already differs at T-20 is a genuine early warning, and is the only kind
// worth putting in a screener.
var trajOffsets = []int{30, 20, 15, 10, 5, 3, 1, 0}

// trajectoryAt samples the feature set at each offset before bs[i].
func trajectoryAt(bs []abar, i int) map[int]map[string]float64 {
	out := map[int]map[string]float64{}
	for _, off := range trajOffsets {
		j := i - off
		if j < winLong {
			continue
		}
		// the offset must be a real time distance, not just a row distance
		if bs[i].ts-bs[j].ts != int64(off)*60_000 {
			continue
		}
		if f, ok := featuresAt(bs, j); ok {
			out[off] = f
		}
	}
	return out
}

// ---------- report ----------

type report struct{ w io.Writer }

func newReport(w io.Writer) *report { return &report{w} }

func (r *report) line(f string, a ...any) { fmt.Fprintf(r.w, f+"\n", a...) }
func (r *report) rule()                   { r.line("%s", strings.Repeat("─", 74)) }
func (r *report) head(s string) {
	fmt.Fprintln(r.w)
	r.rule()
	r.line("  %s", s)
	r.rule()
}

func (r *report) health(series map[string][]abar) {
	var rows, labelled int
	var minTs, maxTs int64 = math.MaxInt64, 0
	mins := map[int64]bool{}
	for _, bs := range series {
		for _, b := range bs {
			rows++
			if b.labelled {
				labelled++
			}
			if b.ts < minTs {
				minTs = b.ts
			}
			if b.ts > maxTs {
				maxTs = b.ts
			}
			mins[b.ts] = true
		}
	}
	expect := (maxTs-minTs)/60_000 + 1
	r.head("資料健康")
	r.line("  期間          %s → %s",
		time.UnixMilli(minTs).UTC().Format("01-02 15:04"),
		time.UnixMilli(maxTs).UTC().Format("01-02 15:04 (UTC)"))
	r.line("  標的 / 列數   %d 檔 / %d 列", len(series), rows)
	r.line("  分鐘覆蓋      %d / %d  (%.1f%%)", len(mins), expect, float64(len(mins))/float64(expect)*100)
	r.line("  已標記        %d 列 (%.1f%%) — 最近一小時尚在前向視窗內,未標記屬正常",
		labelled, float64(labelled)/float64(rows)*100)
}

func (r *report) q1(eps []episode, series map[string][]abar, cfg AnalyzeConfig) {
	dirWord, sign := "上漲", "+"
	if dirOf(cfg.Side) < 0 {
		dirWord, sign = "下跌", "-"
	}
	r.head(fmt.Sprintf("Q1 機會頻率 — 5 分鐘內%s ≥ %s%.0f%%", dirWord, sign, cfg.EventPct*100))
	if len(eps) == 0 {
		r.line("  這段期間沒有任何事件。")
		r.line("  資料還太少,或這個門檻對目前的市況太高 — 用 -event-pct 0.05 再看一次。")
		return
	}
	span := float64(eps[len(eps)-1].ts-eps[0].ts) / float64(24*time.Hour/time.Millisecond)
	if span < 0.01 {
		span = 0.01
	}
	byCoin := map[string]int{}
	byHour := map[int]int{}
	for _, e := range eps {
		byCoin[e.symbol]++
		byHour[time.UnixMilli(e.ts).UTC().Hour()]++
	}
	r.line("  事件數        %d 次(已合併同一波,間隔 <%s 視為同一次)", len(eps), cfg.EpisodeGap)
	r.line("  平均每天      %.1f 次", float64(len(eps))/span)
	r.line("  涉及標的      %d 檔", len(byCoin))

	type kv struct {
		k string
		n int
	}
	var top []kv
	for k, n := range byCoin {
		top = append(top, kv{k, n})
	}
	sort.Slice(top, func(i, j int) bool {
		if top[i].n != top[j].n {
			return top[i].n > top[j].n
		}
		return top[i].k < top[j].k
	})
	if len(top) > cfg.TopCoins {
		top = top[:cfg.TopCoins]
	}
	r.line("")
	r.line("  最常爆的標的(這就是「易爆池」的雛型):")
	for _, t := range top {
		r.line("    %-16s %d 次  %s", t.k, t.n, strings.Repeat("█", t.n))
	}
	r.line("")
	r.line("  時段分佈 (UTC 小時):")
	for h := 0; h < 24; h++ {
		if byHour[h] > 0 {
			r.line("    %02d:00  %-3d %s", h, byHour[h], strings.Repeat("▪", byHour[h]))
		}
	}
}

func (r *report) q4(eps []episode, cfg AnalyzeConfig, visPct, lev float64) {
	r.head("Q4 MFE / MAE 分佈 — 決定停利、停損、時間停損")
	if len(eps) == 0 {
		return
	}
	get := func(f func(episode) float64) []float64 {
		var v []float64
		for _, e := range eps {
			v = append(v, f(e))
		}
		sort.Float64s(v)
		return v
	}
	show := func(name string, v []float64, mul float64, unit string) {
		if len(v) == 0 {
			return
		}
		r.line("  %-22s p10 %7.1f%s   p25 %7.1f%s   中位 %7.1f%s   p75 %7.1f%s   p90 %7.1f%s",
			name, pct(v, 0.10)*mul, unit, pct(v, 0.25)*mul, unit, pct(v, 0.50)*mul, unit,
			pct(v, 0.75)*mul, unit, pct(v, 0.90)*mul, unit)
	}
	show("MFE 5m(最大有利)", get(func(e episode) float64 { return e.mfe5 }), 100, "%")
	show("MAE 5m(最大不利)", get(func(e episode) float64 { return e.mae5 }), 100, "%")
	show("MFE 15m", get(func(e episode) float64 { return e.mfe15 }), 100, "%")
	show("到頂秒數 15m", get(func(e episode) float64 { return e.secsPeak }), 1, "s")

	r.line("")
	for _, tgt := range []float64{0.10, 0.15, 0.20, 0.30} {
		var hit int
		for _, e := range eps {
			if e.mfe5 >= tgt {
				hit++
			}
		}
		r.line("  觸及 +%2.0f%% 的比例   %5.1f%%  (%d/%d)", tgt*100,
			float64(hit)/float64(len(eps))*100, hit, len(eps))
	}
	r.line("")
	r.line("  ⚠ 以上是從「錨點」量的,而錨點是尖峰前最多 5 分鐘那根 K 棒 —")
	r.line("    當下你認不出它。所以 MFE 偏大、MAE 偏小、到頂秒數會被釘在 300 秒附近,")
	r.line("    這是錨定方式的產物,不是行情的性質。真正能拿到的看下一段。")

	r.tradable(eps, visPct, lev)
}

// tradable re-states the same episodes from the first bar a live system could
// have reacted to. The gap between this section and the one above IS the cost
// of detection — and it is usually where a promising-looking edge disappears.
func (r *report) tradable(eps []episode, visPct, lev float64) {
	r.head(fmt.Sprintf("Q4b 可交易性 — 從價格已經動了 +%.1f%% 之後才進場", visPct*100))
	var vis []episode
	for _, e := range eps {
		if e.hasVis {
			vis = append(vis, e)
		}
	}
	if len(vis) == 0 {
		r.line("  沒有任何一次在收盤價上顯現出來(可能都是上影線) — 樣本太少或行情形態如此。")
		return
	}
	r.line("  可辨識的次數  %d / %d", len(vis), len(eps))
	const cost = 0.003 // 來回手續費 + 小幣滑價,保守取 0.3%

	col := func(f func(episode) float64) []float64 {
		var v []float64
		for _, e := range vis {
			v = append(v, f(e))
		}
		sort.Float64s(v)
		return v
	}
	show := func(name string, v []float64, mul float64, unit string) {
		r.line("  %-22s p10 %7.1f%s   中位 %7.1f%s   p90 %7.1f%s",
			name, pct(v, 0.10)*mul, unit, pct(v, 0.50)*mul, unit, pct(v, 0.90)*mul, unit)
	}
	show("偵測延遲(錨點→可辨識)", col(func(e episode) float64 { return e.visSecs }), 1, "s")
	mfe := col(func(e episode) float64 { return e.visMFE })
	mae := col(func(e episode) float64 { return e.visMAE })
	show("進場後 MFE(最好情況)", mfe, 100, "%")
	show("進場後 MAE(最壞情況)", mae, 100, "%")
	toPeak := col(func(e episode) float64 { return e.visToPeakSec })
	show("進場→到頂", toPeak, 1, "s")

	// MFE alone flatters everything: it is the best price the move ever offered,
	// which nobody exits at. The verdict has to come from a rule that can lose.
	r.line("")
	r.line("  停利/停損模擬(進場後 5 分鐘時間停損,來回成本 %.1f%%,同一根同時觸及則算停損):", cost*100)
	r.line("  %-14s %8s %12s %12s", "TP / SL", "命中率", "平均淨報酬", "總淨報酬")
	r.line("  %s", strings.Repeat("-", 50))
	type grid struct{ tp, sl float64 }
	for _, g := range []grid{{0.03, 0.02}, {0.05, 0.02}, {0.05, 0.03}, {0.08, 0.03}} {
		var wins int
		var total float64
		for _, e := range vis {
			ret := e.simulate(g.tp, g.sl, cost)
			total += ret
			if ret > 0 {
				wins++
			}
		}
		avg := total / float64(len(vis))
		r.line("  +%.0f%% / -%.0f%%      %6.1f%% %11.2f%% %11.2f%%",
			g.tp*100, g.sl*100, float64(wins)/float64(len(vis))*100, avg*100, total*100)
	}

	r.line("")
	r.line("  ⚠ 這張表同樣只跑在「事後確認有行情」的那些 K 棒上,所以命中率與總報酬都偏樂觀。")
	r.line("    實盤數字看最上面掃描表的「全觸發淨報酬」那一欄。")
	r.leverage(vis, mae, cost, lev)

	r.line("")
	medToPeak := pct(toPeak, 0.5)
	if medToPeak <= 90 {
		r.line("  ⚠ 從看得見到見頂只有 %.0f 秒(中位)— 人工點擊來不及,這條路必須自動化,",
			medToPeak)
		r.line("    否則就得放棄第一波、改抓回踩或同板塊落後幣。")
	} else {
		r.line("  從看得見到見頂 %.0f 秒(中位)— 人工仍有機會,但滑價會吃掉可觀比例。", medToPeak)
	}
	if p10 := pct(mfe, 0.10); p10 < cost {
		r.line("  ⚠ 最差的 10%% 進場後只剩 %.1f%% 空間 — 有一部分是「看到就已經結束」,", p10*100)
		r.line("    這些單注定虧手續費,必須靠停損規則而不是靠訊號來控制。")
	}
	if p10 := pct(mae, 0.10); p10 < -0.05 {
		r.line("  ⚠ 最差的 10%% 進場後回撤達 %.1f%% — 停損若要涵蓋它,風報比會很難看。", p10*100)
	}
}

func (r *report) q3(eps []episode, base map[string][]float64) {
	r.head("Q3 事件前 vs 平常 — 哪些指標真的有領先性")
	if len(eps) == 0 {
		return
	}
	evt := map[string][]float64{}
	for _, e := range eps {
		for k, v := range e.feat {
			evt[k] = append(evt[k], v)
		}
	}
	var keys []string
	for k := range evt {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	r.line("  %-24s %12s %12s %10s", "指標(事件當根)", "事件中位數", "平常中位數", "樣本")
	r.line("  %s", strings.Repeat("-", 62))
	for _, k := range keys {
		ev, bs := evt[k], base[k]
		if len(ev) == 0 || len(bs) == 0 {
			continue
		}
		sort.Float64s(ev)
		sort.Float64s(bs)
		r.line("  %-24s %12.3f %12.3f %10d", k, pct(ev, 0.5), pct(bs, 0.5), len(ev))
	}
	r.line("")
	r.line("  讀法:兩欄差距不明顯的指標就是沒有資訊量,別再往上加權重。")
	r.line("  這些是「事件當根」的值 — 真正有領先性的,應該在事件發生前就已經偏離。")
}

// leverage restates the outcome in margin terms and, above all, checks how
// often the position would simply have been closed by the exchange.
//
// At high leverage the stop is no longer a choice. Liquidation sits roughly
// 1/leverage away, maintenance margin brings it slightly nearer still, and any
// adverse excursion past it ends the trade at a total loss of margin no matter
// what stop was intended. A plan whose typical drawdown approaches that
// distance is not a plan with a wide stop — it is a plan with no stop at all.
func (r *report) leverage(vis []episode, maeSorted []float64, cost, lev float64) {
	if lev <= 1 || len(vis) == 0 {
		return
	}
	liq := 1 / lev // optimistic: maintenance margin makes it hit marginally sooner
	var blown int
	for _, e := range vis {
		if e.visMAE <= -liq {
			blown++
		}
	}
	r.line("")
	r.line("  ── %.0fx 槓桿的現實 ──", lev)
	r.line("  強平距離        約 -%.2f%%(維持保證金會讓它再近一點)", liq*100)
	r.line("  進場後觸及該距離 %.1f%%  (%d/%d) — 這些單直接爆倉,保證金歸零",
		float64(blown)/float64(len(vis))*100, blown, len(vis))
	if len(maeSorted) > 0 {
		r.line("  進場後 MAE 的 p10 是 %.1f%%,p25 是 %.1f%% — 停損必須設在強平之內才有意義",
			pct(maeSorted, 0.10)*100, pct(maeSorted, 0.25)*100)
	}
	r.line("  單趟成本 %.2f%% 的價格波動,在 %.0fx 下等於保證金的 %.1f%%",
		cost*100, lev, cost*lev*100)
	if blown > 0 {
		r.line("  ⚠ 槓桿放大的是既有的邊,不是修正它。若「全觸發淨報酬」為負,")
		r.line("    %.0fx 只會讓同一個負期望值以 %.0f 倍的速度實現。", lev, lev)
	}
}

// q3b answers "does anything move BEFORE the move", measured against each
// coin's OWN normal rather than the market's.
//
// Reported as a deviation, so a row sitting near zero across every column has
// no timing information regardless of how different that coin looks from the
// market on average.
func (r *report) q3b(eps []episode, bySym map[string]map[string][]float64) {
	r.head("Q3b 事件前的軌跡 — 相對「同一隻幣自己的常態」偏離多少")
	if len(eps) == 0 {
		return
	}
	// each symbol's own median per feature
	med := map[string]map[string]float64{}
	for sym, feats := range bySym {
		med[sym] = map[string]float64{}
		for k, v := range feats {
			sort.Float64s(v)
			med[sym][k] = pct(v, 0.5)
		}
	}
	names := map[string]bool{}
	for _, e := range eps {
		for _, f := range e.traj {
			for k := range f {
				names[k] = true
			}
		}
	}
	var keys []string
	for k := range names {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		r.line("  樣本不足以構成軌跡(事件太靠近資料起點)。")
		return
	}

	hdr := "  %-22s"
	args := []any{"指標(與自身常態的差)"}
	for _, off := range trajOffsets {
		hdr += " %8s"
		if off == 0 {
			args = append(args, "T-0")
		} else {
			args = append(args, fmt.Sprintf("T-%d", off))
		}
	}
	r.line(hdr, args...)
	r.line("  %s", strings.Repeat("-", 22+9*len(trajOffsets)))

	for _, k := range keys {
		row := "  %-22s"
		vals := []any{k}
		for _, off := range trajOffsets {
			var d []float64
			for _, e := range eps {
				f, ok := e.traj[off]
				if !ok {
					continue
				}
				x, ok2 := f[k]
				if !ok2 {
					continue
				}
				m, ok3 := med[e.symbol][k]
				if !ok3 {
					continue
				}
				d = append(d, x-m)
			}
			row += " %8s"
			if len(d) == 0 {
				vals = append(vals, "-")
				continue
			}
			sort.Float64s(d)
			vals = append(vals, fmt.Sprintf("%+.2f", pct(d, 0.5)))
		}
		r.line(row, vals...)
	}
	r.line("")
	r.line("  0 代表「跟這隻幣平常沒兩樣」。整列都貼近 0 的指標,對『何時進場』毫無資訊,")
	r.line("  即使它在上一張表裡跟全市場差很多 —— 那只是說明這隻幣本來就跟大盤不同。")
	r.line("  要找的是在 T-10、T-20 就明顯不為 0,且往 T-0 持續放大的那幾列。")
}

func (r *report) footer(eps []episode, cfg AnalyzeConfig) {
	r.head("下一步")
	switch {
	case len(eps) == 0:
		r.line("  先讓資料再累積幾天,或降低 -event-pct 看看較小的波動。")
	case len(eps) < 30:
		r.line("  樣本僅 %d 筆,還不足以下結論 — 三位數以上再認真看 Q3。", len(eps))
	default:
		r.line("  樣本 %d 筆,Q1 和 Q4 已經可以判讀。", len(eps))
		r.line("  接著才輪到 Q2(lift):用 Q3 裡真的有差距的欄位組成燃料分數,")
		r.line("  再比較「分數前 15 名」與「全市場平均」的事件發生率。")
	}
	r.line("")
	r.line("  提醒:成本(來回手續費 + 滑價)抓 0.2~0.4%%,所以 MAE 的 p25 若比它還深,")
	r.line("  停損就會被雜訊掃到 — 這條線比進場訊號更決定期望值。")
}

// pct returns the q-quantile of a sorted slice (nearest-rank).
func pct(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(q * float64(len(sorted)-1))
	if i < 0 {
		i = 0
	}
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i]
}
