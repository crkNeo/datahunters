package collector

import (
	"database/sql"
	"fmt"
	"io"
	"math"
	"sort"
	"time"
)

// detail.go replays individual events minute by minute.
//
// The aggregate sections answer "what is typical across 228 events". That is
// the right question for deciding whether an edge exists, and the wrong one for
// understanding what actually happens: if bursts come in several different
// flavours — a squeeze, a news pop, a sector sympathy move — then a median over
// all of them describes none of them, and every precursor washes out against
// the others.
//
// This view also pulls in the three tables the aggregate analysis never touches
// — order-book depth, spot volume and market regime — which between them carry
// the variables most likely to explain WHY one coin moved 12% while another
// with identical flow moved 2%.

// detailRow is one minute around an event, assembled from every table.
type detailRow struct {
	ts        int64
	close_    float64
	ret1m     float64
	volQuote  float64
	volZ      float64
	takerPct  float64
	oiUSD     float64
	dOI5m     float64
	fundBps   float64
	basisBps  float64
	spotRatio float64 // spot quote volume / perp quote volume
	askUSD1   float64 // resting sell-side depth within 1%
	bidUSD1   float64
	hasDepth  bool
	mktRet    float64 // market median return that minute
}

// RunEventDetail prints a minute-by-minute replay of the largest events.
func RunEventDetail(db *sql.DB, cfg AnalyzeConfig, w io.Writer, topN, before, after int) error {
	from := int64(0)
	if cfg.Days > 0 {
		from = time.Now().Add(-time.Duration(cfg.Days) * 24 * time.Hour).UnixMilli()
	}
	series, err := loadAnalysisSeries(db, from)
	if err != nil {
		return err
	}
	eps, _, _ := splitEpisodesFull(series, cfg)
	if len(eps) == 0 {
		fmt.Fprintln(w, "這段期間沒有符合門檻的事件。")
		return nil
	}
	// biggest first — the clearest specimens are the most informative to read
	sort.Slice(eps, func(i, j int) bool { return eps[i].mfe5 > eps[j].mfe5 })
	if topN > 0 && topN < len(eps) {
		eps = eps[:topN]
	}

	rep := newReport(w)
	dir := dirOf(cfg.Side)
	word := "暴漲"
	if dir < 0 {
		word = "暴跌"
	}
	rep.head(fmt.Sprintf("逐事件回放 — 最大的 %d 次%s,前 %d 分鐘到後 %d 分鐘",
		len(eps), word, before, after))
	rep.line("  T-0 是錨點(該幅度最早可被察覺的那一根),尖峰通常落在其後幾分鐘。")
	rep.line("  volZ = 成交量對該幣前 60 分鐘的 z 分數;主買%% = 主動買量佔比;")
	rep.line("  ΔOI5m = 持倉量 5 分鐘變化;現貨比 = 現貨成交額 ÷ 合約成交額;")
	rep.line("  賣深/買深 = ±1%% 內掛單美元(每 5 分鐘取樣,空白代表該分鐘沒取);")
	rep.line("  大盤 = 當分鐘全市場報酬中位數,用來分辨個股行情與整體 beta。")

	for _, e := range eps {
		rows, err := loadDetail(db, e.symbol, e.ts, before, after)
		if err != nil {
			return err
		}
		rep.eventTable(e, rows, before)
	}
	return nil
}

