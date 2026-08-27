package collector

import (
	"log"
	"math"
	"time"

	"datahunter/internal/exchange"
)

// pattern_live.go — 「爆發型態」store-free 化的核心。
//
// 原本偵測要讀 snap_1m、結果回填要讀 snap_1m 的前瞻 bar。這裡改成:
//   偵測  → 用記憶體滾動窗(collector 每 tick 已抓到的 snapRow,不落表)
//   回填  → 即時抓該幣的 1m K 線算前瞻結果(重啟安全,不依賴任何快照表)
// 只有 pattern_hits(訊號 + 結果)仍持久化,供「爆發型態」分頁讀取。

// patternRing is bars kept per symbol: 90 covers the 60-bar vol z-score plus the
// longest setup window; a little margin above that.
const patternRing = 96

// pbarFromSnap builds a detection bar from one in-memory snapshot row. Mirrors the
// per-row processing loadPatternSeries did against snap_1m, so detection sees the
// exact same inputs — only the source changed (memory instead of DB).
func pbarFromSnap(s snapRow, mktRetPct float64) pbar {
	b := pbar{ts: s.Ts, open: s.Open, high: s.High, low: s.Low, close_: s.Close,
		volQuote: s.VolQuote, oiUSD: s.OIUSD, mktRet: mktRetPct}
	if s.VolQuote > 0 {
		b.takerPct = s.TakerBuyQuote / s.VolQuote * 100
	}
	b.fundingBps = s.Funding * 10000
	if s.IndexPx > 0 && s.Mark > 0 {
		b.basisBps = (s.Mark - s.IndexPx) / s.IndexPx * 10000
	}
	// spotRatio 保持 0:spot 已停採,passesCFilter 對 0 視為「無現貨對、未知」不擋。
	return b
}

// appendPatternRing records this minute's snapshots into the per-symbol ring and
// prunes symbols that have gone stale (dropped out of the tracked set).
func (c *Collector) appendPatternRing(snaps []snapRow, mktRetPct float64, barTs int64) {
	c.pmu.Lock()
	defer c.pmu.Unlock()
	for _, s := range snaps {
		if s.Close <= 0 {
			continue
		}
		bs := append(c.pring[s.Symbol], pbarFromSnap(s, mktRetPct))
		if len(bs) > patternRing {
			bs = bs[len(bs)-patternRing:]
		}
		c.pring[s.Symbol] = bs
	}
	for sym, bs := range c.pring { // 超過 ~2h 沒新 bar 的幣就釋放,記憶體有界
		if len(bs) == 0 || barTs-bs[len(bs)-1].ts > 120*60_000 {
			delete(c.pring, sym)
		}
	}
}

// patternSeries returns a copy of the ring with derived fields (oiChg5m, volZ)
// filled — the same shape DetectPatterns used to get from loadPatternSeries.
func (c *Collector) patternSeries() map[string][]pbar {
	c.pmu.Lock()
	out := make(map[string][]pbar, len(c.pring))
	for sym, bs := range c.pring {
		out[sym] = append([]pbar(nil), bs...)
	}
	c.pmu.Unlock()
	for _, bs := range out {
		fillPatternDerived(bs)
	}
	return out
}

// fillPatternDerived computes the two series-derived fields in place. Extracted
// from loadPatternSeries so the live path and the offline backtest stay identical.
func fillPatternDerived(bs []pbar) {
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

// RunPatternOutcomes backfills pattern_hits outcomes on an interval, fetching the
// fired coin's live 1m klines (no snap_1m dependency, restart-safe).
func (c *Collector) RunPatternOutcomes(every time.Duration, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case <-time.After(every):
			if err := c.backfillPatternOutcomes(); err != nil {
				log.Printf("patterns: %v", err)
			}
		}
	}
}

func (c *Collector) backfillPatternOutcomes() error {
	cutoff := time.Now().Add(-20 * time.Minute).UnixMilli()
	rows, err := c.db.Query(`SELECT day, ts, symbol, pattern, price FROM pattern_hits
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
		mfe5, mae5, ret5, mfe15, ok := forwardOutcome(c.ex, p.symbol, p.ts, p.price)
		if !ok {
			// 抓不到前瞻窗(幣已下市 / 訊號太舊 / API 錯)→ 仍標記完成,避免無限重試;結果留 0。
			log.Printf("patterns: outcome %s %s: 無法取得前瞻K線,標記完成(結果留空)", p.symbol, p.patt)
		}
		if _, err := c.db.Exec(`UPDATE pattern_hits SET outcome_done = 1,
		        mfe_5m = ?, mae_5m = ?, ret_5m = ?, mfe_15m = ?
		      WHERE day = ? AND ts = ? AND symbol = ? AND pattern = ?`,
			mfe5, mae5, ret5, mfe15, p.day, p.ts, p.symbol, p.patt); err != nil {
			log.Printf("patterns: outcome update: %v", err)
		}
	}
	return nil
}

// forwardOutcome fetches the fired coin's 1m klines and computes the same four
// forward measures the old snap_1m query produced, all as a fraction of entry:
//
//	mfe_5m  = max high in (ts, ts+5m]  / price - 1
//	mae_5m  = min low  in (ts, ts+5m]  / price - 1
//	ret_5m  = close of the last bar <= ts+5m / price - 1
//	mfe_15m = max high in (ts, ts+15m] / price - 1
//
// ok=false when the forward window isn't within the fetched range (signal older
// than ~185m) — the caller then marks the hit done with zero outcomes.
func forwardOutcome(ex *exchange.Client, sym string, ts int64, price float64) (mfe5, mae5, ret5, mfe15 float64, ok bool) {
	ks, err := ex.BinanceKlines(sym, "1m", 200)
	if err != nil || len(ks) == 0 {
		return 0, 0, 0, 0, false
	}
	end5, end15 := ts+5*60_000, ts+15*60_000
	var hi5, lo5, close5, hi15 float64
	var have5, have15 bool
	for _, k := range ks {
		if k.Ts <= ts || k.Ts > end15 {
			continue
		}
		if !have15 || k.High > hi15 {
			hi15 = k.High
		}
		have15 = true
		if k.Ts <= end5 {
			if !have5 {
				hi5, lo5 = k.High, k.Low
			} else {
				if k.High > hi5 {
					hi5 = k.High
				}
				if k.Low < lo5 {
					lo5 = k.Low
				}
			}
			close5 = k.Close
			have5 = true
		}
	}
	if !have15 {
		return 0, 0, 0, 0, false
	}
	f := func(v float64, valid bool) float64 {
		if !valid || v <= 0 {
			return 0
		}
		return v/price - 1
	}
	return f(hi5, have5), f(lo5, have5), f(close5, have5), f(hi15, have15), true
}
