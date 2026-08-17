package collector

import (
	"database/sql"
	"fmt"
	"strings"
)

// store.go is the write path. Everything goes in as chunked multi-row
// INSERT IGNORE.
//
// Multi-row matters more than it looks: 100 symbols × 4 tables at one statement
// per row is 400 round trips and 400 commits every minute, on a connection pool
// the live dashboard is also using (SetMaxOpenConns(10) in cache/db.go). Batched
// into one statement per table per minute it is 4 round trips, and the collector
// stops competing with the server for connections.
//
// IGNORE rather than REPLACE/UPSERT because these rows are immutable facts about
// a closed bar. A restart mid-minute, or an overlapping backfill, should be a
// no-op — never a rewrite of history that a label was already computed against.

// snapRow is one symbol's closed 1-minute bar plus its derivatives state.
type snapRow struct {
	Ts            int64
	Symbol        string
	Open          float64
	High          float64
	Low           float64
	Close         float64
	VolQuote      float64
	Trades        float64
	TakerBuyQuote float64
	OIContracts   float64
	OIUSD         float64
	Funding       float64
	NextFundingTs int64
	Mark          float64
	IndexPx       float64
}

type depthRow struct {
	Ts        int64
	Symbol    string
	Bid1      float64
	Ask1      float64
	SpreadBps float64
	BidUSD05  float64
	BidUSD1   float64
	BidUSD2   float64
	AskUSD05  float64
	AskUSD1   float64
	AskUSD2   float64
	Truncated bool
}

type spotRow struct {
	Ts            int64
	Symbol        string
	Close         float64
	VolQuote      float64
	TakerBuyQuote float64
}

type regimeRow struct {
	Ts         int64
	BTCPx      float64
	BTCOIUSD   float64
	ETHPx      float64
	ETHOIUSD   float64
	TotalOIUSD float64
	AdvCount   int
	DecCount   int
	MedianRet  float64
	Disp       float64
	Universe   int
}

type universeRow struct {
	Day         int
	Symbol      string
	Status      string
	OnboardTs   int64
	QuoteVol24h float64
	RankVol     int
	Selected    bool
}

// execer is the subset of *sql.DB the write path uses. Keeping the writers on
// an interface lets row construction be unit-tested without a live database.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// insertChunkRows is the shared batching primitive: it builds
// `INSERT IGNORE INTO t (cols) VALUES (...),(...)` in chunks so a burst never
// exceeds max_allowed_packet, and so one bad chunk cannot lose the whole minute.
func insertChunkRows(db execer, table string, cols []string, n int, args func(i int) []any) error {
	if n == 0 {
		return nil
	}
	const chunk = 200
	tuple := "(" + strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",") + ")"
	head := fmt.Sprintf("INSERT IGNORE INTO %s (%s) VALUES ", table, strings.Join(cols, ","))

	var firstErr error
	for start := 0; start < n; start += chunk {
		end := start + chunk
		if end > n {
			end = n
		}
		vals := make([]string, 0, end-start)
		params := make([]any, 0, (end-start)*len(cols))
		for i := start; i < end; i++ {
			vals = append(vals, tuple)
			params = append(params, args(i)...)
		}
		if _, err := db.Exec(head+strings.Join(vals, ","), params...); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("insert %s: %w", table, err)
		}
	}
	return firstErr
}

func writeSnaps(db *sql.DB, rows []snapRow) error {
	cols := []string{"day", "ts", "symbol", "open", "high", "low", "close",
		"vol_quote", "trades", "taker_buy_quote", "oi_contracts", "oi_usd",
		"funding", "next_funding_ts", "mark", "index_px"}
	return insertChunkRows(db, "snap_1m", cols, len(rows), func(i int) []any {
		r := rows[i]
		return []any{dayKeyMs(r.Ts), r.Ts, r.Symbol, r.Open, r.High, r.Low, r.Close,
			r.VolQuote, r.Trades, r.TakerBuyQuote, r.OIContracts, r.OIUSD,
			r.Funding, r.NextFundingTs, r.Mark, r.IndexPx}
	})
}

func writeDepths(db *sql.DB, rows []depthRow) error {
	cols := []string{"day", "ts", "symbol", "bid1", "ask1", "spread_bps",
		"bid_usd_05", "bid_usd_1", "bid_usd_2",
		"ask_usd_05", "ask_usd_1", "ask_usd_2", "truncated"}
	return insertChunkRows(db, "depth_1m", cols, len(rows), func(i int) []any {
		r := rows[i]
		t := 0
		if r.Truncated {
			t = 1
		}
		return []any{dayKeyMs(r.Ts), r.Ts, r.Symbol, r.Bid1, r.Ask1, r.SpreadBps,
			r.BidUSD05, r.BidUSD1, r.BidUSD2, r.AskUSD05, r.AskUSD1, r.AskUSD2, t}
	})
}

func writeSpots(db *sql.DB, rows []spotRow) error {
	cols := []string{"day", "ts", "symbol", "close", "vol_quote", "taker_buy_quote"}
	return insertChunkRows(db, "spot_1m", cols, len(rows), func(i int) []any {
		r := rows[i]
		return []any{dayKeyMs(r.Ts), r.Ts, r.Symbol, r.Close, r.VolQuote, r.TakerBuyQuote}
	})
}

func writeRegime(db *sql.DB, r regimeRow) error {
	cols := []string{"day", "ts", "btc_px", "btc_oi_usd", "eth_px", "eth_oi_usd",
		"total_oi_usd", "adv_count", "dec_count", "median_ret", "disp", "universe"}
	return insertChunkRows(db, "regime_1m", cols, 1, func(int) []any {
		return []any{dayKeyMs(r.Ts), r.Ts, r.BTCPx, r.BTCOIUSD, r.ETHPx, r.ETHOIUSD,
			r.TotalOIUSD, r.AdvCount, r.DecCount, r.MedianRet, r.Disp, r.Universe}
	})
}

func writeUniverse(db *sql.DB, rows []universeRow) error {
	cols := []string{"day", "symbol", "status", "onboard_ts", "quote_vol_24h", "rank_vol", "selected"}
	return insertChunkRows(db, "universe_1d", cols, len(rows), func(i int) []any {
		r := rows[i]
		sel := 0
		if r.Selected {
			sel = 1
		}
		return []any{r.Day, r.Symbol, r.Status, r.OnboardTs, r.QuoteVol24h, r.RankVol, sel}
	})
}