func loadDetail(db *sql.DB, symbol string, anchorTs int64, before, after int) ([]detailRow, error) {
	lo := anchorTs - int64(before+60)*60_000 // extra hour of history for the z-score
	hi := anchorTs + int64(after)*60_000

	type snap struct {
		ts                                         int64
		close_, volQuote, taker, oiUSD, fu, mk, ix float64
	}
	var snaps []snap
	rows, err := db.Query(`SELECT ts, close, vol_quote, taker_buy_quote, oi_usd, funding, mark, index_px
	                       FROM snap_1m WHERE symbol=? AND ts>=? AND ts<=? ORDER BY ts`,
		symbol, lo, hi)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var s snap
		if err := rows.Scan(&s.ts, &s.close_, &s.volQuote, &s.taker, &s.oiUSD, &s.fu, &s.mk, &s.ix); err != nil {
			rows.Close()
			return nil, err
		}
		snaps = append(snaps, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	spot := map[int64]float64{}
	if r2, err := db.Query(`SELECT ts, vol_quote FROM spot_1m WHERE symbol=? AND ts>=? AND ts<=?`,
		symbol, lo, hi); err == nil {
		for r2.Next() {
			var ts int64
			var v float64
			if r2.Scan(&ts, &v) == nil {
				spot[ts] = v
			}
		}
		r2.Close()
	}
	type dep struct{ ask, bid float64 }
	depth := map[int64]dep{}
	if r3, err := db.Query(`SELECT ts, ask_usd_1, bid_usd_1 FROM depth_1m WHERE symbol=? AND ts>=? AND ts<=?`,
		symbol, lo, hi); err == nil {
		for r3.Next() {
			var ts int64
			var d dep
			if r3.Scan(&ts, &d.ask, &d.bid) == nil {
				depth[ts] = d
			}
		}
		r3.Close()
	}
	mkt := map[int64]float64{}
	if r4, err := db.Query(`SELECT ts, median_ret FROM regime_1m WHERE ts>=? AND ts<=?`, lo, hi); err == nil {
		for r4.Next() {
			var ts int64
			var v float64
			if r4.Scan(&ts, &v) == nil {
				mkt[ts] = v
			}
		}
		r4.Close()
	}

	out := make([]detailRow, 0, len(snaps))
	for i, s := range snaps {
		if s.ts < anchorTs-int64(before)*60_000 {
			continue // history kept only to seed the z-score
		}
		d := detailRow{ts: s.ts, close_: s.close_, volQuote: s.volQuote, oiUSD: s.oiUSD,
			fundBps: s.fu * 10000, mktRet: mkt[s.ts] * 100}
		if i > 0 && snaps[i-1].close_ > 0 {
			d.ret1m = (s.close_/snaps[i-1].close_ - 1) * 100
		}
		if s.volQuote > 0 {
			d.takerPct = s.taker / s.volQuote * 100
			if sv, ok := spot[s.ts]; ok {
				d.spotRatio = sv / s.volQuote
			}
		}
		if i >= 5 && snaps[i-5].oiUSD > 0 && s.oiUSD > 0 {
			d.dOI5m = (s.oiUSD/snaps[i-5].oiUSD - 1) * 100
		}
		if s.ix > 0 && s.mk > 0 {
			d.basisBps = (s.mk - s.ix) / s.ix * 10000
		}
		// volume z-score against the preceding hour of this same coin
		if i >= 60 {
			var sum, sum2 float64
			for j := i - 60; j < i; j++ {
				sum += snaps[j].volQuote
				sum2 += snaps[j].volQuote * snaps[j].volQuote
			}
			mean := sum / 60
			if v := sum2/60 - mean*mean; v > 0 {
				d.volZ = (s.volQuote - mean) / math.Sqrt(v)
			}
		}
		if dp, ok := depth[s.ts]; ok {
			d.askUSD1, d.bidUSD1, d.hasDepth = dp.ask, dp.bid, true
		}
		out = append(out, d)
	}
	return out, nil
}

func (r *report) eventTable(e episode, rows []detailRow, before int) {
	r.line("")
	r.rule()
	r.line("  %s   錨點 %s UTC   事件幅度 %.1f%%   進場後可得 %.1f%%",
		e.symbol, time.UnixMilli(e.ts).UTC().Format("01-02 15:04"), e.mfe5*100, e.visMFE*100)
	r.rule()
	r.line("  %6s %10s %7s %8s %6s %7s %9s %7s %7s %8s %9s %9s %7s",
		"T", "價格", "1m%", "量(K)", "volZ", "主買%", "OI(M)", "ΔOI5m", "資費bp", "基差bp", "現貨比", "賣深(K)", "大盤%")
	r.line("  %s", "-------------------------------------------------------------------------------------------------------------")

	for _, d := range rows {
		off := int((d.ts - e.ts) / 60_000)
		mark := fmt.Sprintf("%+d", off)
		if off == 0 {
			mark = "  T0"
		}
		depthCell := "       -"
		if d.hasDepth {
			depthCell = fmt.Sprintf("%8.0f", d.askUSD1/1000)
		}
		spotCell := "        -"
		if d.spotRatio > 0 {
			spotCell = fmt.Sprintf("%9.2f", d.spotRatio)
		}
		r.line("  %6s %10.6g %+7.2f %8.0f %+6.1f %7.1f %9.2f %+7.2f %7.2f %+8.1f %s %s %+7.2f",
			mark, d.close_, d.ret1m, d.volQuote/1000, d.volZ, d.takerPct,
			d.oiUSD/1e6, d.dOI5m, d.fundBps, d.basisBps, spotCell, depthCell, d.mktRet)
	}
}
