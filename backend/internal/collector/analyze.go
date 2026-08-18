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
}

func DefaultAnalyzeConfig() AnalyzeConfig {
	return AnalyzeConfig{Days: 0, EventPct: 0.10, EpisodeGap: 30 * time.Minute, TopCoins: 12}
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

	rep := newReport(w)
	rep.health(series)

	eps, base := splitEpisodes(series, cfg)
	rep.q1(eps, series, cfg)
	rep.q4(eps, cfg)
	rep.q3(eps, base)
	rep.footer(eps, cfg)
	return nil
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
type episode struct {
	symbol                      string
	i                           int // index into the symbol's series
	ts                          int64
	mfe5, mae5, mfe15, secsPeak float64
	feat                        map[string]float64
}

// splitEpisodes finds burst starts and collects a matched baseline.
//
// Baseline bars exclude anything within an hour either side of an episode:
// including the run-up or the aftermath in "normal conditions" would blunt
// exactly the contrast the comparison exists to measure.
func splitEpisodes(series map[string][]abar, cfg AnalyzeConfig) (eps []episode, base map[string][]float64) {
	base = map[string][]float64{}
	gapMs := int64(cfg.EpisodeGap / time.Millisecond)

	for sym, bs := range series {
		var lastEventTs int64 = math.MinInt32
		excl := map[int]bool{}
		for i, b := range bs {
			if !b.labelled || b.mfe5 < cfg.EventPct {
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
			eps = append(eps, episode{symbol: sym, i: i, ts: b.ts,
				mfe5: b.mfe5, mae5: b.mae5, mfe15: b.mfe15, secsPeak: b.secsPeak, feat: f})
		}
		// mark the neighbourhood of every event bar as unusable for baseline
		for i, b := range bs {
			if b.labelled && b.mfe5 >= cfg.EventPct {
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
				for k, v := range f {
					base[k] = append(base[k], v)
				}
			}
		}
	}
	sort.Slice(eps, func(i, j int) bool { return eps[i].ts < eps[j].ts })
	return eps, base
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
	r.head(fmt.Sprintf("Q1 機會頻率 — 5 分鐘內 ≥ +%.0f%%", cfg.EventPct*100))
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

func (r *report) q4(eps []episode, cfg AnalyzeConfig) {
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
	secs := get(func(e episode) float64 { return e.secsPeak })
	if len(secs) > 0 {
		med := pct(secs, 0.50)
		r.line("")
		if med <= 120 {
			r.line("  ⚠ 到頂中位數僅 %.0f 秒 — 人工點擊來不及,只剩自動化或改抓第二波。", med)
		} else {
			r.line("  到頂中位數 %.0f 秒 — 人工反應仍有機會,但滑價會吃掉大部分。", med)
		}
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
