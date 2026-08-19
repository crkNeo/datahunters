package cache

import (
	"database/sql"
	"time"
)

// patterns.go serves the 爆發型態 board: the A / B setups the collector detects
// live, together with what actually happened after each firing.
//
// The rows are written by cmd/collector, not by this process. That separation
// is what makes the board trustworthy: a hit is recorded the moment it fires,
// and its outcome is filled in later by a job that cannot influence whether the
// hit was recorded. What the admin sees is therefore a genuine out-of-sample
// tally rather than a list curated after the fact.

// PatternHit is one recorded firing plus its outcome.
type PatternHit struct {
	Time       string  `json:"time"`
	Symbol     string  `json:"symbol"`
	Pattern    string  `json:"pattern"` // "A" | "B"
	Price      float64 `json:"price"`
	OIChg5m    float64 `json:"oi_chg_5m"` // percent
	VolZ       float64 `json:"vol_z"`
	TakerPct   float64 `json:"taker_pct"`
	BasisBps   float64 `json:"basis_bps"`
	FundingBps float64 `json:"funding_bps"`
	SpotRatio  float64 `json:"spot_ratio"`
	RunBars    int     `json:"run_bars"`
	RunPct     float64 `json:"run_pct"` // percent
	Done       bool    `json:"done"`
	MFE5       float64 `json:"mfe_5m"` // percent
	MAE5       float64 `json:"mae_5m"`
	Ret5       float64 `json:"ret_5m"`
	MFE15      float64 `json:"mfe_15m"`
}

// PatternStat is the per-pattern tally — the numbers that decide whether the
// setup is worth anything.
type PatternStat struct {
	Pattern  string  `json:"pattern"`
	Total    int     `json:"total"`
	Done     int     `json:"done"`
	WinPct   float64 `json:"win_pct"`     // share reaching +1% within 5m
	MedMFE5  float64 `json:"med_mfe_5m"`  // percent
	MedMAE5  float64 `json:"med_mae_5m"`  // percent
	AvgRet5  float64 `json:"avg_ret_5m"`  // percent, net of a 0.3% round trip
	BestMFE5 float64 `json:"best_mfe_5m"` // percent
}

// PatternData is the /api/admin/patterns payload.
type PatternData struct {
	Hits      []PatternHit  `json:"hits"`
	Stats     []PatternStat `json:"stats"`
	Note      string        `json:"note"`
	UpdatedAt string        `json:"updated_at"`
}

// patternWinPct is the bar a firing must clear within five minutes to count as
// a win. Set just above a round trip's cost so "win" means "paid for itself",
// not merely "went up at all".
const patternWinPct = 0.01

// Patterns returns recent hits (newest first) and the running tally.
//
// Missing table or no collector yet is a normal state, not an error: the board
// simply reports that nothing has been recorded.
func (s *Store) Patterns(limit int) PatternData {
	out := PatternData{Hits: []PatternHit{}, Stats: []PatternStat{},
		UpdatedAt: time.Now().Format(time.RFC3339)}
	if s.db == nil {
		out.Note = "未連接資料庫"
		return out
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.sql.Query(`SELECT ts, symbol, pattern, price, oi_chg_5m, vol_z,
	        taker_pct, basis_bps, funding_bps, spot_ratio, run_bars, run_pct,
	        outcome_done, mfe_5m, mae_5m, ret_5m, mfe_15m
	      FROM pattern_hits ORDER BY ts DESC LIMIT ?`, limit)
	if err != nil {
		out.Note = "尚無資料 — 採集器(cmd/collector)還沒跑過,或還沒偵測到任何型態"
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var h PatternHit
		var ts int64
		var done int
		if err := rows.Scan(&ts, &h.Symbol, &h.Pattern, &h.Price, &h.OIChg5m, &h.VolZ,
			&h.TakerPct, &h.BasisBps, &h.FundingBps, &h.SpotRatio, &h.RunBars, &h.RunPct,
			&done, &h.MFE5, &h.MAE5, &h.Ret5, &h.MFE15); err != nil {
			continue
		}
		h.Time = time.UnixMilli(ts).UTC().Format("01-02 15:04")
		h.Done = done == 1
		// stored as fractions; the board reads in percent
		h.OIChg5m *= 100
		h.RunPct *= 100
		h.MFE5 *= 100
		h.MAE5 *= 100
		h.Ret5 *= 100
		h.MFE15 *= 100
		out.Hits = append(out.Hits, h)
	}
	out.Stats = patternStats(s.db.sql)
	if len(out.Hits) == 0 {
		out.Note = "尚未偵測到任何型態 — 條件刻意設得嚴格,一天只會有少數幾次"
	}
	return out
}

// patternStats tallies every recorded hit, not just the page being displayed.
func patternStats(db *sql.DB) []PatternStat {
	out := []PatternStat{}
	for _, pat := range []string{"A", "B"} {
		var st PatternStat
		st.Pattern = pat
		if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(outcome_done),0)
		                       FROM pattern_hits WHERE pattern = ?`, pat).
			Scan(&st.Total, &st.Done); err != nil {
			continue
		}
		if st.Done > 0 {
			// The averages cover only settled rows. Including pending ones would
			// score every fresh signal as a flat zero and drag the tally toward
			// nothing the longer the board is left open.
			var wins int
			var avgRet, bestMFE sql.NullFloat64
			db.QueryRow(`SELECT COUNT(*) FROM pattern_hits
			             WHERE pattern = ? AND outcome_done = 1 AND mfe_5m >= ?`,
				pat, patternWinPct).Scan(&wins)
			db.QueryRow(`SELECT AVG(ret_5m), MAX(mfe_5m) FROM pattern_hits
			             WHERE pattern = ? AND outcome_done = 1`, pat).
				Scan(&avgRet, &bestMFE)
			st.WinPct = float64(wins) / float64(st.Done) * 100
			// net of one round trip, so the number can be read as-is
			st.AvgRet5 = (avgRet.Float64 - 0.003) * 100
			st.BestMFE5 = bestMFE.Float64 * 100
			st.MedMFE5 = patternMedian(db, pat, "mfe_5m") * 100
			st.MedMAE5 = patternMedian(db, pat, "mae_5m") * 100
		}
		out = append(out, st)
	}
	return out
}

// patternMedian avoids window functions so the board works on MySQL 5.7 too.
func patternMedian(db *sql.DB, pat, col string) float64 {
	rows, err := db.Query(`SELECT `+col+` FROM pattern_hits
	                       WHERE pattern = ? AND outcome_done = 1 ORDER BY `+col, pat)
	if err != nil {
		return 0
	}
	defer rows.Close()
	var v []float64
	for rows.Next() {
		var f float64
		if rows.Scan(&f) == nil {
			v = append(v, f)
		}
	}
	if len(v) == 0 {
		return 0
	}
	if len(v)%2 == 1 {
		return v[len(v)/2]
	}
	return (v[len(v)/2-1] + v[len(v)/2]) / 2
}
