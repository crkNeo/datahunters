package collector

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"
)

// schema.go owns the DDL for the research tables and their daily partitions.
//
// Design rules these tables follow, and why:
//
//  1. RAW ONLY. Nothing derived is stored — no z-scores, no percentiles, no
//     "fuel score". Derived values are computed offline from this history with
//     a strictly backward-looking window. Storing a derived value freezes the
//     formula and, worse, invites a rewrite that quietly uses full-history
//     statistics — which is lookahead, and it makes any later backtest lie.
//
//  2. EVERY symbol in the universe, every minute — not just the interesting
//     ones. A log of only the coins that fired can tell you how often a signal
//     preceded a move; it can never tell you how often the move happened
//     WITHOUT the signal. Without that control group there is no lift, and
//     without lift there is no way to know whether a score has any information
//     in it at all.
//
//  3. PARTITION BY day from the start. On MySQL, deleting a slice of a large
//     time-series table with DELETE means row-by-row work, a mountain of undo
//     log, and space that never returns to the filesystem. With daily RANGE
//     partitions, retention is DROP PARTITION — near-instant, and the space is
//     actually freed. Retrofitting partitions onto a table that already holds
//     tens of millions of rows means a full table rebuild, so it has to be done
//     at creation time.
//
// The `day` column (YYYYMMDD, UTC) is redundant with `ts` on purpose: MySQL
// requires every column of a partitioning expression to appear in every unique
// key, and partitioning on a raw ms timestamp would need an unwieldy range list.
// It also lets queries prune partitions with a plain `WHERE day = ?`.

// partitionedTables are rebuilt daily and pruned by retention.
var partitionedTables = []string{"snap_1m", "depth_1m", "spot_1m", "regime_1m", "labels_1m"}

