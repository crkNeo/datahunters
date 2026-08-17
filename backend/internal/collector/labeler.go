package collector

import (
	"database/sql"
	"log"
	"sort"
	"time"
)

// labeler.go backfills what happened AFTER each snapshot: forward returns, and
// the maximum favourable / adverse excursion inside each horizon.
//
// Why MFE/MAE and not just the return: a take-profit is not hit by the return
// at the horizon, it is hit by the path. A bar that ends +2% after touching
// +14% is a winner for a +10% target and a loser for a "hold 15 minutes" rule.
// The distribution of MFE against MAE is what sets the target, the stop and the
// time stop — every one of those numbers is a guess until this table exists.
//
// The arithmetic is done in Go rather than SQL window functions so the tool runs
// unchanged on MySQL 5.7 and 8.0. At this data volume the difference is not
// worth a version dependency.

// LabelConfig tunes the backfill.
type LabelConfig struct {
	// EventPct is the move that counts as an "event" — the thing the whole
	// screener is trying to anticipate. 0.10 = +10% within 5 minutes.
	EventPct float64
	// ChunkMinutes is how many minutes of base bars to process per pass.
	ChunkMinutes int
	// LagMinutes is how far behind live to stay, so a bar is only labelled once
	// its full 60-minute window has been collected.
	LagMinutes int
}

func DefaultLabelConfig() LabelConfig {
	return LabelConfig{EventPct: 0.10, ChunkMinutes: 360, LagMinutes: 65}
}

// horizons are the forward windows, in minutes. 5m is the one that matters for
// a few-minutes holding period; the longer ones exist to show how much of the
// move was still available later — i.e. whether leaving early costs anything.
var horizons = []int{5, 15, 30, 60}

// RunLabeler backfills labels until it catches up, then repeats on an interval.
func RunLabeler(db *sql.DB, cfg LabelConfig, every time.Duration, stop <-chan struct{}) {
	for {
		for {
			n, more, err := labelOnce(db, cfg)
			if err != nil {
				log.Printf("labeler: %v", err)
				break
			}
			if n > 0 {
				log.Printf("labeler: wrote %d labels", n)
			}
			if !more {
				break
			}
			select {
			case <-stop:
				return
			default:
			}
		}
		select {
		case <-stop:
			return
		case <-time.After(every):
		}
	}
}

// labelOnce processes one chunk. It returns how many labels were written and
// whether more work remains.
//
// The watermark advances to the end of the chunk even when the chunk yielded no
// rows. A window with no snapshots behind it (collector downtime) is a normal
// occurrence, and a labeler that only moved when it produced output would sit
// on that window forever.
func labelOnce(db *sql.DB, cfg LabelConfig) (int, bool, error) {
	from, ok, err := labelWatermark(db)
	if err != nil || !ok {
		return 0, false, err
	}
	// Never label a bar whose forward window is still filling: a partial window
	// would record an MFE that simply had not happened yet, which is the most
	// direct way to manufacture a signal that cannot be traded.
	ceiling := time.Now().UTC().Add(-time.Duration(cfg.LagMinutes) * time.Minute).UnixMilli()
	if from >= ceiling {
		return 0, false, nil
	}
	chunkMs := int64(cfg.ChunkMinutes) * 60_000
	to := from + chunkMs
	more := true
	if to >= ceiling {
		to, more = ceiling, false
	}

	// Load base bars plus one full forward window beyond the chunk.
	maxH := horizons[len(horizons)-1]
	loadTo := to + int64(maxH)*60_000
	series, err := loadSeries(db, from, loadTo)
	if err != nil {
		return 0, false, err
	}

	var rows []labelRow
	for sym, bars := range series {
		for i, b := range bars {
			if b.ts < from || b.ts >= to {
				continue // forward-window padding, not a base bar
			}
			if b.close_ <= 0 {
				continue
			}
			rows = append(rows, computeLabel(sym, bars, i, cfg.EventPct))
		}
	}
	if err := writeLabels(db, rows); err != nil {
		return 0, false, err
	}
	if err := setState(db, labelWatermarkKey, to); err != nil {
		// Failing to persist the marker would replay this chunk after a
		// restart. INSERT IGNORE makes the replay harmless, but the caller
		// should still hear about it rather than loop silently.
		return len(rows), false, err
	}
	return len(rows), more, nil
}

type bar struct {
	ts                int64
	high, low, close_ float64
}

type labelRow struct {
	Ts        int64
	Symbol    string
	BaseClose float64
	Ret       map[int]float64
	MFE       map[int]float64
	MAE       map[int]float64
	SecsPeak  int
	Bars5     int
	Bars60    int
	IsEvent   bool
}

