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
	var dataLo int64 = 1 << 62
	var dataHi int64
	// One setup can stay true for several consecutive bars. Counting each of
	// them as a separate trade inflates the sample with near-identical,
	// perfectly correlated outcomes — the fastest way to turn three
	// observations into a confident-looking thirty.
	cooldown := int(cfg.EpisodeGap / time.Minute)
	for sym, bs := range series {
		lastFire := map[string]int{}
		if len(bs) > 1 {
			d := float64(bs[len(bs)-1].ts-bs[0].ts) / float64(24*time.Hour/time.Millisecond)
			if d > span {
				span = d
			}
			if bs[0].ts < dataLo {
				dataLo = bs[0].ts
			}
			if bs[len(bs)-1].ts > dataHi {
				dataHi = bs[len(bs)-1].ts
			}
		}
		for i := range bs {
			for _, c := range []struct {
				pat string
				fn  func(string, []pbar, int) (patternHit, bool)
			}{{"A", detectA}, {"B", detectB}} {
				if last, seen := lastFire[c.pat]; seen && i-last < cooldown {
					continue
				}
				if _, ok := c.fn(sym, bs, i); !ok {
					continue
				}
				lastFire[c.pat] = i
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
	rep.line("  期間 %.1f 天,標的 %d 檔(同一標的同型態 %d 分鐘內只算一次)",
		span, len(series), int(cfg.EpisodeGap/time.Minute))
	if cfg.OOSFrom > 0 {
		// The year is printed deliberately. Without it a boundary set a year off
		// looks identical to a correct one, every row lands on one side, and the
		// split silently reports nothing while appearing to work.
		rep.line("  樣本外起點 %s UTC — 之前的資料是條件被寫出來時看過的,",
			time.UnixMilli(cfg.OOSFrom).UTC().Format("2006-01-02 15:04"))
		rep.line("  在那上面的表現不算證據,只有之後的才算。")
		if cfg.OOSFrom < dataLo || cfg.OOSFrom > dataHi {
			rep.line("")
			rep.line("  ⚠ 這個起點落在資料範圍(%s ~ %s UTC)之外,",
				time.UnixMilli(dataLo).UTC().Format("2006-01-02 15:04"),
				time.UnixMilli(dataHi).UTC().Format("2006-01-02 15:04"))
			rep.line("    所有紀錄都會被歸到同一側 —— 這樣的切分沒有意義,請確認年份。")
		}
	}
	rep.line("")
	rep.line("  %-10s %8s %9s %10s %10s %11s %12s", "型態", "觸發數", "每天", "命中率", "MFE中位", "MAE中位", "平均淨報酬")
	rep.line("  %s", "--------------------------------------------------------------------------")
	rows := []struct {
		label  string
		lo, hi int64
	}{{"全部", 0, 1 << 62}}
	if cfg.OOSFrom > 0 {
		rows = []struct {
			label  string
			lo, hi int64
		}{{"設計期內", 0, cfg.OOSFrom}, {"樣本外", cfg.OOSFrom, 1 << 62}}
	}
	for _, win := range rows {
		for _, pat := range []string{"A", "B"} {
			var mfe, mae, ret []float64
			for _, s := range shots {
				if s.pat != pat {
					continue
				}
				if ts := series[s.sym][s.i].ts; ts < win.lo || ts >= win.hi {
					continue
				}
				mfe = append(mfe, s.mfe5)
				mae = append(mae, s.mae5)
				ret = append(ret, s.ret5)
			}
			name := pat
			if cfg.OOSFrom > 0 {
				name = win.label + " " + pat
			}
			if len(mfe) == 0 {
				rep.line("  %-10s %8d %9s %10s %10s %11s %12s", name, 0, "-", "-", "-", "-", "-")
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
			rep.line("  %-10s %8d %9.1f %9.1f%% %9.2f%% %10.2f%% %11.2f%%",
				name, len(mfe), float64(len(mfe))/windowDays(win.lo, win.hi, dataLo, dataHi),
				float64(wins)/float64(len(mfe))*100,
				pct(mfe, 0.5)*100, pct(mae, 0.5)*100,
				(tot/float64(len(ret))-cost)*100)
		}
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

func maxI64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func minI64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// windowDays is the length in days of the slice of collected data a window
// actually covers, clamped to what exists.
//
// The rate column has to divide by THIS, not by the whole collection period.
// A short out-of-sample stretch scored against the full span is understated by
// the ratio of the two — 22 firings in five hours read as 8 a day rather than
// 106, which is the difference between "occasional" and "constant".
func windowDays(lo, hi, dataLo, dataHi int64) float64 {
	const day = float64(24 * time.Hour / time.Millisecond)
	if lo < dataLo {
		lo = dataLo
	}
	if hi > dataHi {
		hi = dataHi
	}
	d := float64(hi-lo) / day
	if d < 0.01 {
		return 0.01 // guard the divide; a sub-15-minute window is not a rate
	}
	return d
}