// tableDDL maps table name -> CREATE statement WITHOUT the partition clause.
// ensureSchema appends the clause, and falls back to the bare statement if the
// server refuses (partitioning can be disabled in a managed MySQL).
var tableDDL = map[string]string{

	// snap_1m is the core table: one row per symbol per closed 1-minute bar.
	"snap_1m": `CREATE TABLE IF NOT EXISTS snap_1m (
  day             INT    NOT NULL,
  ts              BIGINT NOT NULL,
  symbol          VARCHAR(32) NOT NULL,
  open            DOUBLE NOT NULL DEFAULT 0,
  high            DOUBLE NOT NULL DEFAULT 0,
  low             DOUBLE NOT NULL DEFAULT 0,
  close           DOUBLE NOT NULL DEFAULT 0,
  vol_quote       DOUBLE NOT NULL DEFAULT 0,
  trades          DOUBLE NOT NULL DEFAULT 0,
  taker_buy_quote DOUBLE NOT NULL DEFAULT 0,
  oi_contracts    DOUBLE NOT NULL DEFAULT 0,
  oi_usd          DOUBLE NOT NULL DEFAULT 0,
  funding         DOUBLE NOT NULL DEFAULT 0,
  next_funding_ts BIGINT NOT NULL DEFAULT 0,
  mark            DOUBLE NOT NULL DEFAULT 0,
  index_px        DOUBLE NOT NULL DEFAULT 0,
  PRIMARY KEY (day, ts, symbol),
  KEY idx_snap_sym_ts (symbol, ts)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	// depth_1m is sampled on a slower cadence than snap_1m: the depth endpoint
	// costs far more request weight than a kline, and book shape does not
	// change on a 60-second timescale the way trade flow does.
	// truncated=1 means the returned book ended before ±2%, so the outer bands
	// are lower bounds — the analysis must not treat them as measurements.
	"depth_1m": `CREATE TABLE IF NOT EXISTS depth_1m (
  day        INT    NOT NULL,
  ts         BIGINT NOT NULL,
  symbol     VARCHAR(32) NOT NULL,
  bid1       DOUBLE NOT NULL DEFAULT 0,
  ask1       DOUBLE NOT NULL DEFAULT 0,
  spread_bps DOUBLE NOT NULL DEFAULT 0,
  bid_usd_05 DOUBLE NOT NULL DEFAULT 0,
  bid_usd_1  DOUBLE NOT NULL DEFAULT 0,
  bid_usd_2  DOUBLE NOT NULL DEFAULT 0,
  ask_usd_05 DOUBLE NOT NULL DEFAULT 0,
  ask_usd_1  DOUBLE NOT NULL DEFAULT 0,
  ask_usd_2  DOUBLE NOT NULL DEFAULT 0,
  truncated  TINYINT NOT NULL DEFAULT 0,
  PRIMARY KEY (day, ts, symbol)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	// spot_1m exists for exactly one ratio: spot volume vs perp volume.
	// A move carried by perp volume with spot flat is leverage with nothing
	// underneath it and tends to retrace as fast as it went; a move where spot
	// leads and perp OI follows has real buyers and holds. On a few-minutes
	// holding period that distinction decides whether to chase or to leave.
	"spot_1m": `CREATE TABLE IF NOT EXISTS spot_1m (
  day             INT    NOT NULL,
  ts              BIGINT NOT NULL,
  symbol          VARCHAR(32) NOT NULL,
  close           DOUBLE NOT NULL DEFAULT 0,
  vol_quote       DOUBLE NOT NULL DEFAULT 0,
  taker_buy_quote DOUBLE NOT NULL DEFAULT 0,
  PRIMARY KEY (day, ts, symbol)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	// regime_1m is the market-state row: one per minute, not per symbol.
	// median_ret and disp are the cheap half of the regime gate — median_ret is
	// the baseline for residual (market-neutral) return, and disp says whether
	// money is picking coins or just moving the whole board. A screener that
	// fires the same way in both states is mostly trading beta.
	"regime_1m": `CREATE TABLE IF NOT EXISTS regime_1m (
  day          INT    NOT NULL,
  ts           BIGINT NOT NULL,
  btc_px       DOUBLE NOT NULL DEFAULT 0,
  btc_oi_usd   DOUBLE NOT NULL DEFAULT 0,
  eth_px       DOUBLE NOT NULL DEFAULT 0,
  eth_oi_usd   DOUBLE NOT NULL DEFAULT 0,
  total_oi_usd DOUBLE NOT NULL DEFAULT 0,
  adv_count    INT    NOT NULL DEFAULT 0,
  dec_count    INT    NOT NULL DEFAULT 0,
  median_ret   DOUBLE NOT NULL DEFAULT 0,
  disp         DOUBLE NOT NULL DEFAULT 0,
  universe     INT    NOT NULL DEFAULT 0,
  PRIMARY KEY (day, ts)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	// labels_1m is the outcome side, backfilled once each bar's forward window
	// has elapsed. Kept in its own table so the labeler never contends with the
	// collector's write path, and so labels can be recomputed from scratch
	// (different event thresholds, different horizons) without touching inputs.
	//
	// secs_to_peak_15m answers the question that decides the whole architecture:
	// if the median burst tops out in well under a minute, no human is clicking
	// fast enough and the only options are automation or trading the second leg.
	"labels_1m": `CREATE TABLE IF NOT EXISTS labels_1m (
  day              INT    NOT NULL,
  ts               BIGINT NOT NULL,
  symbol           VARCHAR(32) NOT NULL,
  base_close       DOUBLE NOT NULL DEFAULT 0,
  ret_5m           DOUBLE NOT NULL DEFAULT 0,
  ret_15m          DOUBLE NOT NULL DEFAULT 0,
  ret_30m          DOUBLE NOT NULL DEFAULT 0,
  ret_60m          DOUBLE NOT NULL DEFAULT 0,
  mfe_5m           DOUBLE NOT NULL DEFAULT 0,
  mae_5m           DOUBLE NOT NULL DEFAULT 0,
  mfe_15m          DOUBLE NOT NULL DEFAULT 0,
  mae_15m          DOUBLE NOT NULL DEFAULT 0,
  mfe_30m          DOUBLE NOT NULL DEFAULT 0,
  mae_30m          DOUBLE NOT NULL DEFAULT 0,
  mfe_60m          DOUBLE NOT NULL DEFAULT 0,
  mae_60m          DOUBLE NOT NULL DEFAULT 0,
  secs_to_peak_15m INT    NOT NULL DEFAULT 0,
  bars_5m          INT    NOT NULL DEFAULT 0,
  bars_60m         INT    NOT NULL DEFAULT 0,
  is_event         TINYINT NOT NULL DEFAULT 0,
  PRIMARY KEY (day, ts, symbol),
  KEY idx_lab_event (is_event, ts),
  KEY idx_lab_sym (symbol, ts)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
}

// collector_state is a tiny key/value table for progress markers.
//
// The labeler's position CANNOT be derived from MAX(labels_1m.ts): a stretch of
// time with no snapshots (the collector was down, or restarted) produces zero
// labels, so a derived watermark would never advance past the gap and the
// labeler would re-read the same empty window forever. An explicit marker
// advances whether or not the window produced rows.
const stateDDL = `CREATE TABLE IF NOT EXISTS collector_state (
  k VARCHAR(64) NOT NULL,
  v BIGINT NOT NULL DEFAULT 0,
  PRIMARY KEY (k)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`

// universe_1d is small (one row per symbol per day) and never needs pruning, so
// it stays unpartitioned. rank_vol is the 24h-turnover rank at selection time —
// recording it means a later analysis can ask whether lift is concentrated in a
// particular liquidity band.
const universeDDL = `CREATE TABLE IF NOT EXISTS universe_1d (
  day           INT    NOT NULL,
  symbol        VARCHAR(32) NOT NULL,
  status        VARCHAR(16) NOT NULL DEFAULT '',
  onboard_ts    BIGINT NOT NULL DEFAULT 0,
  quote_vol_24h DOUBLE NOT NULL DEFAULT 0,
  rank_vol      INT    NOT NULL DEFAULT 0,
  selected      TINYINT NOT NULL DEFAULT 0,
  PRIMARY KEY (day, symbol)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`

// unlock_events is the dated unlock schedule AS SEEN ON asof_day.
//
// The whole table is re-recorded daily rather than kept as one current view,
// because unlock schedules get REVISED — projects delay or accelerate, and the
// upstream dataset gets corrected. Joining today's schedule onto a snapshot
// from three months ago would use knowledge that did not exist at the time,
// which is lookahead of the most convincing kind: it makes the feature look
// prescient precisely because it was written after the fact.
const unlockEventsDDL = `CREATE TABLE IF NOT EXISTS unlock_events (
  asof_day  INT    NOT NULL,
  coin      VARCHAR(32) NOT NULL,
  unlock_ts BIGINT NOT NULL,
  category  VARCHAR(64) NOT NULL,
  amount    DOUBLE NOT NULL DEFAULT 0,
  PRIMARY KEY (asof_day, coin, unlock_ts, category),
  KEY idx_ue_coin_ts (coin, unlock_ts)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`

// unlock_snapshot_1d records, per coin per day, what was known about its
// unlocks — including the two negative cases that make the control group valid:
//
//	covered=0                 → the unlock source does not track this coin at
//	                            all. UNKNOWN. Must never be read as "no unlock".
//	covered=1, has_upcoming=0 → genuinely nothing scheduled in the horizon.
//
// price/circ/max_supply are the as-of values, stored raw so that magnitude
// measures (unlock USD against daily turnover, unlock as a share of float) stay
// reproducible offline instead of being frozen here.
const unlockSnapshotDDL = `CREATE TABLE IF NOT EXISTS unlock_snapshot_1d (
  day             INT    NOT NULL,
  coin            VARCHAR(32) NOT NULL,
  in_universe     TINYINT NOT NULL DEFAULT 0,
  covered         TINYINT NOT NULL DEFAULT 0,
  has_upcoming    TINYINT NOT NULL DEFAULT 0,
  next_unlock_ts  BIGINT NOT NULL DEFAULT 0,
  next_unlock_amt DOUBLE NOT NULL DEFAULT 0,
  horizon_amt     DOUBLE NOT NULL DEFAULT 0,
  events_n        INT    NOT NULL DEFAULT 0,
  price           DOUBLE NOT NULL DEFAULT 0,
  circ            DOUBLE NOT NULL DEFAULT 0,
  max_supply      DOUBLE NOT NULL DEFAULT 0,
  PRIMARY KEY (day, coin)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`

// dayKey renders a UTC time as the YYYYMMDD partition key.
func dayKey(t time.Time) int {
	t = t.UTC()
	return t.Year()*10000 + int(t.Month())*100 + t.Day()
}

// dayKeyMs is dayKey for a unix-ms timestamp.
func dayKeyMs(ms int64) int { return dayKey(time.UnixMilli(ms).UTC()) }

// ensureSchema creates the research tables if absent and makes sure partitions
// exist for the days about to be written. Safe to call repeatedly.
func ensureSchema(db *sql.DB, retentionDays int) error {
	for _, name := range partitionedTables {
		ddl, ok := tableDDL[name]
		if !ok {
			return fmt.Errorf("no DDL for %s", name)
		}
		// A brand-new table starts with a single sentinel partition; real days
		// are appended by ensurePartitions below. Partitioning can be disabled
		// server-side, so fall back to an ordinary table rather than refusing
		// to start — collecting unpartitioned data beats collecting none.
		partitioned := ddl + "\nPARTITION BY RANGE (day) (PARTITION p_start VALUES LESS THAN (20000101))"
		if _, err := db.Exec(partitioned); err != nil {
			log.Printf("collector: %s partitioned create failed (%v) — falling back to unpartitioned", name, err)
			if _, err2 := db.Exec(ddl); err2 != nil {
				return fmt.Errorf("create %s: %w", name, err2)
			}
		}
	}
	if _, err := db.Exec(universeDDL); err != nil {
		return fmt.Errorf("create universe_1d: %w", err)
	}
	if _, err := db.Exec(stateDDL); err != nil {
		return fmt.Errorf("create collector_state: %w", err)
	}
	if _, err := db.Exec(unlockEventsDDL); err != nil {
		return fmt.Errorf("create unlock_events: %w", err)
	}
	if _, err := db.Exec(unlockSnapshotDDL); err != nil {
		return fmt.Errorf("create unlock_snapshot_1d: %w", err)
	}
	return ensurePartitions(db, retentionDays)
}

// ensurePartitions adds partitions for today through today+2 (so a write at a
// UTC day boundary never lands with nowhere to go) and drops those older than
// the retention window. Tables that ended up unpartitioned are skipped.
func ensurePartitions(db *sql.DB, retentionDays int) error {
	now := time.Now().UTC()
	for _, name := range partitionedTables {
		have, err := existingPartitions(db, name)
		if err != nil {
			return err
		}
		if len(have) == 0 {
			continue // unpartitioned fallback — nothing to maintain
		}
		for i := 0; i <= 2; i++ {
			d := now.AddDate(0, 0, i)
			key := dayKey(d)
			pname := fmt.Sprintf("p%d", key)
			if have[pname] {
				continue
			}
			next := dayKey(d.AddDate(0, 0, 1))
			stmt := fmt.Sprintf("ALTER TABLE %s ADD PARTITION (PARTITION %s VALUES LESS THAN (%d))", name, pname, next)
			if _, err := db.Exec(stmt); err != nil {
				// Losing one ADD PARTITION is not fatal on its own — but every
				// later day would land in no partition at all, so surface it.
				log.Printf("collector: add partition %s.%s: %v", name, pname, err)
			}
		}
		if retentionDays > 0 {
			cutoff := dayKey(now.AddDate(0, 0, -retentionDays))
			for pname := range have {
				if pname == "p_start" || !strings.HasPrefix(pname, "p") {
					continue
				}
				var key int
				if _, err := fmt.Sscanf(pname, "p%d", &key); err != nil || key == 0 {
					continue
				}
				if key < cutoff {
					if _, err := db.Exec(fmt.Sprintf("ALTER TABLE %s DROP PARTITION %s", name, pname)); err != nil {
						log.Printf("collector: drop partition %s.%s: %v", name, pname, err)
					} else {
						log.Printf("collector: dropped %s.%s (retention %dd)", name, pname, retentionDays)
					}
				}
			}
		}
	}
	return nil
}

func existingPartitions(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(`SELECT PARTITION_NAME FROM information_schema.PARTITIONS
	                       WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND PARTITION_NAME IS NOT NULL`, table)
	if err != nil {
		return nil, fmt.Errorf("list partitions %s: %w", table, err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err == nil {
			out[n] = true
		}
	}
	return out, rows.Err()
}
