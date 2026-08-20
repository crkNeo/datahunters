package collector

import (
	"database/sql"
	"fmt"
	"io"
	"sort"
	"time"
)

// patternbt.go replays the live detectors over collected history.
//
// Without this, tuning a threshold means reading a handful of charts and
// guessing — which is how the first version ended up demanding a 5% OI jump
// because one case happened to show 6.2%. Replaying answers the two questions
// that actually matter, and answers them on every bar rather than on the ones
// that were easy to notice:
//
//	precision — of the signals that fired, how many paid?
//	recall    — of the moves that happened, how many did we fire on?
//
// A detector can be wrong in both directions and only one of them is visible
// from the live board: a rule that never fires looks flawless there.

// PatternBacktest replays both detectors and writes the report to w.
func PatternBacktest(db *sql.DB, cfg AnalyzeConfig, w io.Writer) error {
	from := int64(0)
	if cfg.Days > 0 {
		from = time.Now().Add(-time.Duration(cfg.Days) * 24 * time.Hour).UnixMilli()
	}
	series, err := loadPatternSeries(db, from, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	if len(series) == 0 {
		fmt.Fprintln(w, "沒有資料 — 採集器跑起來了嗎?")
		return nil
	}

	type shot struct {
		sym  string
		i    int
		pat  string
		mfe5 float64
		mae5 float64
		ret5 float64
	}
	var shots []shot
	var span float64
	for sym, bs := range series {
		if len(bs) > 1 {
			d := float64(bs[len(bs)-1].ts-bs[0].ts) / float64(24*time.Hour/time.Millisecond)
			if d > span {
				span = d
			}
		}
		for i := range bs {
			for _, c := range []struct {
				pat string
				fn  func(string, []pbar, int) (patternHit, bool)
			}{{"A", detectA}, {"B", detectB}} {
				if _, ok := c.fn(sym, bs, i); !ok {
					continue
				}
				mfe, mae, ret, ok := forwardFrom(bs, i, 5)
				if !ok {
					continue
				}
				shots = append(shots, shot{sym, i, c.pat, mfe, mae, ret})
			}
		}
	}
	if span < 0.01 {
		span = 0.01
	}

	rep := newReport(w)
	rep.head("型態回放 — 把現在的偵測條件跑過全部歷史")
	rep.line("  期間 %.1f 天,標的 %d 檔", span, len(series))
	rep.line("")
	rep.line("  %-6s %8s %9s %10s %10s %11s %12s", "型態", "觸發數", "每天", "命中率", "MFE中位", "MAE中位", "平均淨報酬")
	rep.line("  %s", "--------------------------------------------------------------------------")
	for _, pat := range []string{"A", "B"} {
		var mfe, mae, ret []float64
		for _, s := range shots {
			if s.pat != pat {
				continue
			}
			mfe = append(mfe, s.mfe5)
			mae = append(mae, s.mae5)
			ret = append(ret, s.ret5)
		}
		if len(mfe) == 0 {
			rep.line("  %-6s %8d %9s %10s %10s %11s %12s", pat, 0, "-", "-", "-", "-", "-")
			continue
		}
		sort.Float64s(mfe)
		sort.Float64s(mae)
		var wins int
		var tot float64
		for _, r := range ret {
			tot += r
		}
		for _, m := range mfe {
			if m >= 0.01 {
				wins++
			}
		}
		const cost = 0.003
		rep.line("  %-6s %8d %9.1f %9.1f%% %9.2f%% %10.2f%% %11.2f%%",
			pat, len(mfe), float64(len(mfe))/span,
			float64(wins)/float64(len(mfe))*100,
			pct(mfe, 0.5)*100, pct(mae, 0.5)*100,
			(tot/float64(len(ret))-cost)*100)
	}

	// recall: of the real moves, how many did a detector fire anywhere near?
	rep.line("")
	rep.line("  ── 涵蓋率:實際發生的行情,偵測器抓到幾個 ──")
	// Episodes come from the labelled view, which is a different row shape than
	// the detector's input — loaded separately rather than converted, so neither
	// side has to know about the other's fields.
	lab, err := loadAnalysisSeries(db, from)
	if err != nil {
		return err
	}
	eps, _, _ := splitEpisodesFull(lab, cfg)
	sort.Slice(eps, func(i, j int) bool { return eps[i].magnitude() > eps[j].magnitude() })
	top := eps
	if len(top) > 20 {
		top = top[:20]
	}
	fired := map[string]bool{}
	for _, s := range shots {
		bs := series[s.sym]
		fired[fmt.Sprintf("%s|%d", s.sym, bs[s.i].ts/60_000)] = true
	}
	var hitN int
	rep.line("  %-16s %10s %12s %8s", "標的", "幅度", "進場後可得", "有觸發")
	rep.line("  %s", "-----------------------------------------------")
	for _, e := range top {
		// a detector counts as covering the move if it fired within the 10
		// minutes before the anchor or the 5 after — close enough to be the
		// same trade rather than a coincidence elsewhere in the day
		hit := false
		for d := -10; d <= 5 && !hit; d++ {
			if fired[fmt.Sprintf("%s|%d", e.symbol, (e.ts+int64(d)*60_000)/60_000)] {
				hit = true
			}
		}
		mark := "—"
		if hit {
			mark, hitN = "✓", hitN+1
		}
		rep.line("  %-16s %9.1f%% %11.1f%% %8s", e.symbol, e.magnitude()*100, e.visMFE*100, mark)
	}
	if len(top) > 0 {
		rep.line("")
		rep.line("  涵蓋 %d / %d = %.0f%%", hitN, len(top), float64(hitN)/float64(len(top))*100)
		rep.line("")
		rep.line("  抓不到的比抓錯的更難察覺 —— 後台看板只會顯示有觸發的那些,")
		rep.line("  一條永遠不觸發的規則在那裡看起來是完美的。")
	}
	return nil
}

// forwardFrom measures the 5-minute outcome from bs[i], by timestamp so a
// collection gap cannot stretch the window.
func forwardFrom(bs []pbar, i, mins int) (mfe, mae, ret float64, ok bool) {
	base := bs[i].close_
	if base <= 0 {
		return 0, 0, 0, false
	}
	limit := bs[i].ts + int64(mins)*60_000
	var hi, lo, last float64
	have := false
	for j := i + 1; j < len(bs) && bs[j].ts <= limit; j++ {
		if !have {
			hi, lo, have = bs[j].high, bs[j].low, true
		}
		if bs[j].high > hi {
			hi = bs[j].high
		}
		if bs[j].low < lo {
			lo = bs[j].low
		}
		last = bs[j].close_
	}
	if !have || last <= 0 {
		return 0, 0, 0, false
	}
	return hi/base - 1, lo/base - 1, last/base - 1, true
}