// computeLabel walks forward from bars[i] and measures each horizon.
//
// Windows are selected by TIMESTAMP, never by index offset: a gap in collection
// (a restart, a rate-limit pause) would otherwise silently stretch a "5 minute"
// window across an hour and corrupt exactly the rows most likely to sit next to
// interesting market conditions.
func computeLabel(sym string, bars []bar, i int, eventPct float64) labelRow {
	base := bars[i]
	r := labelRow{
		Ts: base.ts, Symbol: sym, BaseClose: base.close_,
		Ret: map[int]float64{}, MFE: map[int]float64{}, MAE: map[int]float64{},
	}
	var peakTs int64
	var peakHigh float64
	for _, h := range horizons {
		limit := base.ts + int64(h)*60_000
		var (
			hi, lo   float64
			lastC    float64
			n        int
			haveBand bool
		)
		for j := i + 1; j < len(bars); j++ {
			b := bars[j]
			if b.ts <= base.ts {
				continue
			}
			if b.ts > limit {
				break
			}
			if !haveBand {
				hi, lo, haveBand = b.high, b.low, true
			} else {
				if b.high > hi {
					hi = b.high
				}
				if b.low < lo {
					lo = b.low
				}
			}
			lastC = b.close_
			n++
			if h == 15 && b.high > peakHigh {
				peakHigh, peakTs = b.high, b.ts
			}
		}
		if !haveBand {
			continue
		}
		r.MFE[h] = hi/base.close_ - 1
		r.MAE[h] = lo/base.close_ - 1
		if lastC > 0 {
			r.Ret[h] = lastC/base.close_ - 1
		}
		if h == 5 {
			r.Bars5 = n
		}
		if h == 60 {
			r.Bars60 = n
		}
	}
	if peakTs > 0 {
		r.SecsPeak = int((peakTs - base.ts) / 1000)
	}
	r.IsEvent = r.MFE[5] >= eventPct
	return r
}

// loadSeries pulls snap_1m over a window and groups it by symbol, sorted by ts.
func loadSeries(db *sql.DB, from, to int64) (map[string][]bar, error) {
	rows, err := db.Query(`SELECT symbol, ts, high, low, close FROM snap_1m
	                       WHERE ts >= ? AND ts <= ? AND close > 0`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]bar{}
	for rows.Next() {
		var sym string
		var b bar
		if err := rows.Scan(&sym, &b.ts, &b.high, &b.low, &b.close_); err != nil {
			return nil, err
		}
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

const labelWatermarkKey = "label_watermark"

// labelWatermark is the timestamp of the next bar to label. It prefers the
// persisted marker; on a fresh database it starts at the oldest snapshot, and
// it tolerates an existing labels_1m written before the marker table existed.
func labelWatermark(db *sql.DB) (int64, bool, error) {
	if v, ok, err := getState(db, labelWatermarkKey); err != nil {
		return 0, false, err
	} else if ok && v > 0 {
		return v, true, nil
	}
	var maxLabel, minSnap sql.NullInt64
	if err := db.QueryRow(`SELECT MAX(ts) FROM labels_1m`).Scan(&maxLabel); err != nil {
		return 0, false, err
	}
	if maxLabel.Valid && maxLabel.Int64 > 0 {
		return maxLabel.Int64 + 60_000, true, nil
	}
	if err := db.QueryRow(`SELECT MIN(ts) FROM snap_1m`).Scan(&minSnap); err != nil {
		return 0, false, err
	}
	if !minSnap.Valid || minSnap.Int64 == 0 {
		return 0, false, nil // nothing collected yet
	}
	return minSnap.Int64, true, nil
}

func getState(db *sql.DB, key string) (int64, bool, error) {
	var v sql.NullInt64
	err := db.QueryRow(`SELECT v FROM collector_state WHERE k=?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return v.Int64, v.Valid, nil
}

func setState(db *sql.DB, key string, v int64) error {
	_, err := db.Exec(`INSERT INTO collector_state(k,v) VALUES(?,?)
	                   ON DUPLICATE KEY UPDATE v=VALUES(v)`, key, v)
	return err
}

func writeLabels(db *sql.DB, rows []labelRow) error {
	cols := []string{"day", "ts", "symbol", "base_close",
		"ret_5m", "ret_15m", "ret_30m", "ret_60m",
		"mfe_5m", "mae_5m", "mfe_15m", "mae_15m",
		"mfe_30m", "mae_30m", "mfe_60m", "mae_60m",
		"secs_to_peak_15m", "bars_5m", "bars_60m", "is_event"}
	return insertChunkRows(db, "labels_1m", cols, len(rows), func(i int) []any {
		r := rows[i]
		ev := 0
		if r.IsEvent {
			ev = 1
		}
		return []any{dayKeyMs(r.Ts), r.Ts, r.Symbol, r.BaseClose,
			r.Ret[5], r.Ret[15], r.Ret[30], r.Ret[60],
			r.MFE[5], r.MAE[5], r.MFE[15], r.MAE[15],
			r.MFE[30], r.MAE[30], r.MFE[60], r.MAE[60],
			r.SecsPeak, r.Bars5, r.Bars60, ev}
	})
}
